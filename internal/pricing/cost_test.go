package pricing

import (
	"strings"
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t, err = time.Parse("2006-01-02", s)
	}
	if err != nil {
		panic(err)
	}
	return t
}

func mustDefault(t *testing.T) *Table {
	t.Helper()
	tb, err := Default()
	if err != nil {
		t.Fatalf("embedded pricing data is invalid: %v", err)
	}
	return tb
}

func wantTotal(t *testing.T, c Cost, status Status, usd string) {
	t.Helper()
	if c.Status != status {
		t.Fatalf("status = %s (%s %v), want %s", c.Status, c.Reason, c.Unpriced, status)
	}
	if c.Total.Cmp(MoneyFromString(usd)) != 0 {
		t.Fatalf("total = %s, want %s; components: %+v", c.Total, usd, c.Components)
	}
}

// Worked examples. All prices are per million tokens, so cost = tokens x price / 1,000,000.
// Every expected figure is calculated by hand in the comments from the published prices.
func TestWorkedExamples(t *testing.T) {
	tb := mustDefault(t)

	// Sonnet 5.5 after the 2026-10-07 cache-read cut: input $2, 5m write $2.50,
	// 1h write $4, cache read $0.10, output $10.
	sonnetUse := Usage{UncachedInput: 10_000, CacheWrite: map[string]int64{"5m": 20_000, "1h": 5_000}, CacheRead: 100_000, Output: 2_000}
	haikuUse := func(uncached, read, out int64) Usage {
		return Usage{UncachedInput: uncached, CacheRead: read, Output: out}
	}

	tests := []struct {
		name   string
		req    Request
		status Status
		usd    string
	}{
		{
			name: "anthropic sonnet 5.5 cache writes and read, new cache-read price",
			req:  Request{Provider: Anthropic, Model: "claude-sonnet-5-5", Time: at("2026-10-15"), Usage: sonnetUse},
			// 10,000x2 = 0.02; 20,000x2.5 = 0.05; 5,000x4 = 0.02; 100,000x0.10 = 0.01; 2,000x10 = 0.02 -> 0.12
			status: Priced, usd: "0.12",
		},
		{
			name: "anthropic sonnet 5.5 on the day before the cache-read cut",
			req:  Request{Provider: Anthropic, Model: "claude-sonnet-5-5", Time: at("2026-10-06T23:59:59Z"), Usage: sonnetUse},
			// same, but cache read 100,000x0.20 = 0.02 -> 0.13
			status: Priced, usd: "0.13",
		},
		{
			name: "anthropic batch halves every category, including cache",
			req:  Request{Provider: Anthropic, Model: "claude-sonnet-5-5", Time: at("2026-10-15"), Batch: true, Usage: sonnetUse},
			// 0.12 x 0.5 = 0.06
			status: Priced, usd: "0.06",
		},
		{
			name: "anthropic us data residency is 1.1x and stacks with batch",
			req:  Request{Provider: Anthropic, Model: "claude-sonnet-5-5", Time: at("2026-10-15"), Batch: true, Region: "us", Usage: sonnetUse},
			// 0.12 x 0.5 x 1.1 = 0.066
			status: Priced, usd: "0.066",
		},
		{
			name: "anthropic web search is $10 per 1,000, on top of tokens",
			req:  Request{Provider: Anthropic, Model: "claude-sonnet-5-5", Time: at("2026-10-15"), Usage: Usage{Output: 2_000}, ToolCalls: map[string]int64{"web_search": 3}},
			// 2,000x10 = 0.02; 3 x 10/1000 = 0.03 -> 0.05
			status: Priced, usd: "0.05",
		},
		{
			name: "anthropic haiku 5.5 under 100k prompt uses base prices",
			req:  Request{Provider: Anthropic, Model: "claude-haiku-5-5", Time: at("2026-10-20"), Usage: haikuUse(40_000, 50_000, 1_000)},
			// prompt 90,000. 40,000x0.10 = 0.004; 50,000x0.01 = 0.0005; 1,000x0.50 = 0.0005 -> 0.005
			status: Priced, usd: "0.005",
		},
		{
			name: "anthropic haiku 5.5 over 100k prompt uses the higher prices for everything",
			req:  Request{Provider: Anthropic, Model: "claude-haiku-5-5", Time: at("2026-10-20"), Usage: haikuUse(40_000, 70_000, 1_000)},
			// prompt 110,000. 40,000x0.50 = 0.02; 70,000x0.05 = 0.0035; 1,000x2.50 = 0.0025 -> 0.026
			status: Priced, usd: "0.026",
		},
		{
			name: "anthropic haiku 5.5 counts cache reads toward the threshold",
			req:  Request{Provider: Anthropic, Model: "claude-haiku-5-5", Time: at("2026-10-20"), Usage: haikuUse(1_000, 100_000, 0)},
			// prompt 101,000. 1,000x0.50 = 0.0005; 100,000x0.05 = 0.005 -> 0.0055
			status: Priced, usd: "0.0055",
		},
		{
			name: "anthropic haiku 5.5 exactly at the threshold is not long context",
			req:  Request{Provider: Anthropic, Model: "claude-haiku-5-5", Time: at("2026-10-20"), Usage: haikuUse(100_000, 0, 0)},
			// prompt 100,000 is not over 100,000. 100,000x0.10 = 0.01
			status: Priced, usd: "0.01",
		},
		{
			name: "anthropic haiku 5.5 batch long context stacks the discount on the higher tier",
			req:  Request{Provider: Anthropic, Model: "claude-haiku-5-5", Time: at("2026-10-20"), Batch: true, Usage: haikuUse(200_000, 0, 10_000)},
			// 200,000x0.25 = 0.05; 10,000x1.25 = 0.0125 -> 0.0625
			status: Priced, usd: "0.0625",
		},
		{
			name: "openai gpt-5.5 cached input, short context",
			req:  Request{Provider: OpenAI, Model: "gpt-5.5", Time: at("2026-06-01"), Usage: Usage{UncachedInput: 20_000, CacheRead: 80_000, Output: 3_000}},
			// 20,000x5 = 0.10; 80,000x0.50 = 0.04; 3,000x30 = 0.09 -> 0.23
			status: Priced, usd: "0.23",
		},
		{
			name: "openai gpt-5.5 long context (over 272K) reprices the whole request",
			req:  Request{Provider: OpenAI, Model: "gpt-5.5", Time: at("2026-06-01"), Usage: Usage{UncachedInput: 100_000, CacheRead: 200_000, Output: 3_000}},
			// prompt 300,000. 100,000x10 = 1.00; 200,000x1 = 0.20; 3,000x45 = 0.135 -> 1.335
			status: Priced, usd: "1.335",
		},
		{
			name: "openai gpt-5.5 batch",
			req:  Request{Provider: OpenAI, Model: "gpt-5.5", Time: at("2026-06-01"), Batch: true, Usage: Usage{UncachedInput: 20_000, CacheRead: 80_000, Output: 3_000}},
			// 0.23 x 0.5 = 0.115
			status: Priced, usd: "0.115",
		},
		{
			name: "openai web search tool fee",
			req:  Request{Provider: OpenAI, Model: "gpt-5.5", Time: at("2026-06-01"), Usage: Usage{Output: 1_000}, ToolCalls: map[string]int64{"web_search": 2}},
			// 1,000x30 = 0.03; 2 x 10/1000 = 0.02 -> 0.05
			status: Priced, usd: "0.05",
		},
		{
			name: "openai gpt-6.1-sol 30m and 12h cache writes",
			req:  Request{Provider: OpenAI, Model: "gpt-6.1-sol", Time: at("2026-10-01"), Usage: Usage{UncachedInput: 1_000, CacheWrite: map[string]int64{"30m": 10_000, "12h": 2_000}, CacheRead: 50_000, Output: 500}},
			// 1,000x2 = 0.002; 10,000x2.5 = 0.025; 2,000x5 = 0.01; 50,000x0.10 = 0.005; 500x10 = 0.005 -> 0.047
			status: Priced, usd: "0.047",
		},
		{
			name: "openai gpt-5.5 has no cache-write price, so a write is reported unpriced",
			req:  Request{Provider: OpenAI, Model: "gpt-5.5", Time: at("2026-06-01"), Usage: Usage{UncachedInput: 1_000, CacheWrite: map[string]int64{"30m": 5_000}}},
			// only 1,000x5 = 0.005 is priced
			status: Partial, usd: "0.005",
		},
		{
			name: "google gemini 3.1 pro under 200k prompt",
			req:  Request{Provider: Google, Model: "gemini-3.1-pro-preview", Time: at("2026-06-01"), Usage: Usage{UncachedInput: 150_000, CacheRead: 30_000, Output: 5_000}},
			// prompt 180,000. 150,000x2 = 0.30; 30,000x0.20 = 0.006; 5,000x12 = 0.06 -> 0.366
			status: Priced, usd: "0.366",
		},
		{
			name: "google gemini 3.1 pro over 200k prompt",
			req:  Request{Provider: Google, Model: "gemini-3.1-pro-preview", Time: at("2026-06-01"), Usage: Usage{UncachedInput: 250_000, CacheRead: 50_000, Output: 5_000}},
			// prompt 300,000. 250,000x4 = 1.00; 50,000x0.40 = 0.02; 5,000x18 = 0.09 -> 1.11
			status: Priced, usd: "1.11",
		},
		{
			name: "google gemini 3.1 pro batch halves input and output but not cache reads",
			req:  Request{Provider: Google, Model: "gemini-3.1-pro-preview", Time: at("2026-06-01"), Batch: true, Usage: Usage{UncachedInput: 150_000, CacheRead: 30_000, Output: 5_000}},
			// 150,000x1 = 0.15; 30,000x0.20 = 0.006 (same as standard); 5,000x6 = 0.03 -> 0.186
			status: Priced, usd: "0.186",
		},
		{
			name: "google gemini 3.5 flash-lite batch cache read is an absolute $0.02",
			req:  Request{Provider: Google, Model: "gemini-3.5-flash-lite", Time: at("2026-08-01"), Batch: true, Usage: Usage{CacheRead: 1_000_000}},
			// 1,000,000x0.02 = 0.02
			status: Priced, usd: "0.02",
		},
		{
			name: "google argon, last second before the scheduled doubling",
			req:  Request{Provider: Google, Model: "gemini-4-argon", Time: at("2026-11-30T23:59:59Z"), Usage: Usage{UncachedInput: 1_000_000, Output: 100_000}},
			// 1,000,000x2 = 2.00; 100,000x10 = 1.00 -> 3.00
			status: Priced, usd: "3",
		},
		{
			name: "google argon, from the scheduled doubling",
			req:  Request{Provider: Google, Model: "gemini-4-argon", Time: at("2026-12-01T00:00:00Z"), Usage: Usage{UncachedInput: 1_000_000, Output: 100_000}},
			// 1,000,000x4 = 4.00; 100,000x20 = 2.00 -> 6.00
			status: Priced, usd: "6",
		},
		{
			name: "google search grounding depends on a free allowance, so it is not priced",
			req:  Request{Provider: Google, Model: "gemini-2.5-flash", Time: at("2026-06-01"), Usage: Usage{Output: 1_000_000}, ToolCalls: map[string]int64{"google_search": 3}},
			// 1,000,000x2.5 = 2.50; grounding unpriced
			status: Partial, usd: "2.5",
		},
		{
			name: "google thought tokens are billed on top of candidate tokens at the output price",
			req:  Request{Provider: Google, Model: "gemini-2.5-flash", Time: at("2026-06-01"), Usage: Usage{Output: 1_000_000, Reasoning: 400_000}},
			// 1,000,000x2.50 = 2.50; 400,000x2.50 = 1.00 -> 3.50
			status: Priced, usd: "3.5",
		},
		{
			name: "google thought tokens in batch use the batch output price",
			req:  Request{Provider: Google, Model: "gemini-2.5-flash", Time: at("2026-06-01"), Batch: true, Usage: Usage{Output: 1_000_000, Reasoning: 400_000}},
			// (1,000,000 + 400,000) x 1.25 = 1.75
			status: Priced, usd: "1.75",
		},
		{
			name: "openai gpt-6.1-sol fast mode",
			req:  Request{Provider: OpenAI, Model: "gpt-6.1-sol", Time: at("2026-10-01"), ServiceTier: "fast", Usage: Usage{UncachedInput: 1_000, CacheWrite: map[string]int64{"30m": 10_000}, CacheRead: 50_000, Output: 500}},
			// fast: input 4, 30m write 5, read 0.20, output 20.
			// 1,000x4 = 0.004; 10,000x5 = 0.05; 50,000x0.20 = 0.01; 500x20 = 0.01 -> 0.074
			status: Priced, usd: "0.074",
		},
		{
			name: "openai gpt-6.1-sol fast mode long context",
			req:  Request{Provider: OpenAI, Model: "gpt-6.1-sol", Time: at("2026-10-01"), ServiceTier: "fast", Usage: Usage{UncachedInput: 300_000, Output: 1_000}},
			// fast long context: input 8, output 30. 300,000x8 = 2.40; 1,000x30 = 0.03 -> 2.43
			status: Priced, usd: "2.43",
		},
		{
			name: "openai gpt-6-astra ultrafast",
			req:  Request{Provider: OpenAI, Model: "gpt-6-astra", Time: at("2026-10-01"), ServiceTier: "ultrafast", Usage: Usage{UncachedInput: 100_000, Output: 10_000}},
			// 100,000x60 = 6.00; 10,000x300 = 3.00 -> 9.00
			status: Priced, usd: "9",
		},
		{
			name: "openai flex is priced like batch",
			req:  Request{Provider: OpenAI, Model: "gpt-5.5", Time: at("2026-06-01"), ServiceTier: "flex", Usage: Usage{UncachedInput: 20_000, CacheRead: 80_000, Output: 3_000}},
			// same as the batch example: 0.23 x 0.5 = 0.115
			status: Priced, usd: "0.115",
		},
		{
			name: "google gemini 3.1 pro priority, under 200k",
			req:  Request{Provider: Google, Model: "gemini-3.1-pro-preview", Time: at("2026-06-01"), ServiceTier: "priority", Usage: Usage{UncachedInput: 150_000, Output: 5_000}},
			// 150,000x3.60 = 0.54; 5,000x21.60 = 0.108 -> 0.648
			status: Priced, usd: "0.648",
		},
		{
			name: "google gemini 3.1 pro priority, over 200k",
			req:  Request{Provider: Google, Model: "gemini-3.1-pro-preview", Time: at("2026-06-01"), ServiceTier: "priority", Usage: Usage{UncachedInput: 250_000, Output: 5_000}},
			// 250,000x7.20 = 1.80; 5,000x32.40 = 0.162 -> 1.962
			status: Priced, usd: "1.962",
		},
		{
			name: "google argon priority doubles with the scheduled change",
			req:  Request{Provider: Google, Model: "gemini-4-argon", Time: at("2026-12-01"), ServiceTier: "priority", Usage: Usage{UncachedInput: 1_000_000, Output: 100_000}},
			// 1,000,000x7.20 = 7.20; 100,000x36 = 3.60 -> 10.80
			status: Priced, usd: "10.8",
		},
		{
			name: "google flex is priced like batch",
			req:  Request{Provider: Google, Model: "gemini-2.5-pro", Time: at("2026-06-01"), ServiceTier: "flex", Usage: Usage{UncachedInput: 100_000, Output: 10_000}},
			// batch: input 0.625, output 5. 100,000x0.625 = 0.0625; 10,000x5 = 0.05 -> 0.1125
			status: Priced, usd: "0.1125",
		},
		{
			name: "anthropic opus 5.5 fast mode, cache prices derived from the multipliers",
			req:  Request{Provider: Anthropic, Model: "claude-opus-5-5", Time: at("2026-10-20"), ServiceTier: "fast", Usage: Usage{UncachedInput: 10_000, CacheWrite: map[string]int64{"5m": 20_000}, CacheRead: 100_000, Output: 1_000}},
			// fast input 8, 5m write 8x1.25 = 10, read 8x0.05 = 0.40, output 40.
			// 10,000x8 = 0.08; 20,000x10 = 0.20; 100,000x0.40 = 0.04; 1,000x40 = 0.04 -> 0.36
			status: Priced, usd: "0.36",
		},
		{
			name: "anthropic fast mode stacks with US data residency",
			req:  Request{Provider: Anthropic, Model: "claude-opus-5", Time: at("2026-10-20"), ServiceTier: "fast", Region: "us", Usage: Usage{UncachedInput: 100_000, Output: 10_000}},
			// (100,000x10 + 10,000x50) x 1.1 = (1.00 + 0.50) x 1.1 = 1.65
			status: Priced, usd: "1.65",
		},
		{
			name: "openai reasoning tokens are already inside output and not added again",
			req:  Request{Provider: OpenAI, Model: "gpt-5.5", Time: at("2026-06-01"), Usage: Usage{Output: 3_000, Reasoning: 2_000}},
			// 3,000x30 = 0.09
			status: Priced, usd: "0.09",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := tb.Cost(tt.req)
			wantTotal(t, c, tt.status, tt.usd)
			if c.PricingVersion != tb.Version() {
				t.Errorf("pricing_version = %q, want %q", c.PricingVersion, tb.Version())
			}
		})
	}
}

func TestUnpriced(t *testing.T) {
	tb := mustDefault(t)
	u := Usage{UncachedInput: 1000, Output: 1000}
	tests := []struct {
		name    string
		req     Request
		wantMsg string
	}{
		{"unknown model", Request{Provider: Anthropic, Model: "claude-nonexistent", Time: at("2026-10-20"), Usage: u}, "no price on file"},
		{"known name under the wrong provider", Request{Provider: OpenAI, Model: "claude-sonnet-5-5", Time: at("2026-10-20"), Usage: u}, "no price on file"},
		{"before the model existed", Request{Provider: Anthropic, Model: "claude-sonnet-5-5", Time: at("2026-09-27T23:59:59Z"), Usage: u}, "not priced before 2026-09-28"},
		{"unsupported region", Request{Provider: Anthropic, Model: "claude-haiku-4-5", Time: at("2026-10-20"), Region: "us", Usage: u}, "region"},
		{"openai regional uplift is not on file", Request{Provider: OpenAI, Model: "gpt-5.5", Time: at("2026-10-20"), Region: "us", Usage: u}, "region"},
		{"a service tier the model does not have", Request{Provider: OpenAI, Model: "gpt-5.5", Time: at("2026-10-20"), ServiceTier: "ultrafast", Usage: u}, "service tier price"},
		{"an invented service tier", Request{Provider: OpenAI, Model: "gpt-5.5", Time: at("2026-10-20"), ServiceTier: "turbo", Usage: u}, "service tier price"},
		{"fast with batch", Request{Provider: Anthropic, Model: "claude-opus-5-5", Time: at("2026-10-20"), ServiceTier: "fast", Batch: true, Usage: u}, "cannot be combined with batch"},
		{"fast above the long-context threshold has no listed price", Request{Provider: OpenAI, Model: "gpt-5.5", Time: at("2026-10-20"), ServiceTier: "fast", Usage: Usage{UncachedInput: 300_000}}, "no long-context price"},
		{"flex on a model without it", Request{Provider: Anthropic, Model: "claude-opus-5-5", Time: at("2026-10-20"), ServiceTier: "flex", Usage: u}, "no flex price"},
		{"batch on a model without batch prices", Request{Provider: OpenAI, Model: "gpt-5.3-codex", Time: at("2026-10-20"), Batch: true, Usage: u}, "batch"},
		{"negative tokens", Request{Provider: Anthropic, Model: "claude-haiku-4-5", Time: at("2026-10-20"), Usage: Usage{Output: -1}}, "negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := tb.Cost(tt.req)
			if c.Status != Unpriced {
				t.Fatalf("status = %s, want unpriced (total %s)", c.Status, c.Total)
			}
			if !strings.Contains(c.Reason, tt.wantMsg) {
				t.Errorf("reason %q does not mention %q", c.Reason, tt.wantMsg)
			}
			if c.Total.Sign() != 0 {
				t.Errorf("unpriced cost has a total of %s; it must be zero", c.Total)
			}
		})
	}
}

func TestAliasesAndCase(t *testing.T) {
	tb := mustDefault(t)
	u := Usage{UncachedInput: 1_000_000}
	want := tb.Cost(Request{Provider: Anthropic, Model: "claude-haiku-4-5", Time: at("2026-10-20"), Usage: u})
	got := tb.Cost(Request{Provider: "Anthropic", Model: "CLAUDE-HAIKU-4-5-20251001", Time: at("2026-10-20"), Usage: u})
	wantTotal(t, want, Priced, "1")
	wantTotal(t, got, Priced, "1")
}

func TestAPIEquivalentValue(t *testing.T) {
	tb := mustDefault(t)
	u := Usage{UncachedInput: 1_000_000, Output: 100_000}
	got := tb.APIEquivalentValue(Anthropic, "claude-opus-5-5", at("2026-10-20"), u)
	// 1,000,000x4 = 4.00; 100,000x20 = 2.00 -> 6.00
	wantTotal(t, got.Cost, Priced, "6")
	if got.Label != "estimated" {
		t.Errorf("label = %q, want estimated", got.Label)
	}
}
