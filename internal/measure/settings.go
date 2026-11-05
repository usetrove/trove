package measure

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/store"
)

// Verdicts for a settings profile.
const (
	VerdictNotEnough     = "not_enough_data"
	VerdictNotMeasurable = "not_measurable"
	VerdictLessUsage     = "less_usage"
	VerdictMoreUsage     = "more_usage"
	VerdictNoClearChange = "no_clear_change"
)

// Outcome verdicts.
const (
	OutcomeWorse         = "worse"
	OutcomeNoClearChange = "no_clear_change"
	OutcomeUnknown       = "unknown"
)

// SettingsMetric is one before/after comparison across sessions.
type SettingsMetric struct {
	Metric string  `json:"metric"`
	Before float64 `json:"before"`
	After  float64 `json:"after"`
	// Percent is the change from before to after (negative = lower after).
	Percent float64 `json:"percent_change"`
	Clear   bool    `json:"clear"` // the difference is larger than chance at 95%
	NBefore int     `json:"n_before"`
	NAfter  int     `json:"n_after"`
	Label   Label   `json:"label"`
}

// SettingsEffect is what the data shows about one applied settings profile.
//
// A settings file has no on/off arm to interleave, so like FixEffects this
// compares sessions that started before it with sessions that started after.
// Trove does not record which project or task a session belongs to (that
// would be a path or text), so the comparison covers every session of the
// harness. It supports but does not prove an effect, and the text says so.
type SettingsEffect struct {
	FixID     string    `json:"fix_id"`
	Task      string    `json:"task"`
	Harness   string    `json:"harness"`
	Keys      []string  `json:"keys"`
	AppliedAt time.Time `json:"applied_at"`
	NBefore   int       `json:"n_before_sessions"`
	NAfter    int       `json:"n_after_sessions"`
	Verdict   string    `json:"verdict"`
	// Usage is output tokens per session (thinking is billed as output).
	Usage *SettingsMetric `json:"usage,omitempty"`
	// Prompt is prompt tokens per request, which compaction settings change.
	Prompt *SettingsMetric `json:"prompt,omitempty"`
	// Outcomes are tool errors, turns and abandoned sessions. Whether tests
	// passed is not something Trove records.
	Outcomes       []SettingsMetric `json:"outcomes,omitempty"`
	OutcomeVerdict string           `json:"outcome_verdict"`
	Label          Label            `json:"label"`
	OtherFixes     int              `json:"other_fixes_in_place,omitempty"`
	Text           string           `json:"text"`
	// Recommend is set when outcomes got worse: the command that reverses it.
	Recommend string `json:"recommend,omitempty"`
}

type settingsRow struct {
	fixID, task, harness string
	keys                 []string
	applied              int64
}

type sessionStats struct {
	start      int64
	harness    string
	output     float64
	perReq     float64
	turns      float64
	toolErrors float64
	hasErrors  bool
	abandoned  float64
	hasAband   bool
}

// SettingsEffects reports on every settings profile still in place, newest first.
// It reads counts only, never text.
func SettingsEffects(ctx context.Context, db *store.DB, m config.Measure) ([]SettingsEffect, error) {
	rows, err := db.QueryContext(ctx, `SELECT p.fix_id, p.task, p.harness, p.keys, f.applied_at
		FROM settings_profiles p JOIN fixes f ON f.change_id = p.change_id
		WHERE f.reverted_at IS NULL ORDER BY f.applied_at DESC`)
	if err != nil {
		return nil, err
	}
	var profs []settingsRow
	for rows.Next() {
		var r settingsRow
		var keys string
		if err := rows.Scan(&r.fixID, &r.task, &r.harness, &keys, &r.applied); err != nil {
			_ = rows.Close()
			return nil, err
		}
		_ = json.Unmarshal([]byte(keys), &r.keys)
		profs = append(profs, r)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil || len(profs) == 0 {
		return nil, err
	}
	var inPlace int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fixes WHERE reverted_at IS NULL`).Scan(&inPlace); err != nil {
		return nil, err
	}
	sess, err := sessionStatsAll(ctx, db)
	if err != nil {
		return nil, err
	}
	out := make([]SettingsEffect, 0, len(profs))
	for _, p := range profs {
		out = append(out, settingsEffect(p, sess, m, inPlace-1))
	}
	return out, nil
}

func settingsEffect(p settingsRow, sess []sessionStats, m config.Measure, others int) SettingsEffect {
	e := SettingsEffect{FixID: p.fixID, Task: p.task, Harness: p.harness, Keys: p.keys,
		AppliedAt: time.UnixMilli(p.applied).UTC(), Label: Estimated, OutcomeVerdict: OutcomeUnknown, OtherFixes: max(others, 0)}
	name := describeKeys(p.keys) + " for " + p.task
	if p.harness == "codex" {
		// A Codex profile only applies to sessions started with --profile, and
		// Trove cannot see which ones were. Comparing before and after would
		// mix sessions that used it with sessions that did not.
		e.Verdict = VerdictNotMeasurable
		e.Text = name + ": no effect is shown. A Codex profile only applies to sessions you start with `codex --profile`, and Trove cannot tell which sessions those were, so a before/after comparison would be wrong."
		return e
	}
	var before, after []sessionStats
	for _, s := range sess {
		if s.harness != p.harness {
			continue
		}
		if s.start < p.applied {
			before = append(before, s)
		} else {
			after = append(after, s)
		}
	}
	e.NBefore, e.NAfter = len(before), len(after)
	if len(before) < m.MinSessions || len(after) < m.MinSessions {
		e.Verdict = VerdictNotEnough
		e.Text = fmt.Sprintf("%s: %s: %d session(s) before and %d after; at least %d are needed on each side. No effect is shown yet.", name, TextNotEnough, len(before), len(after), m.MinSessions)
		return e
	}
	e.Label = Measured
	pick := func(f func(sessionStats) (float64, bool)) (b, a []float64) {
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
		return b, a
	}
	metric := func(name string, f func(sessionStats) (float64, bool)) *SettingsMetric {
		b, a := pick(f)
		if len(b) < m.MinSessions || len(a) < m.MinSessions {
			return nil
		}
		return compareMetric(name, b, a)
	}
	e.Usage = metric("output_tokens_per_session", func(s sessionStats) (float64, bool) { return s.output, true })
	e.Prompt = metric("prompt_tokens_per_request", func(s sessionStats) (float64, bool) { return s.perReq, true })
	for _, o := range []*SettingsMetric{
		metric("tool_errors_per_session", func(s sessionStats) (float64, bool) { return s.toolErrors, s.hasErrors }),
		metric("turns_per_session", func(s sessionStats) (float64, bool) { return s.turns, true }),
		metric("abandoned_sessions_rate", func(s sessionStats) (float64, bool) { return s.abandoned, s.hasAband }),
	} {
		if o != nil {
			e.Outcomes = append(e.Outcomes, *o)
		}
	}
	e.Verdict = VerdictNoClearChange
	if u := e.Usage; u != nil && u.Clear {
		e.Verdict = VerdictLessUsage
		if u.After > u.Before {
			e.Verdict = VerdictMoreUsage
		}
	}
	if len(e.Outcomes) > 0 {
		e.OutcomeVerdict = OutcomeNoClearChange
		for _, o := range e.Outcomes {
			if o.Clear && o.After > o.Before {
				e.OutcomeVerdict = OutcomeWorse
			}
		}
	}
	if e.OutcomeVerdict == OutcomeWorse || e.Verdict == VerdictMoreUsage {
		e.Recommend = "trove undo " + p.fixID
	}
	e.Text = settingsText(name, e)
	return e
}

func compareMetric(name string, b, a []float64) *SettingsMetric {
	mb, ma := mean(b), mean(a)
	se := math.Sqrt(sampleVar(b)/float64(len(b)) + sampleVar(a)/float64(len(a)))
	mt := &SettingsMetric{Metric: name, Before: mb, After: ma, NBefore: len(b), NAfter: len(a), Label: Measured,
		Clear: se > 0 && math.Abs(ma-mb) > z95*se}
	if mb != 0 {
		mt.Percent = 100 * (ma - mb) / mb
	}
	return mt
}

func settingsText(name string, e SettingsEffect) string {
	var b strings.Builder
	b.WriteString(name + ": ")
	switch {
	case e.Usage == nil:
		b.WriteString("no usage figure")
	case e.Verdict == VerdictLessUsage:
		fmt.Fprintf(&b, "%.0f%% less output per session", -e.Usage.Percent)
	case e.Verdict == VerdictMoreUsage:
		fmt.Fprintf(&b, "%.0f%% more output per session", e.Usage.Percent)
	default:
		b.WriteString("no clear change in output per session")
	}
	fmt.Fprintf(&b, " (measured, n=%d before / %d after sessions)", e.NBefore, e.NAfter)
	switch e.OutcomeVerdict {
	case OutcomeWorse:
		b.WriteString("; outcomes got worse: " + worseList(e.Outcomes))
	case OutcomeNoClearChange:
		b.WriteString("; no clear change in tool errors, turns or abandoned sessions")
	default:
		b.WriteString("; no outcome figures yet")
	}
	b.WriteString(". Trove does not see whether tests pass. All sessions of the harness are compared, not just this project or task, so this supports but does not prove an effect")
	if e.OtherFixes > 0 {
		b.WriteString("; other fixes are in place too, so their effects are mixed together")
	}
	b.WriteString(".")
	return b.String()
}

func worseList(ms []SettingsMetric) string {
	var w []string
	for _, m := range ms {
		if m.Clear && m.After > m.Before {
			w = append(w, strings.ReplaceAll(m.Metric, "_", " "))
		}
	}
	return strings.Join(w, ", ")
}

func describeKeys(keys []string) string {
	k := append([]string(nil), keys...)
	sort.Strings(k)
	switch len(k) {
	case 0:
		return "settings profile"
	case 1:
		return k[0]
	}
	return fmt.Sprintf("%s and %d more", k[0], len(k)-1)
}

// sessionStatsAll returns one row per session, from the gateway and from agent
// history the gateway did not also record.
func sessionStatsAll(ctx context.Context, db *store.DB) ([]sessionStats, error) {
	var out []sessionStats
	seen := map[string]bool{}
	rows, err := db.QueryContext(ctx, `SELECT session_key, agent, started_at, request_count, total_output_tokens,
		CAST(total_input_uncached_tokens + total_cache_write_tokens + total_cache_read_tokens AS REAL) / request_count,
		outcome_signals FROM sessions WHERE request_count > 0`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k string
		var s sessionStats
		var sig *string
		var turns int
		if err := rows.Scan(&k, &s.harness, &s.start, &turns, &s.output, &s.perReq, &sig); err != nil {
			_ = rows.Close()
			return nil, err
		}
		s.turns = float64(turns)
		if sig != nil {
			var ss SessionSignals
			if json.Unmarshal([]byte(*sig), &ss) == nil {
				s.hasAband, s.abandoned = true, b2f(ss.Abandoned)
				if ss.HasTools {
					s.hasErrors, s.toolErrors = true, float64(ss.ToolErrors)
				}
			}
		}
		seen[k] = true
		out = append(out, s)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = db.QueryContext(ctx, `SELECT session_key, source, MIN(ts), COUNT(*), SUM(output_tokens),
		CAST(SUM(input_tokens + cache_write_5m + cache_write_1h + cache_write_other + cache_read) AS REAL) / COUNT(*),
		SUM(errors) FROM history_requests WHERE sidechain = 0 GROUP BY session_key, source`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var k string
		var s sessionStats
		var turns int
		var errs int64
		if err := rows.Scan(&k, &s.harness, &s.start, &turns, &s.output, &s.perReq, &errs); err != nil {
			return nil, err
		}
		if seen[k] {
			continue
		}
		s.turns, s.hasErrors, s.toolErrors = float64(turns), true, float64(errs)
		out = append(out, s)
	}
	return out, rows.Err()
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
