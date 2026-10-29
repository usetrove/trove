package measure

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/store"
)

func addRequest(t *testing.T, db *store.DB, session string, ts time.Time, cols string, args ...any) {
	t.Helper()
	// cols is a fixed list from the tests below, never user input.
	q := `INSERT INTO requests(ts, session_key, status_code, ` + cols + `) VALUES(?, ?, 200` + repeatQ(len(args)) + `)`
	if _, err := db.ExecContext(context.Background(), q, append([]any{ts.UnixMilli(), session}, args...)...); err != nil {
		t.Fatal(err)
	}
}

func repeatQ(n int) string {
	s := ""
	for i := 0; i < n; i++ {
		s += ",?"
	}
	return s
}

func TestRollupSessionsWritesCountsOnlyAndMarksAbandonment(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	applied := `{"label":"estimated","optimizers":[{"name":"noise_normalizer","state":"on","tokens_removed":50}]}`
	sig := string(Signals{Messages: 9, ToolCalls: 6, DistinctCalls: 4, ToolErrors: 1, Collapsed: 2, Refetches: 1}.JSON())

	// Session A: an optimizer acted on request 2, then the session ended within a minute.
	for i, ts := range []time.Time{t0, t0.Add(time.Minute), t0.Add(90 * time.Second)} {
		ap := any(nil)
		if i >= 1 {
			ap = applied
		}
		addRequest(t, db, "A", ts, "signals, optimizations_applied, input_uncached_tokens", sig, ap, 100)
		if _, err := db.ExecContext(ctx, `INSERT INTO sessions(session_key, agent, started_at, ended_at, request_count, total_input_uncached_tokens)
			VALUES('A','x',?,?,1,100) ON CONFLICT(session_key) DO UPDATE SET ended_at = excluded.ended_at, request_count = request_count + 1`, t0.UnixMilli(), ts.UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO session_pins(session_key, states, bytes_per_token, pinned_at) VALUES('A', '{"noise_normalizer":"on","output_trimmer":"shadow"}', 3.5, ?)`, t0.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	// Session B: long, optimizer acted early, kept going: not abandoned.
	for i := 0; i < 6; i++ {
		ts := t0.Add(time.Duration(i) * 10 * time.Minute)
		addRequest(t, db, "B", ts, "signals, optimizations_applied", sig, applied)
		if _, err := db.ExecContext(ctx, `INSERT INTO sessions(session_key, agent, started_at, ended_at, request_count)
			VALUES('B','x',?,?,1) ON CONFLICT(session_key) DO UPDATE SET ended_at = excluded.ended_at, request_count = request_count + 1`, t0.UnixMilli(), ts.UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	// Session C: still running (ended_at is after the cutoff), so it is left alone.
	if _, err := db.ExecContext(ctx, `INSERT INTO sessions(session_key, agent, started_at, ended_at, request_count) VALUES('C','x',?,?,1)`, t0.UnixMilli(), t0.Add(3*time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}

	now := t0.Add(3*time.Hour + 5*time.Minute)
	n, err := RollupSessions(ctx, db, config.Default().Measure, now, 30*time.Minute)
	if err != nil || n != 2 {
		t.Fatalf("rolled up %d sessions, err=%v; want 2", n, err)
	}
	get := func(key string) SessionSignals {
		var raw *string
		if err := db.QueryRowContext(ctx, `SELECT outcome_signals FROM sessions WHERE session_key = ?`, key).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if raw == nil {
			return SessionSignals{Turns: -1}
		}
		var s SessionSignals
		if err := json.Unmarshal([]byte(*raw), &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	a := get("A")
	if !a.Abandoned || !a.HasTools || a.Repeats != 2 || a.ToolErrors != 1 || a.Refetches != 1 || a.Collapsed != 2 ||
		len(a.On) != 1 || a.On[0] != "noise_normalizer" || len(a.Acted) != 1 || a.Commits != nil {
		t.Errorf("session A = %+v", a)
	}
	if b := get("B"); b.Abandoned || len(b.Acted) != 1 {
		t.Errorf("session B = %+v", b)
	}
	if c := get("C"); c.Turns != -1 {
		t.Errorf("a running session must not be rolled up: %+v", c)
	}
	if n, _ := RollupSessions(ctx, db, config.Default().Measure, now, 30*time.Minute); n != 0 {
		t.Errorf("second run rolled up %d sessions; it must be idempotent", n)
	}
}

func TestTotalsByAndWasteAreLabeled(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	w := Window{From: t0, To: t0.AddDate(0, 0, 3)}
	addRequest(t, db, "s", t0, "agent, model, input_uncached_tokens, output_tokens, cost_usd, findings", "claude_code", "m", 1000, 100, 0.5,
		`[{"detector":"duplicate_tool_results","tokens":400,"usd":0.1,"label":"estimated"}]`)
	addRequest(t, db, "s", t0.Add(time.Hour), "agent, model, input_uncached_tokens, output_tokens, cost_usd, findings", "claude_code", "m", 2000, 200, 0.25,
		`[{"detector":"duplicate_tool_results","tokens":600,"usd":null,"label":"estimated"}]`)
	addRequest(t, db, "s", t0.AddDate(0, 0, 1), "agent, model, input_uncached_tokens, output_tokens", "codex", "n", 10, 1) // unpriced

	byModel, err := TotalsBy(ctx, db, w, ByModel)
	if err != nil || len(byModel) != 2 {
		t.Fatalf("by model: %+v err=%v", byModel, err)
	}
	m := byModel[0] // sorted by key: "m" then "n"
	if m.Key != "m" || m.Requests != 2 || m.InputTokens != 3000 || m.CostUSD.Value != 0.75 || m.CostUSD.Label != Estimated || m.CostUSD.N != 2 {
		t.Errorf("model m = %+v", m)
	}
	if n := byModel[1]; n.CostUSD.Unpriced != 1 || n.CostUSD.N != 0 {
		t.Errorf("model n should count its unpriced request: %+v", n)
	}
	byDay, _ := TotalsBy(ctx, db, w, ByDay)
	if len(byDay) != 2 || byDay[0].Key != "2026-10-01" || byDay[0].Requests != 2 {
		t.Errorf("by day = %+v", byDay)
	}
	if _, err := TotalsBy(ctx, db, w, "path; DROP TABLE requests"); err == nil {
		t.Error("an unknown grouping must be refused")
	}

	waste, err := WasteByCategory(ctx, db, w)
	if err != nil || len(waste) != 1 {
		t.Fatalf("waste = %+v err=%v", waste, err)
	}
	if x := waste[0]; x.Tokens.Value != 1000 || x.Tokens.Label != Estimated || x.USD.Value != 0.1 || x.USD.Unpriced != 1 {
		t.Errorf("waste = %+v", x)
	}
}

func TestOffersWaitForBaselineAndOnlyForOptimizersThatAreOff(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	cfg := config.Default()
	w := Window{From: t0, To: t0.AddDate(0, 0, 30)}
	for i := 0; i < 12; i++ { // 12 sessions each with an estimate worth $0.25 for two optimizers
		addRequest(t, db, "s"+string(rune('a'+i)), t0.Add(time.Duration(i)*time.Hour), "shadow_savings",
			`{"label":"estimated","optimizers":{"noise_normalizer":{"tokens":1000,"usd":0.25},"duplicate_result_collapser":{"tokens":500,"usd":0.25}}}`)
		if _, err := db.ExecContext(ctx, `INSERT INTO sessions(session_key, agent, started_at) VALUES(?, 'a', ?)`, "s"+string(rune('a'+i)), t0.UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	// Baseline not finished: no offer, even though the numbers qualify.
	if got, err := Offers(ctx, db, w, cfg, DefaultOfferRules, t0.Add(time.Hour)); err != nil || len(got) != 0 {
		t.Fatalf("offers during baseline = %+v err=%v", got, err)
	}
	cfg.Measure.BaselineSessions = 5
	after := t0.AddDate(0, 0, 8)
	cfg.Optimizers = map[string]string{"duplicate_result_collapser": "on"}
	got, err := Offers(ctx, db, w, cfg, DefaultOfferRules, after)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Optimizer != "noise_normalizer" {
		t.Fatalf("offers = %+v; want only noise_normalizer (the other is already on)", got)
	}
	o := got[0]
	if o.Enable != `noise_normalizer = "on"` || o.Estimate.USD.Label != Estimated || o.Estimate.USD.Value != 3 || o.Estimate.Sessions != 12 {
		t.Errorf("offer = %+v", o)
	}
	// Nothing was switched on by offering.
	if r, _ := Overrides(ctx, db); len(r) != 0 {
		t.Errorf("offering changed state: %v", r)
	}
	// Below the dollar threshold: no offer.
	if got, _ := Offers(ctx, db, w, cfg, OfferRules{MinSessions: 10, MinUSD: 100}, after); len(got) != 0 {
		t.Errorf("offers under threshold: %+v", got)
	}
}
