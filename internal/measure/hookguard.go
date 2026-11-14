package measure

import (
	"context"
	"fmt"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/store"
)

// RuleHookReread is the guardrail for hook_repeat_read: of the reads it blocked,
// how many did the agent retry anyway. A high rate means the block is not helping
// (the agent needed the file again), so the hook goes back to shadow.
const RuleHookReread = "hook_reread"

// hookWindow is how far back the guardrail looks, so an old bad stretch does not
// count against a hook after the cause was fixed.
const hookWindow = 14 * 24 * time.Hour

// HookStats counts what one hook did. Everything here is a count the hook saw
// itself ("measured") except TokensSaved, which is derived from file sizes
// ("estimated").
type HookStats struct {
	Hook         string `json:"hook"`
	Blocked      int    `json:"blocked"`
	WouldBlock   int    `json:"would_block"`
	Advised      int    `json:"advised"`
	WouldAdvise  int    `json:"would_advise"`
	RereadAnyway int    `json:"reread_anyway"`
	// Rewritten counts tool inputs or outputs a hook really changed; WouldRewrite
	// counts the ones it would have changed in shadow mode. Both are counts the
	// hook saw itself ("measured").
	Rewritten    int `json:"rewritten,omitempty"`
	WouldRewrite int `json:"would_rewrite,omitempty"`
	// TokensRewritten is the tokens those rewrites removed, from sizes only
	// ("estimated"). It says nothing about whether the agent needed them: judge
	// that with the A/B comparison, not with this number.
	TokensRewritten *Figure `json:"tokens_rewritten,omitempty"`
	// Reissued counts quieted commands the agent ran again as first written;
	// FetchedBack counts trimmed outputs it fetched back with trove_fetch_full.
	Reissued    int `json:"reissued,omitempty"`
	FetchedBack int `json:"fetched_back,omitempty"`
	// ExtraRead counts files the agent read more of after hook_read_limit limited a read; Adopted counts
	// sessions where the agent used trove_run (once per session). Both are counts the hook saw itself.
	ExtraRead int `json:"extra_read,omitempty"`
	Adopted   int `json:"adopted,omitempty"`
	Sessions  int `json:"sessions"`
	// TokensSaved is the estimated tokens kept out of the context by blocked
	// reads that were not retried. Blocks the agent retried save nothing.
	TokensSaved Figure `json:"tokens_saved"`
	// RereadRate is retried blocks as a percent of blocked reads (measured); nil
	// until at least one read was blocked.
	RereadRate *Figure `json:"reread_rate_percent,omitempty"`
}

// HookTotals summarises hook_events since the given time, one entry per hook
// that has any rows, ordered by name.
func HookTotals(ctx context.Context, db *store.DB, since time.Time) ([]HookStats, error) {
	rows, err := db.QueryContext(ctx, `SELECT hook,
		SUM(kind = 'blocked'), SUM(kind = 'would_block'), SUM(kind = 'advised'), SUM(kind = 'would_advise'), SUM(kind = 'reread_anyway'),
		COUNT(DISTINCT session_key),
		COALESCE(SUM(CASE WHEN kind = 'blocked' THEN tokens END), 0),
		COALESCE(SUM(CASE WHEN kind = 'reread_anyway' THEN tokens END), 0),
		SUM(kind = 'rewritten'), SUM(kind = 'would_rewrite'),
		COALESCE(SUM(CASE WHEN kind = 'rewritten' THEN tokens END), 0),
		SUM(kind = 'reissued'), SUM(kind = 'fetched_back'), SUM(kind = 'extra_read'), SUM(kind = 'adopted')
		FROM hook_events WHERE ts >= ? GROUP BY hook ORDER BY hook`, since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []HookStats
	for rows.Next() {
		var s HookStats
		var blockedTok, rereadTok, rewriteTok int64
		if err := rows.Scan(&s.Hook, &s.Blocked, &s.WouldBlock, &s.Advised, &s.WouldAdvise, &s.RereadAnyway, &s.Sessions, &blockedTok, &rereadTok,
			&s.Rewritten, &s.WouldRewrite, &rewriteTok, &s.Reissued, &s.FetchedBack, &s.ExtraRead, &s.Adopted); err != nil {
			return nil, err
		}
		saved := blockedTok - rereadTok // a retried block put the file back, so it saved nothing
		if saved < 0 {
			saved = 0
		}
		s.TokensSaved = Figure{Value: float64(saved), Label: Estimated, N: s.Blocked}
		if s.Rewritten > 0 {
			s.TokensRewritten = &Figure{Value: float64(rewriteTok), Label: Estimated, N: s.Rewritten}
		}
		if s.Blocked > 0 {
			s.RereadRate = &Figure{Value: 100 * float64(s.RereadAnyway) / float64(s.Blocked), Label: Measured, N: s.Blocked}
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Guardrail rules for hook optimizers, recorded in guardrail_events.
const (
	// RuleHookRerun: the agent ran a quieted command again, as first written.
	RuleHookRerun = "hook_rerun"
	// RuleHookFetchBack: the agent fetched back output a hook trimmed.
	RuleHookFetchBack = "hook_fetch_back"
	// RuleHookExtraRead: the agent read more of a file after hook_read_limit limited a read of it.
	RuleHookExtraRead = "hook_extra_read"
	// RuleHookUnusedAdvice: in sessions where hook_large_output suggested trove_run, the agent did not use it.
	RuleHookUnusedAdvice = "hook_unused_advice"
)

// Placeholder limits for the rules that are not a plain "share the agent undid". They are not tuned by any
// benchmark; the docs and the rollback text say so. A user can still re-enable a rolled-back optimizer.
const (
	// extraReadLimitFactor: a limited read followed by another read is what the limit's note asks for, so
	// it is only a warning sign when it is common. The limit is this many times measure.refetch_rate_percent
	// (default 25, so 50 percent), capped at 100.
	extraReadLimitFactor = 2
	// trimmerLimitDivisor: the output trimmer hides text, so it is judged more strictly than the rest. Its
	// limit is measure.refetch_rate_percent divided by this (default 25, so 12 percent), and at least 1.
	trimmerLimitDivisor = 2
	// MinAdoptionPercent is the share of sessions where hook_large_output gave its hint in which the agent must
	// then use trove_run, or the hint is only a cost. Below it the hook goes back to shadow.
	MinAdoptionPercent = 10
)

// hookRule says how one hook optimizer is judged: of the events in the
// denominator kind, what share also produced the numerator kind.
type hookRule struct {
	optimizer, rule string
	denominator     string // events that count as the optimizer acting
	numerator       string // events that mean the action did not help
	// what says in plain English what happened, for the reason. bad and acted are the counts.
	what func(bad, acted int) string
	// limit is the percent at which the optimizer is rolled back; nil means m.RefetchRatePercent.
	limit func(m config.Measure) int
	// adoption marks the one rule judged per session and in the opposite direction: acted is the number of
	// sessions that were advised, numerator counts the sessions among them where the advice was followed,
	// and the rest are the bad ones. It can only run when the hook that records adoption is installed.
	adoption bool
}

func countsOf(format string) func(bad, acted int) string {
	return func(bad, acted int) string { return fmt.Sprintf(format, bad, acted) }
}

var hookRules = []hookRule{
	{optimizer: config.OptimizerHookRepeatRead, rule: RuleHookReread, denominator: "blocked", numerator: "reread_anyway",
		what: countsOf("read the same file again after %d of %d blocked reads")},
	{optimizer: config.OptimizerHookQuietCommands, rule: RuleHookRerun, denominator: "rewritten", numerator: "reissued",
		what: countsOf("ran %d of %d quieted commands again without the quiet flag")},
	{optimizer: config.OptimizerOutputTrimmer, rule: RuleHookFetchBack, denominator: "rewritten", numerator: "fetched_back",
		what:  countsOf("fetched back %d of %d trimmed outputs"),
		limit: func(m config.Measure) int { return max(1, m.RefetchRatePercent/trimmerLimitDivisor) }},
	{optimizer: config.OptimizerHookReadLimit, rule: RuleHookExtraRead, denominator: "rewritten", numerator: "extra_read",
		what:  countsOf("read more of the file after %d of %d limited reads"),
		limit: func(m config.Measure) int { return min(100, m.RefetchRatePercent*extraReadLimitFactor) }},
	{optimizer: config.OptimizerHookLargeOutput, rule: RuleHookUnusedAdvice, denominator: "advised", numerator: "adopted", adoption: true,
		what: func(bad, acted int) string {
			return fmt.Sprintf("did not use trove_run in %d of %d sessions where Trove suggested it", bad, acted)
		},
		limit: func(config.Measure) int { return 100 - MinAdoptionPercent }},
}

// HookGuardOptions says what the guardrail can see in this install.
type HookGuardOptions struct {
	// AdoptionObservable is true when the hook that records trove_run use is installed in Claude Code. Without
	// it a hint nobody could be seen following would look unfollowed, so that rule is skipped.
	AdoptionObservable bool
}

// EvaluateHooks is EvaluateHooksWith with nothing extra observable: the adoption rule does not run.
func EvaluateHooks(ctx context.Context, db *store.DB, m config.Measure, now time.Time) ([]Event, error) {
	return EvaluateHooksWith(ctx, db, m, now, HookGuardOptions{})
}

// unusedAdvice counts, for the adoption rule, the Claude Code sessions in which the optimizer gave its hint
// (acted) and how many of those did not then use trove_run (bad). Codex sessions are left out: its hooks
// cannot see trove_run, so every Codex session would look as if the hint was ignored.
func unusedAdvice(ctx context.Context, db *store.DB, r hookRule, since int64) (acted, bad int, err error) {
	var followed int
	err = db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT a.session_key),
			COUNT(DISTINCT CASE WHEN EXISTS (SELECT 1 FROM hook_events u WHERE u.hook = a.hook AND u.kind = ? AND u.session_key = a.session_key AND u.ts >= ?) THEN a.session_key END)
		FROM hook_events a WHERE a.hook = ? AND a.kind = ? AND a.ts >= ? AND a.session_key NOT LIKE 'x-%'`,
		r.numerator, since, r.optimizer, r.denominator, since).Scan(&acted, &followed)
	return acted, acted - followed, err
}

// EvaluateHooksWith is the guardrail pass for hook-based optimizers. For each of them
// it rolls the optimizer back to shadow when, since the last time the user
// cleared a rollback, at least m.MinSessions actions were counted and the share
// that did not help (a block the agent retried, a quieted command it ran again,
// a trimmed output it fetched back) reached m.RefetchRatePercent. These are the
// hook's own counts ("measured"); they cannot see what an action cost in extra
// turns, which is what the A/B comparison is for. It is safe to call repeatedly
// and returns the rollbacks it recorded.
func EvaluateHooksWith(ctx context.Context, db *store.DB, m config.Measure, now time.Time, opts HookGuardOptions) ([]Event, error) {
	active, err := Overrides(ctx, db)
	if err != nil {
		return nil, err
	}
	var events []Event
	for _, r := range hookRules {
		if _, rolled := active[r.optimizer]; rolled {
			continue
		}
		if r.adoption && !opts.AdoptionObservable {
			continue
		}
		cleared, err := lastCleared(ctx, db, r.optimizer)
		if err != nil {
			return events, err
		}
		since := now.Add(-hookWindow).UnixMilli()
		if cleared > since {
			since = cleared
		}
		var acted, bad int
		if r.adoption {
			acted, bad, err = unusedAdvice(ctx, db, r, since)
		} else {
			err = db.QueryRowContext(ctx, `SELECT COALESCE(SUM(kind = ?), 0), COALESCE(SUM(kind = ?), 0)
				FROM hook_events WHERE hook = ? AND ts >= ?`, r.denominator, r.numerator, r.optimizer, since).Scan(&acted, &bad)
		}
		if err != nil {
			return events, err
		}
		if acted < m.MinSessions || acted == 0 {
			continue
		}
		limitPercent := m.RefetchRatePercent
		if r.limit != nil {
			limitPercent = r.limit(m)
		}
		rate := float64(bad) / float64(acted)
		threshold := float64(limitPercent) / 100
		if rate < threshold {
			continue
		}
		ev := Event{At: now.UTC(), Optimizer: r.optimizer, Rule: r.rule, Observed: rate, Threshold: threshold,
			SampleOn: acted, Reference: RefNone,
			Reason: fmt.Sprintf("Trove set %s back to shadow because the agent %s (%.0f%%, limit %d%%). This is measured from the hook's own counts; re-enable it deliberately if you disagree.",
				r.optimizer, r.what(bad, acted), rate*100, limitPercent)}
		if err := recordRollback(ctx, db, ev); err != nil {
			return events, err
		}
		events = append(events, ev)
	}
	return events, nil
}
