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

func ckEvent(t *testing.T, db *store.DB, at time.Time, key, kind string, b, tok int64) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `INSERT INTO hook_events(ts, session_key, hook, kind, bytes, tokens) VALUES(?,?,?,?,?,?)`,
		at.UnixMilli(), key, config.OptimizerHookSessionCheckpoint, kind, b, tok); err != nil {
		t.Fatal(err)
	}
}

func TestCheckpointWindowsAttributesEventsToTheirWindow(t *testing.T) {
	db := openDB(t)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	// Session a: a note, a restore whose window re-reads twice and recovers at call 4, then a second boundary
	// without the restore whose window repeats a failure and never edits.
	ckEvent(t, db, at(0), "a", "checkpointed", 900, 0)
	ckEvent(t, db, at(1), "a", "restored", 1200, 363)
	ckEvent(t, db, at(2), "a", "reread_unchanged", 1, 0)
	ckEvent(t, db, at(3), "a", "reread_unchanged", 2, 0)
	ckEvent(t, db, at(4), "a", "recovered", 4, 0)
	ckEvent(t, db, at(10), "a", "would_restore", 1200, 363)
	ckEvent(t, db, at(11), "a", "repeat_failure", 3, 0)
	// Session b: the pointer and a clean window that recovers at call 2.
	ckEvent(t, db, at(0), "b", "pointed", 200, 77)
	ckEvent(t, db, at(1), "b", "recovered", 2, 0)
	// Session c: a metric event with no boundary before it is not attributed.
	ckEvent(t, db, at(0), "c", "repeat_failure", 1, 0)

	c, err := CheckpointWindowsBetween(context.Background(), db, at(0).UnixMilli(), 0)
	if err != nil {
		t.Fatal(err)
	}
	want := CheckpointWindows{Saved: 1, Restored: 1, Pointed: 1,
		With:    CheckpointArm{Windows: 2, Rereads: 2, BadWindows: 1, Recovered: 2, CallsToRecover: 6, AddedTokens: 440},
		Without: CheckpointArm{Windows: 1, RepeatFailures: 1, BadWindows: 1, AddedTokens: 363}}
	if c != want {
		t.Fatalf("got  %+v\nwant %+v", c, want)
	}
	if c.With.MeanCallsToRecover() != 3 {
		t.Errorf("mean %v", c.With.MeanCallsToRecover())
	}
	if s, err := CheckpointSummary(context.Background(), db, Window{From: at(100), To: at(200)}); err != nil || s != nil {
		t.Errorf("an empty period: %+v %v", s, err)
	}
}

// ckWindows stores n windows on each side; badWith and badWithout of them re-read an unchanged file.
func ckWindows(t *testing.T, db *store.DB, n, badWith, badWithout int) {
	t.Helper()
	for i := 0; i < 2*n; i++ {
		with := i < n
		key := fmt.Sprintf("ck%03d", i)
		at := t0.Add(time.Duration(i) * time.Hour)
		kind, bad := "would_restore", i-n < badWithout
		if with {
			kind, bad = "restored", i < badWith
		}
		ckEvent(t, db, at, key, kind, 1000, 300)
		if bad {
			ckEvent(t, db, at.Add(time.Minute), key, "reread_unchanged", 1, 0)
		}
	}
}

func TestCheckpointGuardrailTripsWhenRestoreWindowsAreWorse(t *testing.T) {
	db := openDB(t)
	ckWindows(t, db, 40, 30, 4)
	evs, err := EvaluateHooks(context.Background(), db, config.Default().Measure, t0.Add(100*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Rule != RuleCheckpointWindows || evs[0].Optimizer != config.OptimizerHookSessionCheckpoint ||
		!strings.Contains(evs[0].Reason, "75% of the 20-call windows after a restore (30 of 40)") {
		t.Fatalf("events: %+v", evs)
	}
	ov, err := Overrides(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if _, rolled := ov[config.OptimizerHookSessionCheckpoint]; !rolled {
		t.Error("the rollback was not recorded")
	}
}

func TestCheckpointGuardrailHoldsWhenRestoreHelps(t *testing.T) {
	db := openDB(t)
	ckWindows(t, db, 40, 4, 30)
	evs, err := EvaluateHooks(context.Background(), db, config.Default().Measure, t0.Add(100*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 0 {
		t.Fatalf("events: %+v", evs)
	}
}

func TestCheckpointGuardrailTripsOnFailingTestsAfterARestore(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	for i := 0; i < 80; i++ {
		key := fmt.Sprintf("ct%03d", i)
		at := t0.Add(time.Duration(i) * time.Hour)
		code := 0
		if i < 40 {
			ckEvent(t, db, at, key, "restored", 1000, 300)
			if i < 30 {
				code = 1
			}
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO test_runs(session_key, ts, cmd_hash, exit_code) VALUES(?,?,'h',?)`,
			key, at.Add(5*time.Minute).UnixMilli(), code); err != nil {
			t.Fatal(err)
		}
	}
	evs, err := EvaluateHooks(ctx, db, config.Default().Measure, t0.Add(100*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Rule != RuleCheckpointTestsAfter {
		t.Fatalf("events: %+v", evs)
	}
}
