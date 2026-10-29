package pricing

import (
	"fmt"
	"strings"
)

// Categories a counterfactual can remove tokens from.
const (
	CatInput     = "input"      // uncached input
	CatCacheRead = "cache_read" // priced at the cache-read rate, not the input rate
	CatOutput    = "output"     // for Anthropic and OpenAI this includes reasoning
	// CatReasoning removes Gemini thought tokens, which are billed on top of output.
	// For Anthropic and OpenAI reasoning is already inside output, so removing it
	// from here changes nothing; remove from CatOutput instead.
	CatReasoning = "reasoning"

	catWritePrefix = "cache_write_" // followed by the TTL, e.g. "cache_write_5m"
)

// CatCacheWrite returns the category name for cache writes of a given TTL.
func CatCacheWrite(ttl string) string { return catWritePrefix + ttl }

// Counterfactual is the cost of the same request with some tokens removed.
type Counterfactual struct {
	Before  Cost  `json:"before"`
	After   Cost  `json:"after"`
	Removed int64 `json:"removed_tokens"` // after clamping to what the request actually had
	// Savings is Before-After. It is only meaningful when both costs are fully
	// priced; otherwise Valid is false and Savings is zero.
	Savings Money `json:"savings_usd"`
	Valid   bool  `json:"valid"`
}

// CostWithout prices req as reported and again with n tokens removed from category.
// A token removed from the cache-read category saves the cache-read price, not the
// input price. If removal drops the prompt below a long-context threshold the lower
// tier applies to what is left, as the provider would have billed it. n is clamped
// to the tokens present in that category.
func (t *Table) CostWithout(req Request, category string, n int64) (Counterfactual, error) {
	return t.CostWithoutAll(req, map[string]int64{category: n})
}

// CostWithoutAll is CostWithout for several categories at once, for example a
// span of tokens that was billed partly as cache reads and partly as input.
// Each count is clamped to what the category holds; Removed is the total
// actually removed. Long-context tiers are re-evaluated once, on the final
// prompt, exactly as the provider would.
func (t *Table) CostWithoutAll(req Request, removals map[string]int64) (Counterfactual, error) {
	after := req
	after.Usage.CacheWrite = copyWrites(req.Usage.CacheWrite)

	var removed int64
	for category, n := range removals {
		if n < 0 {
			return Counterfactual{}, fmt.Errorf("cannot remove a negative number of tokens (%d)", n)
		}
		got, err := remove(&after, req.Provider, category, n)
		if err != nil {
			return Counterfactual{}, err
		}
		removed += got
	}
	// For Anthropic and OpenAI, reasoning is a subset of output; keep it consistent.
	// For Google it is separate and removed through CatReasoning.
	if req.Provider != Google && after.Usage.Reasoning > after.Usage.Output {
		after.Usage.Reasoning = after.Usage.Output
	}

	cf := Counterfactual{Before: t.Cost(req), After: t.Cost(after), Removed: removed, Savings: zeroMoney()}
	if cf.Before.Status == Priced && cf.After.Status == Priced {
		cf.Valid = true
		cf.Savings = cf.Before.Total.Sub(cf.After.Total)
	}
	return cf, nil
}

// remove takes up to n tokens out of one category of after and returns how many it took.
func remove(after *Request, provider, category string, n int64) (int64, error) {
	var have int64
	var set func(int64)
	switch {
	case category == CatInput:
		have, set = after.Usage.UncachedInput, func(v int64) { after.Usage.UncachedInput = v }
	case category == CatCacheRead:
		have, set = after.Usage.CacheRead, func(v int64) { after.Usage.CacheRead = v }
	case category == CatOutput:
		have, set = after.Usage.Output, func(v int64) { after.Usage.Output = v }
	case category == CatReasoning && provider != Google:
		return 0, fmt.Errorf("reasoning tokens are already inside output for %s; remove them with the output category", provider)
	case category == CatReasoning:
		have, set = after.Usage.Reasoning, func(v int64) { after.Usage.Reasoning = v }
	case strings.HasPrefix(category, catWritePrefix):
		ttl := strings.TrimPrefix(category, catWritePrefix)
		have = after.Usage.CacheWrite[ttl]
		set = func(v int64) {
			if after.Usage.CacheWrite == nil {
				after.Usage.CacheWrite = map[string]int64{}
			}
			after.Usage.CacheWrite[ttl] = v
		}
	default:
		return 0, fmt.Errorf("unknown token category %q; use input, cache_read, output or cache_write_<ttl>", category)
	}
	n = min(n, have)
	set(have - n)
	return n, nil
}

func copyWrites(m map[string]int64) map[string]int64 {
	if m == nil {
		return nil
	}
	out := make(map[string]int64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
