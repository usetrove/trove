package pricing

import (
	"math"
	"testing"
)

func TestCustomRatesCost(t *testing.T) {
	c, err := NewCustomRates("vllm", "0.60", "2.40", "0.15")
	if err != nil {
		t.Fatal(err)
	}
	// 1,000,000 uncached in * 0.60 + 500,000 cache read * 0.15 + 250,000 out * 2.40
	// = 0.60 + 0.075 + 0.60 = 1.275
	got := c.Cost(Usage{UncachedInput: 1_000_000, CacheRead: 500_000, Output: 250_000})
	if got.Status != Priced || got.Total.String() != "1.275" {
		t.Errorf("got %s %s (%s)", got.Status, got.Total, got.Reason)
	}
	if v := c.Version(); len(v) != len("custom:")+8 || v[:7] != "custom:" {
		t.Errorf("version %q", v)
	}
}

func TestCustomRatesDoNotGuess(t *testing.T) {
	c, _ := NewCustomRates("vllm", "0.60", "2.40", "")
	// Cache reads with no configured rate: partial, never silently free.
	got := c.Cost(Usage{UncachedInput: 1_000_000, CacheRead: 10})
	if got.Status != Partial || got.Total.String() != "0.6" || len(got.Unpriced) != 1 {
		t.Errorf("got %+v", got)
	}
	// Cache writes have no custom rate.
	got = c.Cost(Usage{CacheWrite: map[string]int64{"5m": 5}})
	if got.Status != Unpriced {
		t.Errorf("got %+v", got)
	}
}

func TestCustomRatesVersionChangesWithRates(t *testing.T) {
	a, _ := NewCustomRates("x", "1", "2", "")
	b, _ := NewCustomRates("x", "1", "3", "")
	if a.Version() == b.Version() {
		t.Error("different rates share a version")
	}
}

func TestGPUHourUSDPerMTok(t *testing.T) {
	// $2.00/hour, 1,800 busy seconds, 3,600,000 tokens: $1.00 spent / 3.6 Mtok = 0.2777...
	got, ok := GPUHourUSDPerMTok(MustDec("2.00"), 3_600_000, 1800)
	if !ok || math.Abs(got-0.277777) > 1e-5 {
		t.Errorf("got %v %v", got, ok)
	}
	for _, c := range []struct {
		tokens int64
		secs   float64
	}{{0, 10}, {10, 0}, {-1, 5}} {
		if _, ok := GPUHourUSDPerMTok(MustDec("2"), c.tokens, c.secs); ok {
			t.Errorf("computed with tokens=%d secs=%v", c.tokens, c.secs)
		}
	}
	if _, ok := GPUHourUSDPerMTok(Dec{}, 10, 10); ok {
		t.Error("computed without an hourly cost")
	}
}
