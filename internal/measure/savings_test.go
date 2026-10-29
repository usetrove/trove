package measure

import (
	"context"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/pricing"
)

// Prices (per million tokens): input 2, cache read 0.2, 5m write 2.5, 1h write 4.
// Every expected value below was worked out by hand.
func TestRequestSavingsHandCalculated(t *testing.T) {
	tbl := testTable(t)
	at := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	req := func(u pricing.Usage) pricing.Request {
		return pricing.Request{Provider: pricing.Anthropic, Model: "m", Time: at, Usage: u}
	}
	tests := []struct {
		name    string
		req     pricing.Request
		removed int64
		want    string // exact USD
		ok      bool
	}{
		{"all removed tokens were cache reads", req(pricing.Usage{CacheRead: 100_000, Output: 5_000}), 50_000,
			"0.01", true}, // 50,000 x 0.2 / 1e6; output untouched
		{"mix of input, 5m write and cache read", req(pricing.Usage{UncachedInput: 10_000, CacheWrite: map[string]int64{"5m": 20_000}, CacheRead: 100_000}), 13_000,
			"0.009", true}, // split 1,000/2,000/10,000: 2,000 + 5,000 + 2,000 = 9,000 / 1e6
		{"one-hour cache write and cache read", req(pricing.Usage{CacheWrite: map[string]int64{"1h": 40_000}, CacheRead: 40_000}), 10_000,
			"0.021", true}, // split 5,000/5,000: 20,000 + 1,000 = 21,000 / 1e6
		{"plain input only", req(pricing.Usage{UncachedInput: 30_000}), 3_000,
			"0.006", true}, // 3,000 x 2 / 1e6
		{"nothing removed", req(pricing.Usage{UncachedInput: 30_000}), 0, "0", true},
		{"unknown model cannot be priced", pricing.Request{Provider: pricing.Anthropic, Model: "unknown", Time: at, Usage: pricing.Usage{UncachedInput: 1}}, 1, "0", false},
	}
	var sum SumSavings
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok, err := RequestSavings(tbl, tt.req, tt.removed)
			if err != nil {
				t.Fatal(err)
			}
			if ok != tt.ok {
				t.Fatalf("priced = %v, want %v", ok, tt.ok)
			}
			if got.Cmp(pricing.MoneyFromString(tt.want)) != 0 {
				t.Errorf("savings = %s, want exactly %s", got, tt.want)
			}
			sum.Add(got, ok)
		})
	}
	if sum.Total.Cmp(pricing.MoneyFromString("0.046")) != 0 || sum.Priced != 5 || sum.Unpriced != 1 {
		t.Errorf("sum = %s over %d priced, %d unpriced; want exactly 0.046 over 5 and 1", sum.Total, sum.Priced, sum.Unpriced)
	}
}

func TestSavingsByOptimizerSharesBytesAndLabelsEstimated(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	ins := func(session string, ts time.Time, removed int64, diff any, applied string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `INSERT INTO requests(ts, session_key, tokens_removed, cost_diff_usd, optimizations_applied)
			VALUES(?,?,?,?,?)`, ts.UnixMilli(), session, removed, diff, applied); err != nil {
			t.Fatal(err)
		}
	}
	// 400 tokens removed (300 by the first optimizer, 100 by the second), worth $0.04: shared 0.03 / 0.01.
	ins("s1", t0, 400, 0.04, `{"label":"estimated","optimizers":[
		{"name":"noise_normalizer","state":"on","tokens_removed":300},
		{"name":"duplicate_result_collapser","state":"on","tokens_removed":100},
		{"name":"output_trimmer","state":"shadow","tokens_removed":9999}]}`)
	// An unpriced request is counted, not guessed.
	ins("s2", t0.Add(time.Minute), 100, nil, `{"optimizers":[{"name":"noise_normalizer","state":"on","tokens_removed":100}]}`)

	got, err := SavingsByOptimizer(ctx, db, Window{From: t0, To: t0.Add(time.Hour)}, config.Default().Measure)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d optimizers, want 2 (shadow entries must not count): %+v", len(got), got)
	}
	by := map[string]OptimizerSavings{}
	for _, g := range got {
		by[g.Optimizer] = g
	}
	n := by["noise_normalizer"]
	if n.USD.Value != 0.03 || n.USD.N != 2 || n.USD.Unpriced != 1 || n.USD.Label != Estimated || n.Sessions != 2 || n.Tokens.Value != 400 {
		t.Errorf("noise_normalizer = %+v", n)
	}
	if d := by["duplicate_result_collapser"]; d.USD.Value != 0.01 || d.USD.Label != Estimated {
		t.Errorf("duplicate_result_collapser = %+v", d)
	}
}

func TestCorroborationNeedsBothSidesAndEnoughSessions(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	m := config.Default().Measure
	add := func(i int, on bool, promptPerReq int64) {
		key := "k" + time.Duration(i).String()
		pinned := t0.Add(time.Duration(i) * time.Hour)
		states := `{"noise_normalizer":"shadow"}`
		if on {
			states = `{"noise_normalizer":"on"}`
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO sessions(session_key, agent, started_at, request_count, total_input_uncached_tokens)
			VALUES(?, 'a', ?, 10, ?)`, key, pinned.UnixMilli(), promptPerReq*10); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO session_pins(session_key, states, bytes_per_token, pinned_at) VALUES(?,?,3.5,?)`, key, states, pinned.UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	// Only "on" sessions: nothing to compare with.
	for i := 0; i < 25; i++ {
		add(i, true, 80_000+int64(i%5)*100)
	}
	if c, err := Corroborate(ctx, db, "noise_normalizer", m); err != nil || c != nil {
		t.Fatalf("with no off sessions want nil, got %+v err=%v", c, err)
	}
	// A few off sessions: not enough.
	for i := 100; i < 105; i++ {
		add(i, false, 100_000)
	}
	c, err := Corroborate(ctx, db, "noise_normalizer", m)
	if err != nil || c == nil || c.Verdict != NotEnoughData || c.Reduced {
		t.Fatalf("5 off sessions must give not-enough-data, got %+v err=%v", c, err)
	}
	// Enough off sessions with clearly larger prompts: corroborated, labeled measured.
	for i := 105; i < 125; i++ {
		add(i, false, 100_000+int64(i%5)*100)
	}
	c, err = Corroborate(ctx, db, "noise_normalizer", m)
	if err != nil || c == nil || !c.Reduced || c.Label != Measured {
		t.Fatalf("want reduced and measured, got %+v err=%v", c, err)
	}
}
