package measure

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
)

// seedSessions stores n finished sessions, one hour apart, each sending
// about perReq prompt tokens per request.
func seedSessions(t *testing.T, n int, start time.Time, perReq int64, tag string, db func(q string, a ...any)) {
	t.Helper()
	for i := 0; i < n; i++ {
		s := start.Add(time.Duration(i) * time.Hour)
		// A little variation so the comparison has a spread.
		tokens := perReq + int64(i%3)*10
		db(`INSERT INTO sessions(session_key, agent, started_at, ended_at, request_count, total_cache_read_tokens)
			VALUES(?, 'claude_code', ?, ?, 10, ?)`, fmt.Sprintf("%s-%d", tag, i), s.UnixMilli(), s.Add(10*time.Minute).UnixMilli(), tokens*10)
	}
}

func TestFixEffectsMeasuredOnlyWithEnoughSessions(t *testing.T) {
	db := openDB(t)
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := db.Exec(q, a...); err != nil {
			t.Fatal(err)
		}
	}
	applied := t0.Add(48 * time.Hour)
	exec(`INSERT INTO fixes(change_id, fix_id, kind, agent, path_hash, applied_at, est_tokens, est_usd, label)
		VALUES('c1', 'ctx-1', 'context_slim', 'claude_code', 'h', ?, 900, 0.02, 'estimated')`, applied.UnixMilli())
	m := config.Default().Measure // MinSessions 20

	// Too few sessions on both sides: still an estimate.
	seedSessions(t, 3, t0, 5000, "b", exec)
	seedSessions(t, 3, applied.Add(time.Hour), 4000, "a", exec)
	fx, err := FixEffects(context.Background(), db, m)
	if err != nil || len(fx) != 1 {
		t.Fatalf("%v %+v", err, fx)
	}
	if fx[0].Label != Estimated || fx[0].Enough || fx[0].Text == "" || fx[0].EstTokens != 900 {
		t.Fatalf("few sessions must stay an estimate: %+v", fx[0])
	}

	// Plenty of sessions: measured, and the drop is detected.
	seedSessions(t, 25, t0.Add(-30*time.Hour), 5000, "b2", exec)
	seedSessions(t, 25, applied.Add(10*time.Hour), 4000, "a2", exec)
	fx, err = FixEffects(context.Background(), db, m)
	if err != nil {
		t.Fatal(err)
	}
	e := fx[0]
	if e.Label != Measured || !e.Enough || !e.Reduced || e.Percent < 15 || e.Percent > 25 {
		t.Fatalf("want a measured ~20%% reduction, got %+v", e)
	}
	if e.NBefore < 20 || e.NAfter < 20 {
		t.Fatalf("sample sizes: %+v", e)
	}
}

func TestFixEffectsIgnoresRevertedAndMidSessionSessions(t *testing.T) {
	db := openDB(t)
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := db.Exec(q, a...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO fixes(change_id, fix_id, kind, agent, path_hash, applied_at, reverted_at, est_tokens, label)
		VALUES('c1', 'x', 'mcp_prune', 'claude_code', 'h', ?, ?, 10, 'estimated')`, t0.UnixMilli(), t0.Add(time.Hour).UnixMilli())
	fx, err := FixEffects(context.Background(), db, config.Default().Measure)
	if err != nil || len(fx) != 0 {
		t.Fatalf("a reverted fix must not be reported: %v %+v", err, fx)
	}
}

// seedTTLSessions stores n Claude Code history sessions, one hour apart, each
// writing w5 and w1 cache tokens per request over 5 requests.
func seedTTLSessions(t *testing.T, n int, start time.Time, side int, w5, w1 int64, tag string, db func(q string, a ...any)) {
	t.Helper()
	for i := 0; i < n; i++ {
		s := start.Add(time.Duration(i) * time.Hour)
		for r := 0; r < 5; r++ {
			db(`INSERT INTO history_requests(request_key, source, session_key, ts, model, cache_write_5m, cache_write_1h, sidechain)
				VALUES(?, 'claude_code', ?, ?, 'm', ?, ?, ?)`, fmt.Sprintf("%s-%d-%d", tag, i, r), fmt.Sprintf("%s-%d", tag, i),
				s.Add(time.Duration(r)*time.Minute).UnixMilli(), w5, w1, side)
		}
	}
}

// A cache lifetime fix is judged on where the cache writes went: the history
// must show them moving to the lifetime the fix set.
func TestFixEffectsCacheTTLShowsWritesMovingAsPredicted(t *testing.T) {
	for _, tc := range []struct {
		name               string
		kind               string
		side               int
		before5, before1   int64
		after5, after1     int64
		wantMoved          bool
		wantBefore, wantAt float64
	}{
		{"main 1h to 5m", "cache_ttl_main", 0, 0, 1000, 1000, 0, true, 1, 0},
		{"subagents 5m to 1h", "cache_ttl_subagents", 1, 1000, 0, 0, 1000, true, 0, 1},
		{"setting ignored", "cache_ttl_main", 0, 0, 1000, 0, 1000, false, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openDB(t)
			exec := func(q string, a ...any) {
				t.Helper()
				if _, err := db.Exec(q, a...); err != nil {
					t.Fatal(err)
				}
			}
			applied := t0.Add(48 * time.Hour)
			exec(`INSERT INTO fixes(change_id, fix_id, kind, agent, path_hash, applied_at, est_tokens, label)
				VALUES('c1', 'ttl-1', ?, 'claude_code', 'h', ?, 0, 'estimated')`, tc.kind, applied.UnixMilli())
			m := config.Default().Measure
			seedTTLSessions(t, 25, t0.Add(-30*time.Hour), tc.side, tc.before5, tc.before1, "b", exec)
			// The other bucket keeps its own lifetime and must not be mixed in.
			seedTTLSessions(t, 25, t0.Add(-30*time.Hour), 1-tc.side, 500, 500, "o", exec)
			seedTTLSessions(t, 25, applied.Add(time.Hour), tc.side, tc.after5, tc.after1, "a", exec)
			fx, err := FixEffects(context.Background(), db, m)
			if err != nil || len(fx) != 1 {
				t.Fatalf("%v %+v", err, fx)
			}
			e := fx[0]
			if e.TTL == nil {
				t.Fatalf("no lifetime split: %+v", e)
			}
			if e.TTL.Before1hShare != tc.wantBefore || e.TTL.After1hShare != tc.wantAt || e.TTL.Moved != tc.wantMoved {
				t.Fatalf("got %+v", *e.TTL)
			}
			if e.Label != Measured || !e.Enough || e.Text == "" {
				t.Fatalf("want a measured result with text: %+v", e)
			}
		})
	}
}

func TestFixEffectsCacheTTLNeedsSessionsAfter(t *testing.T) {
	db := openDB(t)
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := db.Exec(q, a...); err != nil {
			t.Fatal(err)
		}
	}
	applied := t0.Add(48 * time.Hour)
	exec(`INSERT INTO fixes(change_id, fix_id, kind, agent, path_hash, applied_at, est_tokens, label)
		VALUES('c1', 'ttl-1', 'cache_ttl_main', 'claude_code', 'h', ?, 0, 'estimated')`, applied.UnixMilli())
	seedTTLSessions(t, 25, t0.Add(-30*time.Hour), 0, 0, 1000, "b", exec)
	seedTTLSessions(t, 2, applied.Add(time.Hour), 0, 1000, 0, "a", exec)
	fx, err := FixEffects(context.Background(), db, config.Default().Measure)
	if err != nil || len(fx) != 1 {
		t.Fatalf("%v %+v", err, fx)
	}
	if fx[0].Label != Estimated || fx[0].Enough || fx[0].TTL == nil || fx[0].TTL.Moved {
		t.Fatalf("two sessions after must not count as measured: %+v", fx[0])
	}
}
