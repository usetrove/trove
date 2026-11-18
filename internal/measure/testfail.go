package measure

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/usetrove/trove/internal/store"
)

// TestFailArm is one trial arm's test_fail_rate input: how many of its finished sessions ran an allowlisted
// test command, and how many of those ended on a failing run (the last run decides, as in the rollup).
// Measured: exit codes the hooks saw. Sessions that ran no test are not counted, so they cannot dilute the rate.
type TestFailArm struct {
	Sessions int
	Failed   int
}

// Rate is the share of sessions that ended with failing tests, 0 when none ran a test.
func (a TestFailArm) Rate() float64 {
	if a.Sessions == 0 {
		return 0
	}
	return float64(a.Failed) / float64(a.Sessions)
}

// TestFailByArm reads the test_fail_rate inputs for an optimizer's trial: sessions assigned after sinceMS,
// split by arm. A session's runs are matched on its key or a gateway split of it ("key~n"), as the hook trial
// does. A session whose last test run is at or after cutoffMS may still be running, so it is left out.
func TestFailByArm(ctx context.Context, db *store.DB, optimizer string, sinceMS, cutoffMS int64) (on, off TestFailArm, err error) {
	rows, err := db.QueryContext(ctx, `SELECT a.arm,
		(SELECT t.exit_code FROM test_runs t WHERE t.session_key = a.session_key OR t.session_key LIKE a.session_key || '~%'
			ORDER BY t.ts DESC, t.id DESC LIMIT 1),
		(SELECT MAX(t.ts) FROM test_runs t WHERE t.session_key = a.session_key OR t.session_key LIKE a.session_key || '~%')
		FROM experiment_assignments a WHERE a.optimizer = ? AND a.assigned_at > ?`, optimizer, sinceMS)
	if err != nil {
		return on, off, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var arm string
		var code, last sql.NullInt64
		if err := rows.Scan(&arm, &code, &last); err != nil {
			return on, off, err
		}
		if !code.Valid || !last.Valid || last.Int64 >= cutoffMS {
			continue
		}
		a := &off
		if arm == "on" {
			a = &on
		} else if arm != "off" {
			continue
		}
		a.Sessions++
		if code.Int64 != 0 {
			a.Failed++
		}
	}
	return on, off, rows.Err()
}

// TestFailTrips is the test_fail_rate guardrail. It trips when both arms have at least minPerArm sessions that
// ran tests and the share ending with failing tests is more than marginPts percentage points higher with the
// optimizer on than without it. The reason is plain English and labeled measured.
func TestFailTrips(on, off TestFailArm, minPerArm int, marginPts float64) (bool, string) {
	if on.Sessions < minPerArm || off.Sessions < minPerArm {
		return false, ""
	}
	gap := (on.Rate() - off.Rate()) * 100
	if gap <= marginPts {
		return false, ""
	}
	return true, fmt.Sprintf("sessions with it on ended with failing tests %.0f%% of the time (%d of %d) against %.0f%% without it (%d of %d), more than the %g-point margin (measured)",
		on.Rate()*100, on.Failed, on.Sessions, off.Rate()*100, off.Failed, off.Sessions, marginPts)
}
