package measure

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/store"
)

// Guardrails for the code-write nudges (hook_write_to_edit, B26; hook_edit_batch, B27). Beside the retry rule in
// hookRules (the agent sent a denied Write again), two rules look for harm the hooks cannot see themselves:
//
//   - Edit failures: the share of Edit and MultiEdit calls that failed (history_tool_calls.error) in sessions where
//     the nudge acted, against sessions where it did not. A Write turned into Edits that do not apply is the
//     clearest sign the nudge hurt.
//   - Failing tests right after: of the nudges followed by an allowlisted test run in the same session within
//     CodeTestsAfterWindow, the share whose first such run failed, against the failing share of test runs in
//     sessions where it did not act. Partial edits that leave a file broken show up here.
//
// Both trip when the rate with the nudge is more than CodeWriteMarginPts points above the other sessions', beyond
// chance, by the same sequential test the other hook rules use, and set the optimizer back to shadow. Counts only:
// no path, code or test output is read.

// Guardrail rules for the code-write nudges, recorded in guardrail_events.
const (
	RuleCodeWriteRetry   = "code_write_retry"
	RuleCodeEditFailures = "code_edit_failures"
	RuleCodeTestsAfter   = "code_tests_after"
)

// Placeholders, not set by a benchmark; the docs and the rollback text say so.
const (
	// CodeWriteMarginPts is how many percentage points above the other sessions' rate the rate with the nudge may
	// be before a rule trips.
	CodeWriteMarginPts = 10
	// CodeTestsAfterWindow is how soon after a nudge a test run counts as "right after".
	CodeTestsAfterWindow = 15 * time.Minute
)

// codeWriteActs maps each code-write nudge to the hook_events kind that means it acted.
var codeWriteActs = []struct{ optimizer, kind string }{
	{config.OptimizerHookWriteToEdit, "blocked"},
	{config.OptimizerHookEditBatch, "advised"},
}

// CodeRate is one side of a code-write rule: bad events out of n (measured).
type CodeRate struct{ Bad, N int }

// Rate is Bad/N, 0 when N is 0.
func (c CodeRate) Rate() float64 {
	if c.N == 0 {
		return 0
	}
	return float64(c.Bad) / float64(c.N)
}

// EditFailuresByNudge counts Edit and MultiEdit calls since sinceMS, and the failed ones, in sessions where the
// optimizer acted (kind) and in the rest.
func EditFailuresByNudge(ctx context.Context, db *store.DB, optimizer, kind string, sinceMS int64) (on, off CodeRate, err error) {
	rows, err := db.QueryContext(ctx, `SELECT r.session_key IN (SELECT session_key FROM hook_events WHERE hook = ? AND kind = ? AND ts >= ?),
			COUNT(*), COALESCE(SUM(t.error <> 0), 0)
		FROM history_tool_calls t JOIN history_requests r ON r.request_key = t.request_key
		WHERE t.tool_name IN ('Edit', 'MultiEdit') AND t.ts >= ?
		GROUP BY 1`, optimizer, kind, sinceMS, sinceMS)
	if err != nil {
		return on, off, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var acted bool
		var c CodeRate
		if err := rows.Scan(&acted, &c.N, &c.Bad); err != nil {
			return on, off, err
		}
		if acted {
			on = c
		} else {
			off = c
		}
	}
	return on, off, rows.Err()
}

// TestsAfterNudge counts nudges since sinceMS that an allowlisted test run followed within CodeTestsAfterWindow,
// and those whose first such run failed; and, for comparison, test runs and failing ones in sessions where the
// optimizer did not act.
func TestsAfterNudge(ctx context.Context, db *store.DB, optimizer, kind string, sinceMS int64) (on, off CodeRate, err error) {
	rows, err := db.QueryContext(ctx, `SELECT (SELECT t.exit_code FROM test_runs t WHERE t.session_key = e.session_key
			AND t.ts > e.ts AND t.ts <= e.ts + ? ORDER BY t.ts, t.id LIMIT 1)
		FROM hook_events e WHERE e.hook = ? AND e.kind = ? AND e.ts >= ?`, CodeTestsAfterWindow.Milliseconds(), optimizer, kind, sinceMS)
	if err != nil {
		return on, off, err
	}
	for rows.Next() {
		var code sql.NullInt64
		if err := rows.Scan(&code); err != nil {
			_ = rows.Close()
			return on, off, err
		}
		if !code.Valid {
			continue
		}
		on.N++
		if code.Int64 != 0 {
			on.Bad++
		}
	}
	if err := rows.Close(); err != nil {
		return on, off, err
	}
	if err := rows.Err(); err != nil {
		return on, off, err
	}
	err = db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(exit_code <> 0), 0) FROM test_runs
		WHERE ts >= ? AND session_key NOT IN (SELECT session_key FROM hook_events WHERE hook = ? AND kind = ? AND ts >= ?)`,
		sinceMS, optimizer, kind, sinceMS).Scan(&off.N, &off.Bad)
	return on, off, err
}

// codeRuleTrips is the shared test: enough events with the nudge, and its rate above the other sessions' by more
// than the margin at every look.
func codeRuleTrips(on, off CodeRate, minN int) (bool, float64) {
	limit := off.Rate() + CodeWriteMarginPts/100.0
	if on.N < minN || on.N == 0 || limit >= 1 {
		return false, limit
	}
	return seqRateExceeds(on.Bad, on.N, limit), limit
}

// evaluateCodeWrites runs the two rules for each code-write nudge not already rolled back, recording a rollback for
// each that trips. skip lists optimizers already rolled back.
func evaluateCodeWrites(ctx context.Context, db *store.DB, m config.Measure, now time.Time, skip map[string]bool) ([]Event, error) {
	var events []Event
	for _, a := range codeWriteActs {
		if skip[a.optimizer] {
			continue
		}
		cleared, err := lastCleared(ctx, db, a.optimizer)
		if err != nil {
			return events, err
		}
		since := now.Add(-hookWindow).UnixMilli()
		if cleared > since {
			since = cleared
		}
		type rule struct {
			name string
			read func(context.Context, *store.DB, string, string, int64) (CodeRate, CodeRate, error)
			what string
		}
		for _, r := range []rule{
			{RuleCodeEditFailures, EditFailuresByNudge, "Edit calls failed %.0f%% of the time (%d of %d) in sessions where it acted, against %.0f%% (%d of %d) in the rest"},
			{RuleCodeTestsAfter, TestsAfterNudge, "the first test run within 15 minutes after it acted failed %.0f%% of the time (%d of %d), against %.0f%% of test runs (%d of %d) in sessions where it did not act"},
		} {
			on, off, err := r.read(ctx, db, a.optimizer, a.kind, since)
			if err != nil {
				return events, err
			}
			trips, limit := codeRuleTrips(on, off, m.MinSessions)
			if !trips {
				continue
			}
			what := fmt.Sprintf(r.what, on.Rate()*100, on.Bad, on.N, off.Rate()*100, off.Bad, off.N)
			ev := Event{At: now.UTC(), Optimizer: a.optimizer, Rule: r.name, Observed: on.Rate(), Threshold: limit,
				SampleOn: on.N, SampleRef: off.N, Reference: RefOffSessions,
				Reason: fmt.Sprintf("Trove set %s back to shadow because %s, more than the %d-point margin (a placeholder) beyond chance. This is measured from history and test results; re-enable it deliberately if you disagree.",
					a.optimizer, what, CodeWriteMarginPts)}
			if err := recordRollback(ctx, db, ev); err != nil {
				return events, err
			}
			events = append(events, ev)
			break // one rollback per optimizer is enough
		}
	}
	return events, nil
}
