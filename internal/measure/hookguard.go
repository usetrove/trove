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
	Sessions    int `json:"sessions"`
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
		SUM(kind = 'reissued'), SUM(kind = 'fetched_back')
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
			&s.Rewritten, &s.WouldRewrite, &rewriteTok, &s.Reissued, &s.FetchedBack); err != nil {
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
)

// hookRule says how one hook optimizer is judged: of the events in the
// denominator kind, what share also produced the numerator kind.
type hookRule struct {
	optimizer, rule string
	denominator     string // events that count as the optimizer acting
	numerator       string // events that mean the action did not help
	what            string // plain English, for the reason
}

var hookRules = []hookRule{
	{config.OptimizerHookRepeatRead, RuleHookReread, "blocked", "reread_anyway", "read the same file again after %d of %d blocked reads"},
	{config.OptimizerHookQuietCommands, RuleHookRerun, "rewritten", "reissued", "ran %d of %d quieted commands again without the quiet flag"},
	{config.OptimizerOutputTrimmer, RuleHookFetchBack, "rewritten", "fetched_back", "fetched back %d of %d trimmed outputs"},
}

// EvaluateHooks is the guardrail pass for hook-based optimizers. For each of them
// it rolls the optimizer back to shadow when, since the last time the user
// cleared a rollback, at least m.MinSessions actions were counted and the share
// that did not help (a block the agent retried, a quieted command it ran again,
// a trimmed output it fetched back) reached m.RefetchRatePercent. These are the
// hook's own counts ("measured"); they cannot see what an action cost in extra
// turns, which is what the A/B comparison is for. It is safe to call repeatedly
// and returns the rollbacks it recorded.
func EvaluateHooks(ctx context.Context, db *store.DB, m config.Measure, now time.Time) ([]Event, error) {
	active, err := Overrides(ctx, db)
	if err != nil {
		return nil, err
	}
	var events []Event
	for _, r := range hookRules {
		if _, rolled := active[r.optimizer]; rolled {
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
		if err := db.QueryRowContext(ctx, `SELECT COALESCE(SUM(kind = ?), 0), COALESCE(SUM(kind = ?), 0)
			FROM hook_events WHERE hook = ? AND ts >= ?`, r.denominator, r.numerator, r.optimizer, since).Scan(&acted, &bad); err != nil {
			return events, err
		}
		if acted < m.MinSessions || acted == 0 {
			continue
		}
		rate := float64(bad) / float64(acted)
		threshold := float64(m.RefetchRatePercent) / 100
		if rate < threshold {
			continue
		}
		ev := Event{At: now.UTC(), Optimizer: r.optimizer, Rule: r.rule, Observed: rate, Threshold: threshold,
			SampleOn: acted, Reference: RefNone,
			Reason: fmt.Sprintf("Trove set %s back to shadow because the agent "+r.what+" (%.0f%%, limit %d%%). This is measured from the hook's own counts; re-enable it deliberately if you disagree.",
				r.optimizer, bad, acted, rate*100, m.RefetchRatePercent)}
		if err := recordRollback(ctx, db, ev); err != nil {
			return events, err
		}
		events = append(events, ev)
	}
	return events, nil
}
