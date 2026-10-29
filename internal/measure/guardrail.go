package measure

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/store"
)

// Rule names recorded in guardrail_events.
const (
	RuleRetries    = "retries"
	RuleToolErrors = "tool_errors"
	RuleRefetch    = "refetch"
)

// Reference kinds: what the optimizer's sessions were compared with.
const (
	RefOffSessions = "off_sessions" // sessions after the first "on" session where it was not on
	RefBaseline    = "baseline"     // sessions before it was ever on
	RefNone        = "none"
)

// RuleResult is one rule's verdict for one optimizer.
type RuleResult struct {
	Rule       string     `json:"rule"`
	Comparison Comparison `json:"comparison"`
	Text       string     `json:"text"` // plain English, safe to print
}

// OptimizerCheck is the guardrail state of one optimizer.
type OptimizerCheck struct {
	Optimizer string       `json:"optimizer"`
	Reference string       `json:"reference"`
	Rules     []RuleResult `json:"rules"`
	// Verdict is Regression if any rule tripped, NotEnoughData if every rule
	// lacked data, otherwise NoRegression.
	Verdict    Verdict `json:"verdict"`
	RolledBack bool    `json:"rolled_back"`
	// Summary is the sentence for the report. It never claims quality is unchanged.
	Summary string `json:"summary"`
}

type finishedSession struct {
	Key string
	SessionSignals
}

func loadFinished(ctx context.Context, db *store.DB) ([]finishedSession, error) {
	rows, err := db.QueryContext(ctx, `SELECT session_key, outcome_signals FROM sessions WHERE outcome_signals IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []finishedSession
	for rows.Next() {
		var k, raw string
		if err := rows.Scan(&k, &raw); err != nil {
			return nil, err
		}
		fs := finishedSession{Key: k}
		if json.Unmarshal([]byte(raw), &fs.SessionSignals) != nil {
			continue // written by a future version; skip rather than guess
		}
		out = append(out, fs)
	}
	return out, rows.Err()
}

func lastCleared(ctx context.Context, db *store.DB, optimizer string) (int64, error) {
	var ms *int64
	err := db.QueryRowContext(ctx, `SELECT MAX(cleared_at) FROM guardrail_events WHERE optimizer = ?`, optimizer).Scan(&ms)
	if err != nil || ms == nil {
		return 0, err
	}
	return *ms, nil
}

func has(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Check compares every optimizer that has run "on" against its reference and
// returns the verdicts. It changes nothing.
func Check(ctx context.Context, db *store.DB, m config.Measure) ([]OptimizerCheck, error) {
	all, err := loadFinished(ctx, db)
	if err != nil {
		return nil, err
	}
	active, err := Overrides(ctx, db)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, s := range all {
		for _, n := range s.On {
			names[n] = true
		}
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)

	var out []OptimizerCheck
	for _, name := range sorted {
		cleared, err := lastCleared(ctx, db, name)
		if err != nil {
			return nil, err
		}
		_, rolled := active[name]
		out = append(out, checkOne(name, all, m, cleared, rolled))
	}
	return out, nil
}

func checkOne(name string, all []finishedSession, m config.Measure, clearedAtMS int64, rolled bool) OptimizerCheck {
	var firstOn int64
	for _, s := range all {
		if has(s.On, name) && (firstOn == 0 || s.PinnedAt < firstOn) {
			firstOn = s.PinnedAt
		}
	}
	var treated, baseline, off []finishedSession
	for _, s := range all {
		switch {
		case has(s.On, name):
			if s.PinnedAt > clearedAtMS { // sessions from before a rollback was lifted do not count again
				treated = append(treated, s)
			}
		case s.PinnedAt < firstOn:
			baseline = append(baseline, s)
		default:
			off = append(off, s)
		}
	}
	ref, kind := baseline, RefBaseline
	if len(off) >= m.MinSessions || len(off) > len(baseline) {
		ref, kind = off, RefOffSessions
	}
	if len(ref) == 0 {
		kind = RefNone
	}

	chk := OptimizerCheck{Optimizer: name, Reference: kind, RolledBack: rolled}
	vals := func(ss []finishedSession, f func(SessionSignals) float64) []float64 {
		var v []float64
		for _, s := range ss {
			if s.HasTools {
				v = append(v, f(s.SessionSignals))
			}
		}
		return v
	}
	minN, relRetry := m.MinSessions, float64(m.RetryIncreasePercent)/100

	rules := []struct {
		name string
		c    Comparison
		what string
	}{
		{RuleRetries, compareMeans(vals(treated, func(s SessionSignals) float64 { return float64(s.Repeats) }),
			vals(ref, func(s SessionSignals) float64 { return float64(s.Repeats) }), minN, relRetry), "repeated identical tool calls per session"},
		{RuleToolErrors, compareMeans(vals(treated, func(s SessionSignals) float64 { return float64(s.ToolErrors) }),
			vals(ref, func(s SessionSignals) float64 { return float64(s.ToolErrors) }), minN, relRetry), "tool errors per session"},
	}
	var k, n int
	for _, s := range treated {
		if s.HasTools && s.Collapsed > 0 {
			n++
			if s.Refetches > 0 {
				k++
			}
		}
	}
	rules = append(rules, struct {
		name string
		c    Comparison
		what string
	}{RuleRefetch, compareRateToLimit(k, n, minN, float64(m.RefetchRatePercent)/100), "sessions that re-fetched a result an optimizer removed"})

	chk.Verdict = NotEnoughData
	for _, r := range rules {
		rr := RuleResult{Rule: r.name, Comparison: r.c, Text: describe(r.name, r.what, r.c, kind, m)}
		chk.Rules = append(chk.Rules, rr)
		switch r.c.Verdict {
		case Regression:
			chk.Verdict = Regression
		case NoRegression:
			if chk.Verdict != Regression {
				chk.Verdict = NoRegression
			}
		}
	}
	switch chk.Verdict {
	case NotEnoughData:
		chk.Summary = fmt.Sprintf("%s: %s (%d sessions with the optimizer on; at least %d needed on each side)", name, TextNotEnough, len(treated), m.MinSessions)
	case NoRegression:
		chk.Summary = fmt.Sprintf("%s: %s (%d sessions on, %d %s sessions)", name, TextNoRegression, len(treated), len(ref), kind)
	default:
		chk.Summary = fmt.Sprintf("%s: regression detected; set back to shadow for new sessions", name)
	}
	return chk
}

func describe(rule, what string, c Comparison, kind string, m config.Measure) string {
	if c.Verdict == NotEnoughData {
		return fmt.Sprintf("%s: %s (%d on, %d %s)", what, TextNotEnough, c.NTreated, c.NRef, kind)
	}
	if rule == RuleRefetch {
		return fmt.Sprintf("%s: %.0f%% of %d sessions (limit %d%%)", what, c.Treated*100, c.NTreated, m.RefetchRatePercent)
	}
	if c.Ref == 0 {
		return fmt.Sprintf("%s: %.2f vs %.2f (+%.2f) over %d on and %d %s sessions", what, c.Treated, c.Ref, c.Change, c.NTreated, c.NRef, kind)
	}
	return fmt.Sprintf("%s: %.2f vs %.2f (%+.0f%%) over %d on and %d %s sessions", what, c.Treated, c.Ref, c.Change*100, c.NTreated, c.NRef, kind)
}

// Event is a recorded rollback.
type Event struct {
	ID        int64     `json:"id"`
	At        time.Time `json:"at"`
	Optimizer string    `json:"optimizer"`
	Rule      string    `json:"rule"`
	Observed  float64   `json:"observed"`
	Threshold float64   `json:"threshold"`
	SampleOn  int       `json:"sample_on"`
	SampleRef int       `json:"sample_reference"`
	Reference string    `json:"reference"`
	Reason    string    `json:"reason"`
	ClearedAt *int64    `json:"cleared_at,omitempty"`
}

// Evaluate runs Check and, for every optimizer that tripped a rule and is not
// already rolled back, records the event and switches it back to shadow for
// new sessions. Sessions already running keep their pinned state (CLAUDE.md
// rule 5). It is safe to call repeatedly.
func Evaluate(ctx context.Context, db *store.DB, m config.Measure, now time.Time) ([]OptimizerCheck, []Event, error) {
	checks, err := Check(ctx, db, m)
	if err != nil {
		return nil, nil, err
	}
	var events []Event
	for i, c := range checks {
		if c.Verdict != Regression || c.RolledBack {
			continue
		}
		var trip RuleResult
		for _, r := range c.Rules {
			if r.Comparison.Verdict == Regression {
				trip = r
				break
			}
		}
		threshold := float64(m.RetryIncreasePercent) / 100
		observed := trip.Comparison.Change
		if trip.Rule == RuleRefetch {
			threshold, observed = float64(m.RefetchRatePercent)/100, trip.Comparison.Treated
		}
		reason := fmt.Sprintf("Trove set %s back to shadow because %s. This is a local, statistical signal; review it with `trove report`, and re-enable it deliberately if you disagree.", c.Optimizer, trip.Text)
		ev := Event{At: now.UTC(), Optimizer: c.Optimizer, Rule: trip.Rule, Observed: observed, Threshold: threshold,
			SampleOn: trip.Comparison.NTreated, SampleRef: trip.Comparison.NRef, Reference: c.Reference, Reason: reason}
		if err := recordRollback(ctx, db, ev); err != nil {
			return checks, events, err
		}
		checks[i].RolledBack = true
		events = append(events, ev)
	}
	return checks, events, nil
}

func recordRollback(ctx context.Context, db *store.DB, ev Event) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO guardrail_events
		(ts, optimizer, rule, observed, threshold, sample_on, sample_ref, reference, reason) VALUES (?,?,?,?,?,?,?,?,?)`,
		ev.At.UnixMilli(), ev.Optimizer, ev.Rule, ev.Observed, ev.Threshold, ev.SampleOn, ev.SampleRef, ev.Reference, ev.Reason); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO optimizations(name, tier, state, changed_at, reason) VALUES(?, '', 'shadow', ?, ?)
		ON CONFLICT(name) DO UPDATE SET state = 'shadow', changed_at = excluded.changed_at, reason = excluded.reason`,
		ev.Optimizer, ev.At.UnixMilli(), guardrailPrefix+ev.Reason); err != nil {
		return err
	}
	return tx.Commit()
}

// GuardrailEvents lists rollbacks, newest first, for the report.
func GuardrailEvents(ctx context.Context, db *store.DB, limit int) ([]Event, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, ts, optimizer, rule, observed, threshold, sample_on, sample_ref, reference, reason, cleared_at
		FROM guardrail_events ORDER BY ts DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Event{}
	for rows.Next() {
		var e Event
		var ts int64
		if err := rows.Scan(&e.ID, &ts, &e.Optimizer, &e.Rule, &e.Observed, &e.Threshold, &e.SampleOn, &e.SampleRef, &e.Reference, &e.Reason, &e.ClearedAt); err != nil {
			return nil, err
		}
		e.At = time.UnixMilli(ts).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}
