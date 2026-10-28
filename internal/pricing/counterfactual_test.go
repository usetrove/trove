package pricing

import (
	"math/rand"
	"testing"
	"time"
)

func TestCostWithout(t *testing.T) {
	tb := mustDefault(t)
	sonnet := Request{Provider: Anthropic, Model: "claude-sonnet-5-5", Time: at("2026-10-15"),
		Usage: Usage{UncachedInput: 10_000, CacheWrite: map[string]int64{"5m": 20_000}, CacheRead: 100_000, Output: 2_000}}
	haiku := Request{Provider: Anthropic, Model: "claude-haiku-5-5", Time: at("2026-10-20"),
		Usage: Usage{UncachedInput: 1_000, CacheRead: 100_000}}

	tests := []struct {
		name    string
		req     Request
		cat     string
		n       int64
		removed int64
		savings string
	}{
		// Sonnet 5.5: input $2, 5m write $2.50, cache read $0.10, output $10.
		{"cache read saves the cache-read price", sonnet, CatCacheRead, 40_000, 40_000, "0.004"}, // 40,000x0.10
		{"uncached input saves the input price", sonnet, CatInput, 5_000, 5_000, "0.01"},         // 5,000x2
		{"cache write saves the write price", sonnet, CatCacheWrite("5m"), 4_000, 4_000, "0.01"}, // 4,000x2.5
		{"output", sonnet, CatOutput, 1_000, 1_000, "0.01"},                                      // 1,000x10
		{"removal is clamped to what was there", sonnet, CatOutput, 1_000_000, 2_000, "0.02"},    // 2,000x10
		{"removing zero changes nothing", sonnet, CatInput, 0, 0, "0"},                           //
		{"missing write TTL removes nothing", sonnet, CatCacheWrite("1h"), 500, 0, "0"},          //
		// Haiku 5.5 prompt is 101,000 (over 100,000). Removing 2,000 cache-read tokens drops it to
		// 99,000 so the whole request reprices at the lower tier:
		// before 1,000x0.50 + 100,000x0.05 = 0.0005 + 0.005 = 0.0055
		// after  1,000x0.10 +  98,000x0.01 = 0.0001 + 0.00098 = 0.00108  -> saves 0.00442
		{"removal can cross below a long-context threshold", haiku, CatCacheRead, 2_000, 2_000, "0.00442"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cf, err := tb.CostWithout(tt.req, tt.cat, tt.n)
			if err != nil {
				t.Fatal(err)
			}
			if !cf.Valid {
				t.Fatalf("counterfactual not valid: before %s after %s", cf.Before.Status, cf.After.Status)
			}
			if cf.Removed != tt.removed {
				t.Errorf("removed = %d, want %d", cf.Removed, tt.removed)
			}
			if cf.Savings.Cmp(MoneyFromString(tt.savings)) != 0 {
				t.Errorf("savings = %s, want %s", cf.Savings, tt.savings)
			}
		})
	}
}

func TestCostWithoutDoesNotMutateTheRequest(t *testing.T) {
	tb := mustDefault(t)
	req := Request{Provider: Anthropic, Model: "claude-sonnet-5-5", Time: at("2026-10-15"),
		Usage: Usage{CacheWrite: map[string]int64{"5m": 100}, CacheRead: 50}}
	if _, err := tb.CostWithout(req, CatCacheWrite("5m"), 60); err != nil {
		t.Fatal(err)
	}
	if req.Usage.CacheWrite["5m"] != 100 {
		t.Errorf("request cache writes were modified: %v", req.Usage.CacheWrite)
	}
}

func TestCostWithoutErrorsAndUnpriced(t *testing.T) {
	tb := mustDefault(t)
	req := Request{Provider: Anthropic, Model: "claude-sonnet-5-5", Time: at("2026-10-15"), Usage: Usage{Output: 10}}
	if _, err := tb.CostWithout(req, "bogus", 1); err == nil {
		t.Error("an unknown category must be an error")
	}
	if _, err := tb.CostWithout(req, CatOutput, -1); err == nil {
		t.Error("a negative removal must be an error")
	}
	if _, err := tb.CostWithout(req, CatReasoning, 1); err == nil {
		t.Error("removing reasoning separately is only valid for Google")
	}
	g := Request{Provider: Google, Model: "gemini-2.5-flash", Time: at("2026-06-01"), Usage: Usage{Output: 1_000_000, Reasoning: 400_000}}
	cf, err := tb.CostWithout(g, CatReasoning, 100_000) // 100,000 x 2.50 = 0.25
	if err != nil || !cf.Valid || cf.Savings.Cmp(MoneyFromString("0.25")) != 0 {
		t.Errorf("google reasoning removal: %+v, %v", cf.Savings, err)
	}
	req.Model = "claude-unknown"
	cf, err = tb.CostWithout(req, CatOutput, 5)
	if err != nil {
		t.Fatal(err)
	}
	if cf.Valid || cf.Savings.Sign() != 0 {
		t.Errorf("an unpriced counterfactual must not claim savings, got valid=%v savings=%s", cf.Valid, cf.Savings)
	}
}

// Property: for every price entry, with random usage, cost is never negative and
// removing tokens from any category never increases it.
func TestPropertyCostNonNegativeAndMonotone(t *testing.T) {
	tb := mustDefault(t)
	rng := rand.New(rand.NewSource(20261028))
	cats := []string{CatInput, CatCacheRead, CatOutput, CatCacheWrite("5m"), CatCacheWrite("1h"), CatCacheWrite("30m"), CatCacheWrite("12h")}
	pick := func(limit int64) int64 {
		switch rng.Intn(4) {
		case 0:
			return 0
		case 1:
			return rng.Int63n(1000)
		case 2:
			return rng.Int63n(limit)
		default:
			return 90_000 + rng.Int63n(300_000) // straddle the 100k, 200k and 272k thresholds
		}
	}
	for _, e := range tb.Entries() {
		if e.EffectiveFrom == nil {
			continue
		}
		when := e.EffectiveFrom.Add(time.Hour)
		for i := 0; i < 300; i++ {
			req := Request{Provider: e.Provider, Model: e.Model, Time: when, Batch: rng.Intn(3) == 0,
				Usage: Usage{
					UncachedInput: pick(1_000_000), CacheRead: pick(1_000_000), Output: pick(100_000),
					CacheWrite: map[string]int64{"5m": pick(300_000), "1h": pick(300_000), "30m": pick(300_000), "12h": pick(300_000)},
				}}
			if e.Provider == Google {
				req.Usage.Reasoning = pick(100_000)
			}
			switch rng.Intn(4) {
			case 0:
				for name := range e.ServiceTiers {
					req.ServiceTier, req.Batch = name, false
					break
				}
			case 1:
				if e.FlexMatchesBatch {
					req.ServiceTier = "flex"
				}
			}
			if rng.Intn(3) == 0 && len(e.Regional) > 0 {
				req.Region = e.Regional[0].Name
			}
			if rng.Intn(3) == 0 {
				req.ToolCalls = map[string]int64{"web_search": rng.Int63n(50)}
			}
			base := tb.Cost(req)
			if base.Total.Sign() < 0 {
				t.Fatalf("%s/%s: negative cost %s for %+v", e.Provider, e.Model, base.Total, req.Usage)
			}
			cat := cats[rng.Intn(len(cats))]
			if e.Provider == Google && rng.Intn(4) == 0 {
				cat = CatReasoning
			}
			cf, err := tb.CostWithout(req, cat, rng.Int63n(400_000))
			if err != nil {
				t.Fatal(err)
			}
			// "Unpriced" is the absence of a cost (for example a premium tier with no
			// long-context price), not a cost of zero, so only compare real costs.
			if cf.Before.Status == Unpriced || cf.After.Status == Unpriced {
				continue
			}
			if cf.After.Total.Cmp(cf.Before.Total) > 0 {
				t.Fatalf("%s/%s: removing %d from %s raised cost %s -> %s; usage %+v batch=%v",
					e.Provider, e.Model, cf.Removed, cat, cf.Before.Total, cf.After.Total, req.Usage, req.Batch)
			}
			if cf.After.Total.Sign() < 0 {
				t.Fatalf("%s/%s: negative cost after removal", e.Provider, e.Model)
			}
		}
	}
}
