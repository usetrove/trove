package measure

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/store"
)

// codeWriteSessions stores n sessions where hook_write_to_edit denied a Write (acted) and n where it did not, each
// with editsPer Edit calls of which failOn (acted) or failOff (rest) failed, and a test run 5 minutes after the
// deny that fails in testsFailOn of the acted sessions.
func codeWriteSessions(t *testing.T, db *store.DB, n, editsPer, failOn, failOff, testsFailOn int) {
	t.Helper()
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2*n; i++ {
		acted := i < n
		key := fmt.Sprintf("cw%03d", i)
		at := t0.Add(time.Duration(i) * time.Hour)
		if acted {
			exec(`INSERT INTO hook_events(ts, session_key, hook, kind) VALUES(?,?,?,?)`, at.UnixMilli(), key, config.OptimizerHookWriteToEdit, "blocked")
		}
		for e := 0; e < editsPer; e++ {
			rk := fmt.Sprintf("%s-%d", key, e)
			ts := at.Add(time.Duration(e+1) * time.Minute).UnixMilli()
			failed := (acted && e < failOn) || (!acted && e < failOff)
			exec(`INSERT INTO history_requests(request_key, source, session_key, ts) VALUES(?,?,?,?)`, rk, "claude_code", key, ts)
			exec(`INSERT INTO history_tool_calls(request_key, idx, ts, tool_name, input_bytes, error) VALUES(?,0,?,'Edit',100,?)`, rk, ts, failed)
		}
		code := 0
		if acted && i < testsFailOn {
			code = 1
		}
		exec(`INSERT INTO test_runs(session_key, ts, cmd_hash, exit_code) VALUES(?,?,'h',?)`, key, at.Add(5*time.Minute).UnixMilli(), code)
	}
}

func TestCodeWriteGuardrailsTripOnEditFailures(t *testing.T) {
	db := openDB(t)
	codeWriteSessions(t, db, 40, 5, 3, 0, 0)
	evs, err := EvaluateHooks(context.Background(), db, config.Default().Measure, t0.Add(100*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Rule != RuleCodeEditFailures || evs[0].Optimizer != config.OptimizerHookWriteToEdit ||
		!strings.Contains(evs[0].Reason, "Edit calls failed 60%") {
		t.Fatalf("events: %+v", evs)
	}
}

func TestCodeWriteGuardrailsTripOnFailingTestsRightAfter(t *testing.T) {
	db := openDB(t)
	codeWriteSessions(t, db, 40, 2, 0, 0, 30)
	evs, err := EvaluateHooks(context.Background(), db, config.Default().Measure, t0.Add(100*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Rule != RuleCodeTestsAfter || !strings.Contains(evs[0].Reason, "first test run within 15 minutes") {
		t.Fatalf("events: %+v", evs)
	}
}

func TestCodeWriteGuardrailsStayQuietWhenNothingIsWorse(t *testing.T) {
	db := openDB(t)
	codeWriteSessions(t, db, 40, 5, 1, 1, 2)
	evs, err := EvaluateHooks(context.Background(), db, config.Default().Measure, t0.Add(100*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 0 {
		t.Fatalf("no rule should trip: %+v", evs)
	}
}

func TestCodeWriteRetryRule(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	for i := 0; i < 40; i++ {
		key := fmt.Sprintf("r%03d", i)
		ts := t0.Add(time.Duration(i) * time.Minute).UnixMilli()
		if _, err := db.ExecContext(ctx, `INSERT INTO hook_events(ts, session_key, hook, kind) VALUES(?,?,?,'blocked')`, ts, key, config.OptimizerHookWriteToEdit); err != nil {
			t.Fatal(err)
		}
		if i%4 != 0 { // three in four denied Writes are sent again
			if _, err := db.ExecContext(ctx, `INSERT INTO hook_events(ts, session_key, hook, kind) VALUES(?,?,?,'overridden')`, ts+1, key, config.OptimizerHookWriteToEdit); err != nil {
				t.Fatal(err)
			}
		}
	}
	evs, err := EvaluateHooks(ctx, db, config.Default().Measure, t0.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Rule != RuleCodeWriteRetry || !strings.Contains(evs[0].Reason, "sent the same Write again after 30 of 40") {
		t.Fatalf("events: %+v", evs)
	}
}
