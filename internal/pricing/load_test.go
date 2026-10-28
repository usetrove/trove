package pricing

import (
	"strings"
	"testing"
	"time"
)

// priceChangeFixture has one model whose price doubles on 2026-02-01, plus a model
// with no published effective date.
const priceChangeFixture = `{
  "schema_version": 1,
  "data_version": "test.1",
  "entries": [
    {"provider": "anthropic", "model": "m", "effective_from": "2026-01-01", "effective_from_source": "https://example.com/a",
     "standard": {"input": "1", "output": "2"}, "source_url": "https://example.com/p", "verified_at": "2026-01-01"},
    {"provider": "anthropic", "model": "m", "effective_from": "2026-02-01", "effective_from_source": "https://example.com/a",
     "standard": {"input": "2", "output": "4"}, "source_url": "https://example.com/p", "verified_at": "2026-02-01"},
    {"provider": "openai", "model": "undated", "effective_from": null,
     "standard": {"input": "1", "output": "1"}, "source_url": "https://example.com/p", "verified_at": "2026-02-01"}
  ]
}`

func fixtureTable(t *testing.T, doc string) *Table {
	t.Helper()
	tb, err := Load(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return tb
}

func TestPriceChangePartwayThroughARange(t *testing.T) {
	tb := fixtureTable(t, priceChangeFixture)
	// One request per day of 1,000,000 uncached input tokens and 500,000 output tokens.
	// Before 2026-02-01: 1x1 + 0.5x2 = 2 per day. From 2026-02-01: 1x2 + 0.5x4 = 4 per day.
	days := []struct {
		day  string
		want string
	}{
		{"2026-01-30", "2"}, {"2026-01-31T23:59:59Z", "2"}, {"2026-02-01T00:00:00Z", "4"}, {"2026-02-02", "4"},
	}
	total := zeroMoney()
	for _, d := range days {
		c := tb.Cost(Request{Provider: Anthropic, Model: "m", Time: at(d.day), Usage: Usage{UncachedInput: 1_000_000, Output: 500_000}})
		wantTotal(t, c, Priced, d.want)
		total = total.Add(c.Total)
	}
	if total.Cmp(MoneyFromString("12")) != 0 { // 2+2+4+4
		t.Errorf("range total = %s, want 12", total)
	}
	before := tb.Cost(Request{Provider: Anthropic, Model: "m", Time: at("2025-12-31T23:59:59Z"), Usage: Usage{Output: 1}})
	if before.Status != Unpriced {
		t.Errorf("a request before the first price took effect must be unpriced, got %s", before.Status)
	}
}

func TestUndatedEntryNeverPrices(t *testing.T) {
	tb := fixtureTable(t, priceChangeFixture)
	c := tb.Cost(Request{Provider: OpenAI, Model: "undated", Time: at("2026-06-01"), Usage: Usage{Output: 1000}})
	if c.Status != Unpriced || !strings.Contains(c.Reason, "no published effective date") {
		t.Fatalf("got %s %q, want unpriced for missing effective date", c.Status, c.Reason)
	}
}

func TestLoadRejectsBadData(t *testing.T) {
	entry := func(patch string) string {
		base := `{"provider":"anthropic","model":"m","effective_from":"2026-01-01","effective_from_source":"https://e.com",
		  "standard":{"input":"1","output":"2"},"source_url":"https://e.com","verified_at":"2026-01-01"` + patch + `}`
		return `{"schema_version":1,"data_version":"t","entries":[` + base + `]}`
	}
	tests := []struct {
		name, doc, want string
	}{
		{"unknown field", strings.Replace(entry(""), `"model"`, `"modle"`, 1), "unknown field"},
		{"schema from the future", `{"schema_version":2,"data_version":"t","entries":[]}`, "schema version"},
		{"missing data_version", `{"schema_version":1,"entries":[]}`, "data_version"},
		{"price as a float", strings.Replace(entry(""), `"input":"1"`, `"input":1`, 1), "JSON strings"},
		{"exponent price", strings.Replace(entry(""), `"input":"1"`, `"input":"1e3"`, 1), "plain decimal"},
		{"negative price", strings.Replace(entry(""), `"input":"1"`, `"input":"-1"`, 1), "negative"},
		{"missing source_url", strings.Replace(entry(""), `"source_url":"https://e.com"`, `"source_url":""`, 1), "source_url"},
		{"missing verified_at", strings.Replace(entry(""), `"verified_at":"2026-01-01"`, `"verified_at":null`, 1), "verified_at"},
		{"bad date", strings.Replace(entry(""), `"verified_at":"2026-01-01"`, `"verified_at":"01/02/2026"`, 1), "YYYY-MM-DD"},
		{"effective_from without a source", strings.Replace(entry(""), `"effective_from_source":"https://e.com",`, ``, 1), "effective_from_source"},
		{"unknown provider", strings.Replace(entry(""), `"anthropic"`, `"acme"`, 1), "unknown provider"},
		{"long context cheaper than standard", entry(`,"long_context":{"threshold_tokens":100,"input":"0.5","output":"2"}`), "lower than standard"},
		{"long context cache read cheaper", strings.Replace(entry(`,"long_context":{"threshold_tokens":100,"input":"1","output":"2","cache_read":"0.01"}`), `"standard":{"input":"1"`, `"standard":{"cache_read":"0.1","input":"1"`, 1), "cache_read is lower"},
		{"duplicate entry", strings.Replace(entry(""), `]}`, `,`+strings.TrimSuffix(strings.TrimPrefix(entry(""), `{"schema_version":1,"data_version":"t","entries":[`), `]}`)+`]}`, 1), "twice"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(strings.NewReader(tt.doc))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want one mentioning %q", err, tt.want)
			}
		})
	}
}

// Every shipped price needs a source and a verification date.
func TestEmbeddedDataIntegrity(t *testing.T) {
	tb := mustDefault(t)
	entries := tb.Entries()
	if len(entries) == 0 {
		t.Fatal("embedded pricing.json has no entries")
	}
	limit := time.Now().Add(48 * time.Hour)
	for _, e := range entries {
		id := e.Provider + "/" + e.Model
		if !strings.HasPrefix(e.SourceURL, "https://") {
			t.Errorf("%s: missing source_url", id)
		}
		if e.VerifiedAt == nil || e.VerifiedAt.After(limit) {
			t.Errorf("%s: verified_at missing or in the future", id)
		}
		if e.EffectiveFrom != nil && e.EffectiveFromSource == "" {
			t.Errorf("%s: effective_from has no source", id)
		}
		if e.TokenizerNote == "" && e.Provider == Anthropic {
			t.Errorf("%s: Anthropic entries must carry a tokenizer note", id)
		}
	}
}
