package measure

import (
	"context"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
)

func TestEndSessionWritesOnlyThatSessionsReceiptNow(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	now := t0.Add(time.Hour)
	add := func(key string, ts time.Time, cost float64) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `INSERT INTO requests(ts, session_key, agent, model, status_code, input_uncached_tokens, cost_usd)
			VALUES(?,?,?,?,200,100,?)`, ts.UnixMilli(), key, "claude_code", "m1", cost); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO sessions(session_key, agent, started_at, ended_at, request_count) VALUES(?,?,?,?,1)
			ON CONFLICT(session_key) DO UPDATE SET ended_at = MAX(ended_at, excluded.ended_at), request_count = request_count + 1`,
			key, "claude_code", ts.UnixMilli(), ts.UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	// Both sessions' last call was one minute ago: neither has been idle long enough for a sweep.
	add("c-aaaaaaaaaaaaaaaaaaaaaaaa", now.Add(-time.Minute), 1.0)
	add("c-aaaaaaaaaaaaaaaaaaaaaaaa~2", now.Add(-time.Minute), 2.0) // an idle-gap successor of the same session
	add("c-bbbbbbbbbbbbbbbbbbbbbbbb", now.Add(-time.Minute), 4.0)

	if got, err := SweepReceipts(ctx, db, 30*time.Minute, now); err != nil || len(got) != 0 {
		t.Fatalf("a sweep must not end a session that is still inside its idle gap: %+v %v", got, err)
	}
	got, err := EndSession(ctx, db, config.Default().Measure, "c-aaaaaaaaaaaaaaaaaaaaaaaa", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Session != "aaaaaaaa" {
		t.Fatalf("receipts = %+v, want the session and its successor", got)
	}
	var rolled int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE outcome_signals IS NOT NULL`).Scan(&rolled); err != nil || rolled != 2 {
		t.Fatalf("rolled up %d sessions (err %v), want 2", rolled, err)
	}
	// The other session is untouched, and ending twice writes nothing more.
	list, _ := ListReceipts(ctx, db, 10)
	for _, r := range list {
		if r.Cost.Value == 4.0 {
			t.Fatalf("the other session got a receipt: %+v", r)
		}
	}
	if again, err := EndSession(ctx, db, config.Default().Measure, "c-aaaaaaaaaaaaaaaaaaaaaaaa", now); err != nil || len(again) != 0 {
		t.Fatalf("second EndSession = %+v %v", again, err)
	}
	// A session the gateway never saw, or an empty key, is not an error.
	if got, err := EndSession(ctx, db, config.Default().Measure, "c-unknown", now); err != nil || len(got) != 0 {
		t.Fatalf("unknown session = %+v %v", got, err)
	}
	if got, err := EndSession(ctx, db, config.Default().Measure, "", now); err != nil || got != nil {
		t.Fatalf("empty key = %+v %v", got, err)
	}
}

func hookEvent(t *testing.T, ctx context.Context, execer func(string, ...any) error, ts time.Time, session, hook, kind string, bytes, tokens int) {
	t.Helper()
	if err := execer(`INSERT INTO hook_events(ts, session_key, hook, kind, bytes, tokens) VALUES(?,?,?,?,?,?)`, ts.UnixMilli(), session, hook, kind, bytes, tokens); err != nil {
		t.Fatal(err)
	}
}

func TestHookTotalsAndGuardrail(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	now := t0.Add(24 * time.Hour)
	name := config.OptimizerHookRepeatRead
	ex := func(q string, a ...any) error { _, err := db.ExecContext(ctx, q, a...); return err }
	m := config.Default().Measure // MinSessions 20, RefetchRatePercent 25

	// 19 blocks, 5 retried: under the minimum sample, so nothing happens however bad the rate looks.
	for i := 0; i < 19; i++ {
		hookEvent(t, ctx, ex, now, "c-s1", name, "blocked", 7000, 2000)
	}
	for i := 0; i < 5; i++ {
		hookEvent(t, ctx, ex, now, "c-s1", name, "reread_anyway", 7000, 2000)
	}
	if ev, err := EvaluateHooks(ctx, db, m, now); err != nil || len(ev) != 0 {
		t.Fatalf("19 blocked reads is too few to judge: %+v %v", ev, err)
	}

	stats, err := HookTotals(ctx, db, now.Add(-time.Hour))
	if err != nil || len(stats) != 1 {
		t.Fatalf("%+v %v", stats, err)
	}
	s := stats[0]
	if s.Blocked != 19 || s.RereadAnyway != 5 || s.Sessions != 1 {
		t.Fatalf("stats = %+v", s)
	}
	if s.TokensSaved.Label != Estimated || s.TokensSaved.Value != float64(14*2000) {
		t.Fatalf("a retried block saves nothing: tokens saved = %+v, want %d estimated", s.TokensSaved, 14*2000)
	}
	if s.RereadRate == nil || s.RereadRate.Label != Measured || int(s.RereadRate.Value) != 26 {
		t.Fatalf("reread rate = %+v", s.RereadRate)
	}

	// One more block reaches the sample: 5/20 = 25%, which is the limit.
	hookEvent(t, ctx, ex, now, "c-s2", name, "blocked", 7000, 2000)
	ev, err := EvaluateHooks(ctx, db, m, now)
	if err != nil || len(ev) != 1 || ev[0].Rule != RuleHookReread || ev[0].SampleOn != 20 {
		t.Fatalf("expected one rollback: %+v %v", ev, err)
	}
	over, err := Overrides(ctx, db)
	if err != nil || over[name] == "" {
		t.Fatalf("the hook should now be rolled back: %v %v", over, err)
	}
	if again, err := EvaluateHooks(ctx, db, m, now); err != nil || len(again) != 0 {
		t.Fatalf("a rolled-back hook is not rolled back twice: %+v %v", again, err)
	}

	// After the user lifts the rollback, old events no longer count against it.
	if err := ClearOverride(ctx, db, name, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if ev, err := EvaluateHooks(ctx, db, m, now.Add(2*time.Minute)); err != nil || len(ev) != 0 {
		t.Fatalf("events from before the override was cleared must not trip it again: %+v %v", ev, err)
	}
}

func TestHookGuardrailLeavesAHealthyHookAlone(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	now := t0.Add(24 * time.Hour)
	ex := func(q string, a ...any) error { _, err := db.ExecContext(ctx, q, a...); return err }
	for i := 0; i < 40; i++ {
		hookEvent(t, ctx, ex, now, "c-s1", config.OptimizerHookRepeatRead, "blocked", 1000, 300)
	}
	hookEvent(t, ctx, ex, now, "c-s1", config.OptimizerHookRepeatRead, "reread_anyway", 1000, 300)
	if ev, err := EvaluateHooks(ctx, db, config.Default().Measure, now); err != nil || len(ev) != 0 {
		t.Fatalf("2.5%% retried is fine: %+v %v", ev, err)
	}
}
