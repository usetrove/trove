package measure

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestHookShadowEstimatesCountsOnlyWouldHaveActedEventsInTheWindow(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	add := func(ts time.Time, session, hook, kind string, tokens int64) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `INSERT INTO hook_events(ts, session_key, hook, kind, bytes, tokens) VALUES(?,?,?,?,?,?)`,
			ts.UnixMilli(), session, hook, kind, tokens*4, tokens); err != nil {
			t.Fatal(err)
		}
	}
	from, to := t0, t0.Add(24*time.Hour)
	// noise_normalizer: three sessions, four would-have-rewritten events.
	add(from.Add(1*time.Hour), "s1", "noise_normalizer", "would_rewrite", 100)
	add(from.Add(2*time.Hour), "s1", "noise_normalizer", "would_rewrite", 200)
	add(from.Add(3*time.Hour), "s2", "noise_normalizer", "would_rewrite", 300)
	add(from.Add(4*time.Hour), "s3", "noise_normalizer", "would_rewrite", 400)
	// hook_repeat_read: would-have-blocked counts the same way.
	add(from.Add(5*time.Hour), "s1", "hook_repeat_read", "would_block", 1000)
	// Things that are not "would have saved tokens": real actions, advice, retries, and other kinds.
	add(from.Add(6*time.Hour), "s9", "noise_normalizer", "rewritten", 5000)
	add(from.Add(6*time.Hour), "s9", "hook_repeat_read", "blocked", 5000)
	add(from.Add(6*time.Hour), "s9", "hook_large_output", "would_advise", 5000)
	add(from.Add(6*time.Hour), "s9", "hook_repeat_read", "reread_anyway", 5000)
	// Outside the window.
	add(from.Add(-time.Minute), "s8", "noise_normalizer", "would_rewrite", 7000)
	add(to, "s8", "noise_normalizer", "would_rewrite", 7000) // the end is exclusive

	got, err := HookShadowEstimates(ctx, db, Window{From: from, To: to})
	if err != nil {
		t.Fatal(err)
	}
	nn := got["noise_normalizer"]
	if nn.Sessions != 3 || nn.Events != 4 || nn.Tokens != 1000 {
		t.Errorf("noise_normalizer = %+v, want 3 sessions, 4 events, 1000 tokens", nn)
	}
	rr := got["hook_repeat_read"]
	if rr.Sessions != 1 || rr.Events != 1 || rr.Tokens != 1000 {
		t.Errorf("hook_repeat_read = %+v", rr)
	}
	if _, ok := got["hook_large_output"]; ok {
		t.Error("advice counted as a saving")
	}
	if len(got) != 2 {
		t.Errorf("got %d optimizers, want 2: %+v", len(got), got)
	}
	if empty, err := HookShadowEstimates(ctx, db, Window{From: to.Add(time.Hour), To: to.Add(2 * time.Hour)}); err != nil || len(empty) != 0 {
		t.Errorf("an empty window gave %v, %v", empty, err)
	}
}

func TestHookSessionsCountsDistinctSessionsOfAnyKind(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	if n, err := HookSessions(ctx, db); err != nil || n != 0 {
		t.Fatalf("empty: %d %v", n, err)
	}
	for i := 0; i < 7; i++ {
		for _, kind := range []string{"would_rewrite", "blocked", "reread_anyway"} {
			if _, err := db.ExecContext(ctx, `INSERT INTO hook_events(ts, session_key, hook, kind) VALUES(?,?,?,?)`,
				t0.UnixMilli(), fmt.Sprintf("s%d", i), "hook_repeat_read", kind); err != nil {
				t.Fatal(err)
			}
		}
	}
	if n, err := HookSessions(ctx, db); err != nil || n != 7 {
		t.Errorf("got %d, %v; want 7 distinct sessions", n, err)
	}
}

func TestTrafficSeenSeparatesTheGatewayFromTheHooks(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	w := Window{From: t0, To: t0.Add(24 * time.Hour)}
	if tr, err := TrafficSeen(ctx, db, w); err != nil || tr.Gateway || tr.Hooks {
		t.Fatalf("empty database: %+v %v", tr, err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO hook_events(ts, session_key, hook, kind) VALUES(?,?,?,?)`,
		t0.Add(time.Hour).UnixMilli(), "s", "noise_normalizer", "would_rewrite"); err != nil {
		t.Fatal(err)
	}
	if tr, _ := TrafficSeen(ctx, db, w); tr.Gateway || !tr.Hooks {
		t.Errorf("hooks only: %+v", tr)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO requests(ts, session_key, agent, provider, model, status_code) VALUES(?,?,?,?,?,?)`,
		t0.Add(time.Hour).UnixMilli(), "s", "claude_code", "anthropic", "m", 200); err != nil {
		t.Fatal(err)
	}
	if tr, _ := TrafficSeen(ctx, db, w); !tr.Gateway || !tr.Hooks {
		t.Errorf("both: %+v", tr)
	}
	// Traffic outside the window does not count.
	if tr, _ := TrafficSeen(ctx, db, Window{From: t0.Add(48 * time.Hour), To: t0.Add(72 * time.Hour)}); tr.Gateway || tr.Hooks {
		t.Errorf("another window: %+v", tr)
	}
}
