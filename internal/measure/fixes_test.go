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
