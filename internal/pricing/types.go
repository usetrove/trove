package pricing

import (
	"encoding/json"
	"fmt"
	"time"
)

// Providers Trove prices.
const (
	Anthropic = "anthropic"
	OpenAI    = "openai"
	Google    = "google"
)

// SchemaVersion is the pricing.json layout this code understands.
const SchemaVersion = 1

// Date is a calendar day (UTC) written as YYYY-MM-DD in pricing.json.
type Date struct{ time.Time }

const dateLayout = "2006-01-02"

// UnmarshalJSON parses YYYY-MM-DD.
func (d *Date) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("dates must be strings like \"2026-10-28\": %w", err)
	}
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		return fmt.Errorf("date %q is not YYYY-MM-DD", s)
	}
	d.Time = t
	return nil
}

// MarshalJSON writes YYYY-MM-DD.
func (d Date) MarshalJSON() ([]byte, error) { return json.Marshal(d.Format(dateLayout)) }

// String is YYYY-MM-DD.
func (d Date) String() string { return d.Format(dateLayout) }

// File is the top level of pricing.json.
type File struct {
	SchemaVersion int      `json:"schema_version"`
	DataVersion   string   `json:"data_version"`
	Notes         []string `json:"notes,omitempty"`
	Entries       []Entry  `json:"entries"`
}

// Entry is the price list for one model that is valid from EffectiveFrom until a
// later entry for the same model takes over. EffectiveFrom is null when the
// provider does not publish a launch date; such an entry never prices a request.
type Entry struct {
	Provider      string   `json:"provider"`
	Model         string   `json:"model"`
	Aliases       []string `json:"aliases,omitempty"`
	EffectiveFrom *Date    `json:"effective_from"`
	// EffectiveFromSource is the official page that states the date (launch or
	// price-change announcement). Required when EffectiveFrom is set.
	EffectiveFromSource string `json:"effective_from_source,omitempty"`

	// Per-million-token prices in USD.
	Standard Tier `json:"standard"`
	// LongContext replaces Standard for the whole request when the prompt is longer
	// than ThresholdTokens (prompt = uncached input + cache writes + cache reads).
	LongContext *LongContext `json:"long_context,omitempty"`
	// Batch gives multipliers applied to each category; a missing multiplier means
	// "not priced in batch mode".
	Batch    *Batch     `json:"batch,omitempty"`
	Regional []Regional `json:"regional,omitempty"`
	ToolFees []ToolFee  `json:"tool_fees,omitempty"`

	TokenizerNote string   `json:"tokenizer_note,omitempty"`
	Notes         []string `json:"notes,omitempty"`
	SourceURL     string   `json:"source_url"`
	VerifiedAt    *Date    `json:"verified_at"`
}

// Tier is one set of per-million-token prices. CacheRead and CacheWrite entries
// are omitted when the provider does not offer them for the model.
type Tier struct {
	Input      Dec            `json:"input"`
	Output     Dec            `json:"output"`
	CacheRead  *Dec           `json:"cache_read,omitempty"`
	CacheWrite map[string]Dec `json:"cache_write,omitempty"` // by TTL: "5m", "1h", "30m", "12h"
}

// LongContext is the price set above a prompt-length threshold.
type LongContext struct {
	ThresholdTokens int64 `json:"threshold_tokens"`
	Tier
}

// Batch multipliers relative to the active (standard or long-context) tier.
type Batch struct {
	Input      *Dec `json:"input"`
	Output     *Dec `json:"output"`
	CacheRead  *Dec `json:"cache_read,omitempty"`
	CacheWrite *Dec `json:"cache_write,omitempty"`
	// CacheReadPrice is an absolute per-million batch cache-read price, for when the
	// provider's batch rate is not a clean multiple of the standard one. It takes
	// the place of CacheRead and cannot be combined with a long-context tier.
	CacheReadPrice *Dec `json:"cache_read_price,omitempty"`
}

// Regional is a data-residency multiplier on all token prices.
type Regional struct {
	Name       string `json:"name"` // e.g. "us"
	Multiplier Dec    `json:"multiplier"`
	Note       string `json:"note,omitempty"`
}

// ToolFee is a per-call fee for a provider-side tool.
type ToolFee struct {
	Tool         string `json:"tool"`
	USDPer1000   Dec    `json:"usd_per_1000_calls"`
	QuotaNote    string `json:"quota_note,omitempty"`
	QuotaDepends bool   `json:"quota_depends,omitempty"` // free allowance makes the cost unknowable from call counts
}
