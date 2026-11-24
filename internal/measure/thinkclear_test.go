package measure

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/measure/limitfit"
	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/store"
)

func clearRows(t *testing.T, db *store.DB, n int, saved, lost int64) {
	t.Helper()
	for i := 0; i < n; i++ {
		applied, _ := json.Marshal([]optimize.Applied{
			{Name: "noise_normalizer", State: "on"},
			{Name: optimize.OptimizerThinkingClear, State: "shadow", Clear: &optimize.ClearEstimate{Model: "claude-opus-5-5", ClearedTurns: 3, Saved: saved, Lost: lost, Label: "estimated"}},
		})
		if _, err := db.ExecContext(context.Background(), `INSERT INTO requests(ts, session_key, provider, model, status_code, optimizations_applied) VALUES(?,?,?,?,?,?)`,
			t0.Add(time.Duration(i)*time.Minute).UnixMilli(), fmt.Sprintf("s%d", i%4), "anthropic", "claude-opus-5-5", 200, string(applied)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestThinkingClearShadowWeighsBothSides(t *testing.T) {
	cases := []struct {
		name        string
		n           int
		saved, lost int64
		positive    bool
	}{
		// Cache reads cost a tenth of 5-minute writes' 1.25x of input, so a write costs about 11.5 reads more.
		{"mostly saved", 30, 50_000, 1_000, true},
		{"mostly rewritten", 30, 5_000, 40_000, false},
		{"too few requests", 5, 50_000, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := openDB(t)
			clearRows(t, db, c.n, c.saved, c.lost)
			v, err := ThinkingClearShadow(context.Background(), db, t.TempDir(), t0, t0.Add(24*time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			if v.Requests != c.n || v.Sessions != min(c.n, 4) || v.NetPositive != c.positive || v.Unit != "USD" || v.Label != Estimated {
				t.Fatalf("%+v", v)
			}
			if v.Saved != int64(c.n)*c.saved || v.Lost != int64(c.n)*c.lost {
				t.Errorf("totals %+v", v)
			}
			if !strings.Contains(v.Text(), "(estimated, weighed by API list prices)") {
				t.Errorf("text: %s", v.Text())
			}
		})
	}
}

func TestThinkingClearShadowWithNothing(t *testing.T) {
	db := openDB(t)
	v, err := ThinkingClearShadow(context.Background(), db, t.TempDir(), t0, t0.Add(time.Hour))
	if err != nil || v.Requests != 0 || v.NetPositive {
		t.Fatalf("%+v %v", v, err)
	}
}

// With a plan-limit fit the sides are weighed with the user's own weights per token type. A meter that charges
// cache reads nearly as much as cache writes makes the same estimate that loses money at API prices pay off.
func TestThinkingClearShadowUsesTheLimitFitWeights(t *testing.T) {
	db := openDB(t)
	clearRows(t, db, 30, 5_000, 40_000)
	dir := t.TempDir()
	w := func(typ string, v float64) limitfit.Weight {
		return limitfit.Weight{Type: typ, Range: limitfit.Range{Point: v, Lo: v, Hi: v}}
	}
	weights := []limitfit.Weight{w("input", 1), w("output", 5), w("cache_read", 1), w("cache_write_5m", 1.1), w("cache_write_1h", 2)}
	if err := limitfit.WriteCache(dir, limitfit.Result{
		FiveHour: limitfit.MeterFit{Meter: limitfit.FiveHour, Tier: limitfit.TierFit, Weights: weights},
		Week:     limitfit.MeterFit{Meter: limitfit.SevenDay, Tier: limitfit.TierFit, Weights: weights},
	}); err != nil {
		t.Fatal(err)
	}
	v, err := ThinkingClearShadow(context.Background(), db, dir, t0, t0.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if v.Unit != "% of weekly limit" || !strings.Contains(v.Basis, "plan-limit fit") || !v.NetPositive {
		t.Fatalf("%+v", v)
	}
}
