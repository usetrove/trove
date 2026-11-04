package gateway

import (
	"math"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/gateway/usage"
	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/store"
)

// Regression: requests that wrote to the prompt cache used to come back unpriced
// (cost_usd NULL) because the per-TTL split in usage.cache_creation was ignored.
// The body below is the usage shape recorded from a real Claude Code request on
// 2026-11-03; the expected cost is worked out by hand from pricing.json for
// claude-haiku-5-5 ($ per million tokens: input 0.1, output 0.5, cache read 0.01,
// 5-minute cache write 0.125):
//
//	2*0.1 + 438*0.125 + 1639*0.01 + 232*0.5 = 187.34  ->  $0.00018734
func TestPriceCoversCacheWritesWithTTLSplit(t *testing.T) {
	tbl, err := pricing.Default()
	if err != nil {
		t.Fatal(err)
	}
	body := `{"type":"message","model":"claude-haiku-5-5","usage":{"input_tokens":2,"output_tokens":232,` +
		`"cache_read_input_tokens":1639,"cache_creation_input_tokens":438,` +
		`"cache_creation":{"ephemeral_5m_input_tokens":438,"ephemeral_1h_input_tokens":0},"service_tier":"standard"}}`
	x := usage.New(usage.Anthropic, "/v1/messages", false)
	x.Write([]byte(body))
	res := x.Finish(true)

	s := &Server{opts: Options{Pricing: tbl}}
	row := &store.Request{Model: "claude-haiku-5-5"}
	s.price(row, &flight{provider: usage.Anthropic, rt: &route{priced: true}, start: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)}, res)

	if row.CostUSD == nil {
		t.Fatalf("request with a cache write was left unpriced (pricing version %q)", row.PricingVersion)
	}
	if want := 0.00018734; math.Abs(*row.CostUSD-want) > 1e-9 {
		t.Fatalf("cost = %.10f, want %.10f", *row.CostUSD, want)
	}
}

// A cache write with no usable split must still be left unpriced, never guessed.
func TestPriceLeavesUnsplitCacheWritesUnpriced(t *testing.T) {
	tbl, err := pricing.Default()
	if err != nil {
		t.Fatal(err)
	}
	body := `{"type":"message","model":"claude-haiku-5-5","usage":{"input_tokens":2,"output_tokens":5,` +
		`"cache_creation_input_tokens":438,"service_tier":"standard"}}`
	x := usage.New(usage.Anthropic, "/v1/messages", false)
	x.Write([]byte(body))
	res := x.Finish(true)

	s := &Server{opts: Options{Pricing: tbl}}
	row := &store.Request{Model: "claude-haiku-5-5"}
	s.price(row, &flight{provider: usage.Anthropic, rt: &route{priced: true}, start: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)}, res)
	if row.CostUSD != nil {
		t.Fatalf("expected no cost without a TTL split, got %v", *row.CostUSD)
	}
}
