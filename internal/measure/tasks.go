package measure

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/store"
)

// TasksEffect is what the data shows about an installed scout or cascade (see
// docs/tasks.md). Files have no on/off arm to interleave, so, like
// SettingsEffects, this compares sessions that started before the install with
// sessions that started after. It reads counts, model names and costs from
// agent history; never text.
//
// What it can and cannot see:
//   - Delegation is the share of sessions with any subagent request. Trove does
//     not parse which subagent ran, so a session that used another subagent
//     counts too. It is labeled as such.
//   - "Finished on" is the model of the last subagent request (else the last request). The cascade's
//     command asks the agent to say which model finished; Trove does not read
//     that text.
//   - Trove does not know whether tests passed. Cost per session and the
//     history error counts are the only outcome proxies.
//   - Sessions are not split by project or task.
//   - Session-level A/B (arms chosen at session start) is not built for these
//     features yet; the figures here support but do not prove an effect.
type TasksEffect struct {
	Feature   string    `json:"feature"`
	AppliedAt time.Time `json:"applied_at"`
	FixIDs    []string  `json:"fix_ids"`
	NBefore   int       `json:"n_before_sessions"`
	NAfter    int       `json:"n_after_sessions"`
	Verdict   string    `json:"verdict"`
	// DelegationRate is the percent of sessions after the install that used any
	// subagent.
	DelegationRate *float64 `json:"delegation_rate_percent,omitempty"`
	// SidechainShare is the percent of tokens (all token kinds) in sessions after
	// the install that ran in subagents, so stayed out of the main conversation.
	SidechainShare *float64 `json:"subagent_token_share_percent,omitempty"`
	// Cost is USD per session; sessions with unpriced requests are left out.
	Cost *SettingsMetric `json:"cost_per_session,omitempty"`
	// Errors is tool errors per session.
	Errors *SettingsMetric `json:"errors_per_session,omitempty"`
	// Escalated is the percent of after-install sessions whose subagents ran on
	// two or more different models (cascade only).
	Escalated *float64 `json:"escalated_percent,omitempty"`
	// FinishedOn counts after-install sessions by the model of the last request.
	FinishedOn map[string]int `json:"finished_on,omitempty"`
	// OverheadRequest and OverheadUse are the declared, estimated token overhead.
	OverheadRequest int    `json:"overhead_tokens_per_request"`
	OverheadUse     int    `json:"overhead_tokens_per_use"`
	Label           Label  `json:"label"`
	Text            string `json:"text"`
	// Recommend is set when cost or errors clearly got worse.
	Recommend string `json:"recommend,omitempty"`
}

type taskSession struct {
	start        int64
	tokens       float64
	sideTokens   float64
	cost         float64
	priced       bool
	errors       float64
	lastTS       int64
	lastModel    string
	lastSideTS   int64
	lastSide     string
	sideModels   map[string]bool
	usedSidechan bool
}

// TasksEffects reports on each scout or cascade still in place.
func TasksEffects(ctx context.Context, db *store.DB, m config.Measure) ([]TasksEffect, error) {
	rows, err := db.QueryContext(ctx, `SELECT t.feature, t.fix_id, f.applied_at, t.overhead_request, t.overhead_use
		FROM task_installs t JOIN fixes f ON f.change_id = t.change_id
		WHERE f.reverted_at IS NULL ORDER BY f.applied_at`)
	if err != nil {
		return nil, err
	}
	type inst struct {
		fixes            []string
		applied          int64
		overReq, overUse int
	}
	byFeature := map[string]*inst{}
	var order []string
	for rows.Next() {
		var feat, fix string
		var at int64
		var a, u int
		if err := rows.Scan(&feat, &fix, &at, &a, &u); err != nil {
			_ = rows.Close()
			return nil, err
		}
		in := byFeature[feat]
		if in == nil {
			in = &inst{applied: at}
			byFeature[feat] = in
			order = append(order, feat)
		}
		in.fixes = append(in.fixes, fix)
		in.overReq, in.overUse = max(in.overReq, a), max(in.overUse, u) // every row carries the feature's total
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil || len(byFeature) == 0 {
		return nil, err
	}
	sess, err := taskSessions(ctx, db)
	if err != nil {
		return nil, err
	}
	var out []TasksEffect
	for _, feat := range order {
		in := byFeature[feat]
		out = append(out, tasksEffect(feat, in.fixes, in.applied, in.overReq, in.overUse, sess, m))
	}
	return out, nil
}

func tasksEffect(feat string, fixes []string, applied int64, overReq, overUse int, sess []taskSession, m config.Measure) TasksEffect {
	e := TasksEffect{Feature: feat, FixIDs: fixes, AppliedAt: time.UnixMilli(applied).UTC(), Label: Estimated,
		OverheadRequest: overReq, OverheadUse: overUse}
	name := map[string]string{"task_scout": "Scout subagent", "task_cascade": "Cascade workflow"}[feat]
	var before, after []taskSession
	for _, s := range sess {
		if s.start < applied {
			before = append(before, s)
		} else {
			after = append(after, s)
		}
	}
	e.NBefore, e.NAfter = len(before), len(after)
	// Delegation and token share need only the after side, and are plain counts.
	if len(after) > 0 {
		var used, esc int
		var tok, side float64
		e.FinishedOn = map[string]int{}
		for _, s := range after {
			if s.usedSidechan {
				used++
			}
			if len(s.sideModels) >= 2 {
				esc++
			}
			tok += s.tokens
			side += s.sideTokens
			// The main thread usually writes the last message, so a session that used
			// subagents is credited to the model of its last subagent request.
			switch {
			case s.lastSide != "":
				e.FinishedOn[s.lastSide]++
			case s.lastModel != "":
				e.FinishedOn[s.lastModel]++
			}
		}
		d := 100 * float64(used) / float64(len(after))
		e.DelegationRate = &d
		if tok > 0 {
			sh := 100 * side / tok
			e.SidechainShare = &sh
		}
		if feat == "task_cascade" {
			p := 100 * float64(esc) / float64(len(after))
			e.Escalated = &p
		}
	}
	if len(before) < m.MinSessions || len(after) < m.MinSessions {
		e.Verdict = VerdictNotEnough
		e.Text = fmt.Sprintf("%s: %s: %d session(s) before and %d after; at least %d are needed on each side. No cost effect is shown yet.%s",
			name, TextNotEnough, len(before), len(after), m.MinSessions, tasksUsage(e))
		return e
	}
	e.Label = Measured
	pick := func(f func(taskSession) (float64, bool)) *SettingsMetric {
		var b, a []float64
		for _, s := range before {
			if v, ok := f(s); ok {
				b = append(b, v)
			}
		}
		for _, s := range after {
			if v, ok := f(s); ok {
				a = append(a, v)
			}
		}
		if len(b) < m.MinSessions || len(a) < m.MinSessions {
			return nil
		}
		return compareMetric("", b, a)
	}
	e.Cost = pick(func(s taskSession) (float64, bool) { return s.cost, s.priced })
	if e.Cost != nil {
		e.Cost.Metric = "cost_usd_per_session"
	}
	e.Errors = pick(func(s taskSession) (float64, bool) { return s.errors, true })
	if e.Errors != nil {
		e.Errors.Metric = "errors_per_session"
	}
	e.Verdict = VerdictNoClearChange
	switch c := e.Cost; {
	case c != nil && c.Clear && c.After < c.Before:
		e.Verdict = VerdictLessUsage
	case c != nil && c.Clear:
		e.Verdict = VerdictMoreUsage
	}
	worse := e.Errors != nil && e.Errors.Clear && e.Errors.After > e.Errors.Before
	if worse || e.Verdict == VerdictMoreUsage {
		e.Recommend = "trove undo " + strings.Join(fixes, " ")
	}
	var b strings.Builder
	b.WriteString(name + ": ")
	switch {
	case e.Cost == nil:
		b.WriteString("no cost figure (too few priced sessions)")
	case e.Verdict == VerdictLessUsage:
		fmt.Fprintf(&b, "%.0f%% lower cost per session", -e.Cost.Percent)
	case e.Verdict == VerdictMoreUsage:
		fmt.Fprintf(&b, "%.0f%% higher cost per session", e.Cost.Percent)
	default:
		b.WriteString("no clear change in cost per session")
	}
	fmt.Fprintf(&b, " (measured, n=%d before / %d after sessions)", len(before), len(after))
	if worse {
		b.WriteString("; tool errors per session went up")
	}
	b.WriteString("." + tasksUsage(e))
	b.WriteString(" Trove does not see whether tests pass, and compares all sessions of the agent, not just this project or task, so this supports but does not prove an effect.")
	e.Text = b.String()
	return e
}

func tasksUsage(e TasksEffect) string {
	var b strings.Builder
	if e.DelegationRate != nil {
		fmt.Fprintf(&b, " %.0f%% of sessions since used a subagent (any subagent; estimated, n=%d).", *e.DelegationRate, e.NAfter)
	}
	if e.SidechainShare != nil {
		fmt.Fprintf(&b, " %.0f%% of their tokens ran in subagents (estimated).", *e.SidechainShare)
	}
	if e.Escalated != nil {
		fmt.Fprintf(&b, " %.0f%% of sessions used subagents on two or more models (estimated; a sign of escalation).", *e.Escalated)
	}
	if len(e.FinishedOn) > 0 {
		keys := make([]string, 0, len(e.FinishedOn))
		for k := range e.FinishedOn {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s %d", k, e.FinishedOn[k]))
		}
		b.WriteString(" Last subagent request (or last request, if none) by model: " + strings.Join(parts, ", ") + ".")
	}
	fmt.Fprintf(&b, " Overhead charged: about %d tokens per request and %d per use (estimated).", e.OverheadRequest, e.OverheadUse)
	return b.String()
}

// taskSessions returns one row per Claude Code session in agent history.
func taskSessions(ctx context.Context, db *store.DB) ([]taskSession, error) {
	rows, err := db.QueryContext(ctx, `SELECT session_key, ts, model, sidechain, cost_usd,
		input_tokens + cache_write_5m + cache_write_1h + cache_write_other + cache_read + output_tokens, errors
		FROM history_requests WHERE source = 'claude_code' ORDER BY session_key, ts`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []taskSession
	idx := map[string]int{}
	unpriced := map[string]bool{}
	for rows.Next() {
		var k, model string
		var ts int64
		var side int
		var cost *float64
		var tok, errs float64
		if err := rows.Scan(&k, &ts, &model, &side, &cost, &tok, &errs); err != nil {
			return nil, err
		}
		i, ok := idx[k]
		if !ok {
			i = len(out)
			idx[k] = i
			out = append(out, taskSession{start: ts, sideModels: map[string]bool{}})
		}
		s := &out[i]
		s.tokens += tok
		s.errors += errs
		if cost == nil {
			unpriced[k] = true
		} else {
			s.cost += *cost
		}
		if side != 0 {
			s.usedSidechan = true
			s.sideTokens += tok
			if model != "" {
				s.sideModels[model] = true
				if ts >= s.lastSideTS {
					s.lastSideTS, s.lastSide = ts, model
				}
			}
		}
		if ts >= s.lastTS {
			s.lastTS, s.lastModel = ts, model
		}
	}
	for k, i := range idx {
		out[i].priced = !unpriced[k]
	}
	return out, rows.Err()
}
