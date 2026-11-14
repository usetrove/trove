package measure

import (
	"context"
	"testing"
	"time"
)

func TestSweepReceipts(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	idle := 30 * time.Minute

	add := func(key string, ts time.Time, status int, model string, cost any, in, cw, cr int, diff any, applied any, removed int) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `INSERT INTO requests(ts, session_key, agent, model, status_code, input_uncached_tokens, cache_write_tokens,
			cache_read_tokens, cost_usd, cost_diff_usd, optimizations_applied, tokens_removed) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			ts.UnixMilli(), key, "claude_code", model, status, in, cw, cr, cost, diff, applied, removed); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO sessions(session_key, agent, started_at, ended_at, request_count) VALUES(?,?,?,?,1)
			ON CONFLICT(session_key) DO UPDATE SET ended_at = MAX(ended_at, excluded.ended_at), request_count = request_count + 1`,
			key, "claude_code", ts.UnixMilli(), ts.UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	now := t0.Add(5 * time.Hour)

	// Finished session: three good calls (one unpriced), a failed call, two models, one saving.
	add("c-aaaaaaaaaaaaaaaaaaaaaaaa", t0, 200, "m1", 1.0, 100, 0, 900, nil, nil, 0)
	add("c-aaaaaaaaaaaaaaaaaaaaaaaa", t0.Add(time.Minute), 200, "m1", 0.5, 100, 0, 900, 0.25, `{"x":1}`, 500)
	add("c-aaaaaaaaaaaaaaaaaaaaaaaa", t0.Add(2*time.Minute), 200, "m2", nil, 100, 0, 0, nil, nil, 0)
	add("c-aaaaaaaaaaaaaaaaaaaaaaaa", t0.Add(3*time.Minute), 500, "m1", 9.0, 999, 0, 0, nil, nil, 0)
	// Still running: last request 10 minutes ago.
	add("c-bbbbbbbbbbbbbbbbbbbbbbbb", now.Add(-10*time.Minute), 200, "m1", 1.0, 1, 0, 0, nil, nil, 0)
	// Finished, but every call failed.
	add("c-cccccccccccccccccccccccc", t0.Add(time.Hour), 429, "m1", nil, 0, 0, 0, nil, nil, 0)
	// Older than the sweep horizon.
	add("c-dddddddddddddddddddddddd", now.Add(-30*24*time.Hour), 200, "m1", 1.0, 1, 0, 0, nil, nil, 0)

	got, err := SweepReceipts(ctx, db, idle, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("wrote %d receipts, want 1: %+v", len(got), got)
	}
	r := got[0]
	if r.Calls != 3 || r.Model != "m1" || r.Models != 2 || r.Agent != "claude_code" || r.Session != "aaaaaaaa" {
		t.Errorf("receipt = %+v", r)
	}
	if r.Cost.Value != 1.5 || r.Cost.Label != Estimated || r.Cost.Unpriced != 1 || r.Cost.N != 2 {
		t.Errorf("cost = %+v (the failed 9.0 call must not count)", r.Cost)
	}
	if r.CacheHit == nil || r.CacheHit.Label != Measured || r.CacheHit.Value != 100*1800.0/2100 {
		t.Errorf("cache hit = %+v", r.CacheHit)
	}
	if r.Saved.Value != 0.25 || r.Saved.Label != Estimated {
		t.Errorf("saved = %+v", r.Saved)
	}

	// Idempotent, including for the session whose calls all failed.
	if again, err := SweepReceipts(ctx, db, idle, now); err != nil || len(again) != 0 {
		t.Errorf("second sweep wrote %d receipts (err %v)", len(again), err)
	}
	// The running session finishes once its idle gap has passed.
	later, err := SweepReceipts(ctx, db, idle, now.Add(21*time.Minute))
	if err != nil || len(later) != 1 || later[0].Session != "bbbbbbbb" {
		t.Errorf("later sweep = %+v (err %v)", later, err)
	}

	list, err := ListReceipts(ctx, db, 10)
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %+v (err %v); the all-failed session must not be listed", list, err)
	}
	if list[0].Session != "bbbbbbbb" || list[1].Calls != 3 || list[1].Cost.Value != 1.5 {
		t.Errorf("list order or content wrong: %+v", list)
	}
}

// A receipt names the optimizers that were on for the session, from its pin.
func TestReceiptNamesTheOptimizersThatWereOn(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	key := "c-eeeeeeeeeeeeeeeeeeeeeeee"
	if _, err := db.ExecContext(ctx, `INSERT INTO requests(ts, session_key, agent, model, status_code, input_uncached_tokens, cost_usd) VALUES(?,?,?,?,?,?,?)`,
		t0.UnixMilli(), key, "claude_code", "m1", 200, 10, 0.5); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO sessions(session_key, agent, started_at, ended_at, request_count) VALUES(?,?,?,?,1)`,
		key, "claude_code", t0.UnixMilli(), t0.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO session_pins(session_key, states, custom_agents, bytes_per_token, pinned_at) VALUES(?,?,?,?,?)`,
		key, `{"noise_normalizer":"on","output_trimmer":"shadow","duplicate_result_collapser":"on"}`, `[]`, 3.5, t0.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	got, err := SweepReceipts(ctx, db, 30*time.Minute, t0.Add(5*time.Hour))
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %+v", err, got)
	}
	want := []string{"duplicate_result_collapser", "noise_normalizer"}
	if len(got[0].On) != 2 || got[0].On[0] != want[0] || got[0].On[1] != want[1] {
		t.Errorf("on = %v, want %v (shadow does not count)", got[0].On, want)
	}
	listed, err := ListReceipts(ctx, db, 5)
	if err != nil || len(listed) != 1 || len(listed[0].On) != 2 {
		t.Errorf("listed receipt lost the names: %v %+v", err, listed)
	}
}
