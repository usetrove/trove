package pricing

import (
	"fmt"
	"sort"
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

// CostWithAdded is the reverse of CostWithoutAll: req is what was billed after
// an optimizer removed tokens, and added is how many tokens it removed. It
// prices the request as if those tokens had still been sent, and returns that as
// Before, with req's own cost as After, so Savings is what the optimizer saved.
//
// The removed tokens are spread over the prompt categories in proportion to how
// the request's prompt was actually billed (input, cache reads, each cache-write
// TTL). That is an estimate: a removed token sat in the cached prefix more often
// than not, but where exactly is not known after the fact. Long-context tiers
// are re-evaluated on the larger prompt. Removed is the number of tokens added.
func (t *Table) CostWithAdded(req Request, added int64) (Counterfactual, error) {
	if added < 0 {
		return Counterfactual{}, fmt.Errorf("cannot add back a negative number of tokens (%d)", added)
	}
	before := req
	before.Usage.CacheWrite = copyWrites(req.Usage.CacheWrite)

	type part struct {
		n   int64
		set func(int64)
	}
	parts := []part{
		{req.Usage.UncachedInput, func(v int64) { before.Usage.UncachedInput += v }},
		{req.Usage.CacheRead, func(v int64) { before.Usage.CacheRead += v }},
	}
	ttls := make([]string, 0, len(req.Usage.CacheWrite))
	for ttl := range req.Usage.CacheWrite {
		ttls = append(ttls, ttl)
	}
	sort.Strings(ttls)
	for _, ttl := range ttls {
		ttl := ttl
		parts = append(parts, part{req.Usage.CacheWrite[ttl], func(v int64) { before.Usage.CacheWrite[ttl] += v }})
	}
	var total int64
	for _, p := range parts {
		total += p.n
	}
	if total == 0 {
		parts[0].set(added) // no prompt to copy the mix from: price as plain input
	} else {
		// Largest-remainder split so the shares add up to exactly added.
		shares := make([]int64, len(parts))
		rems := make([]int64, len(parts))
		var given int64
		for i, p := range parts {
			shares[i], rems[i] = added*p.n/total, added*p.n%total
			given += shares[i]
		}
		for left := added - given; left > 0; left-- {
			best := 0
			for i := range rems {
				if rems[i] > rems[best] {
					best = i
				}
			}
			shares[best]++
			rems[best] = -1
		}
		for i, p := range parts {
			p.set(shares[i])
		}
	}

	cf := Counterfactual{Before: t.Cost(before), After: t.Cost(req), Removed: added, Savings: zeroMoney()}
	if cf.Before.Status == Priced && cf.After.Status == Priced {
		cf.Valid = true
		cf.Savings = cf.Before.Total.Sub(cf.After.Total)
	}
	return cf, nil
}
