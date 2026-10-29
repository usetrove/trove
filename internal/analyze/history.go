package analyze

import (
	"time"

	"github.com/usetrove/trove/internal/pricing"
)

// This file is the narrow surface the history audit (internal/logs) uses. The
// audit has token counts, tool names and result sizes but no request bodies, so
// it cannot run Analyze. It reuses the detector names, thresholds, finding shape
// and shadow mapping so a history finding and a gateway finding read the same.

// DefaultBytesPerToken is the prior used to turn a tool result's size into
// tokens when no provider-reported total is available to calibrate against.
const DefaultBytesPerToken = defaultBytesPerToken

// MinDuplicateBytes is the smallest tool result counted as a duplicate.
const MinDuplicateBytes = minDuplicateByte

// DefaultOptions returns options with every default filled in.
func DefaultOptions(tbl *pricing.Table) Options {
	o := Options{Pricing: tbl}
	o.defaults()
	return o
}

// NewFinding builds a finding from numbers the caller computed. Label is
// always "estimated".
func NewFinding(detector, confidence string, tokens int64, usd *float64, detail map[string]any) Finding {
	return Finding{Detector: detector, Tokens: tokens, USD: usd, Confidence: confidence, Label: "estimated", Detail: detail}
}

// ShadowFor maps findings onto what each optimizer would have removed, exactly
// as the gateway does for its own requests.
func ShadowFor(fs []Finding) Shadow { return shadowFor(fs) }

// ExpensiveModel runs the model-choice check on plain numbers. totalIn counts
// every input token of the request, cached or not.
func (a *Analyzer) ExpensiveModel(provider string, req pricing.Request, start time.Time, totalIn, output int64) (Finding, bool) {
	return a.expensiveModel(provider, req, start, totalIn, output)
}

// Pricing returns the table the analyzer was built with.
func (a *Analyzer) Pricing() *pricing.Table { return a.opts.Pricing }

// Options returns the analyzer's options.
func (a *Analyzer) Options() Options { return a.opts }
