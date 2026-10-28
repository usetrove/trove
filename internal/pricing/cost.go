package pricing

import (
	"fmt"
	"sort"
	"time"
)

// Usage holds token counts the way the provider reports them, after the adapter
// has mapped the provider's field names onto these disjoint buckets:
//
//   - Anthropic: input_tokens (excludes cache), cache_creation.ephemeral_5m/1h_input_tokens,
//     cache_read_input_tokens, output_tokens. Thinking is billed inside output_tokens.
//   - OpenAI: input_tokens includes cached and written tokens, so the adapter subtracts
//     them to get UncachedInput; reasoning tokens are a subset of output_tokens.
//   - Google: promptTokenCount includes cachedContentTokenCount, so the adapter subtracts
//     it. candidatesTokenCount (Output) excludes thoughtsTokenCount (Reasoning), which is
//     billed on top at the output price (totalTokenCount = prompt + tool-use + thoughts +
//     candidates; "response pricing is the sum of output tokens and thinking tokens").
//     toolUsePromptTokenCount is not modelled.
type Usage struct {
	UncachedInput int64            `json:"uncached_input"`
	CacheWrite    map[string]int64 `json:"cache_write,omitempty"` // by TTL
	CacheRead     int64            `json:"cache_read"`
	Output        int64            `json:"output"`
	// Reasoning is informational for Anthropic and OpenAI (already inside Output).
	// For Google it is additional to Output and billed at the output price.
	Reasoning int64 `json:"reasoning,omitempty"`
}

// Request is one provider call to price.
type Request struct {
	Provider string
	Model    string
	Time     time.Time
	Batch    bool
	// Region is a data-residency option such as "us"; empty means the provider default.
	Region string
	// ServiceTier is empty or "standard". Fast, priority, flex and similar tiers are
	// not priced yet and come back unpriced.
	ServiceTier string
	Usage       Usage
	// ToolCalls counts provider-side tool calls by tool name (for example "web_search").
	ToolCalls map[string]int64
}

// Status says how much of a request could be priced.
type Status string

// Pricing outcomes.
const (
	Priced   Status = "priced"   // every part priced
	Partial  Status = "partial"  // Total covers only the priced components; see Unpriced
	Unpriced Status = "unpriced" // nothing priced; see Reason
)

// Component is one line of a cost breakdown.
type Component struct {
	Name  string `json:"name"` // input, cache_write_5m, cache_read, output, tool_web_search
	Units int64  `json:"units"`
	USD   Money  `json:"usd"`
}

// UnpricedPart names a component that could not be priced and why.
type UnpricedPart struct {
	Component string `json:"component"`
	Reason    string `json:"reason"`
}

// Cost is the result of pricing one request. Values are "estimated" from published
// list prices; only provider invoices are authoritative.
type Cost struct {
	Status         Status         `json:"status"`
	Total          Money          `json:"total_usd"`
	Components     []Component    `json:"components"`
	PricingVersion string         `json:"pricing_version"`
	Reason         string         `json:"reason,omitempty"`
	Unpriced       []UnpricedPart `json:"unpriced,omitempty"`
}

// Cost prices req against the entry in force at req.Time. It never guesses: an
// unknown model, a date before the price took effect, or a missing price yields
// Unpriced or Partial with the reason.
func (t *Table) Cost(req Request) Cost {
	c := Cost{PricingVersion: t.file.DataVersion, Total: zeroMoney()}
	fail := func(reason string) Cost {
		c.Status, c.Reason = Unpriced, reason
		return c
	}
	if err := req.Usage.validate(); err != nil {
		return fail(err.Error())
	}
	for tool, n := range req.ToolCalls {
		if n < 0 {
			return fail(fmt.Sprintf("tool call count for %q is negative", tool))
		}
	}
	e, reason := t.Lookup(req.Provider, req.Model, req.Time)
	if e == nil {
		return fail(reason)
	}
	batch := req.Batch
	var premium *ServiceTier
	switch req.ServiceTier {
	case "", "standard":
	case "flex":
		if !e.FlexMatchesBatch {
			return fail(fmt.Sprintf("no flex price on file for %s", e.Model))
		}
		batch = true // flex is priced exactly like batch for these models
	default:
		st, ok := e.ServiceTiers[req.ServiceTier]
		if !ok {
			return fail(fmt.Sprintf("no %q service tier price on file for %s", req.ServiceTier, e.Model))
		}
		if req.Batch {
			return fail(fmt.Sprintf("the %q service tier cannot be combined with batch", req.ServiceTier))
		}
		premium = &st
	}
	regional := MustDec("1")
	if req.Region != "" {
		found := false
		for _, r := range e.Regional {
			if r.Name == req.Region {
				regional, found = r.Multiplier, true
			}
		}
		if !found {
			return fail(fmt.Sprintf("no %q region price on file for %s", req.Region, e.Model))
		}
	}

	u := req.Usage
	tier := e.Standard
	if premium != nil {
		tier = premium.Standard
	}
	if lc := e.LongContext; lc != nil && u.promptTokens() > lc.ThresholdTokens {
		switch {
		case premium == nil:
			tier = lc.Tier
		case premium.LongContext != nil:
			tier = premium.LongContext.Tier
		default:
			return fail(fmt.Sprintf("no long-context price for the %q tier on %s; the prompt is over %d tokens", req.ServiceTier, e.Model, lc.ThresholdTokens))
		}
	}

	add := func(name string, units int64, rate *Dec, mult *Dec, batchOK bool) {
		if units == 0 {
			return
		}
		if rate == nil {
			c.Unpriced = append(c.Unpriced, UnpricedPart{name, fmt.Sprintf("%s has no %s price on file", e.Model, name)})
			return
		}
		v := tokensAt(*rate, units)
		if batch {
			if !batchOK || mult == nil {
				c.Unpriced = append(c.Unpriced, UnpricedPart{name, fmt.Sprintf("%s has no batch price for %s on file", e.Model, name)})
				return
			}
			v.Mul(v, mult.r)
		}
		v.Mul(v, regional.r)
		c.Components = append(c.Components, Component{name, units, Money{v}})
	}
	var b Batch
	if e.Batch != nil {
		b = *e.Batch
	}
	hasBatch := e.Batch != nil
	add("input", u.UncachedInput, &tier.Input, b.Input, hasBatch)
	ttls := make([]string, 0, len(u.CacheWrite))
	for ttl := range u.CacheWrite {
		ttls = append(ttls, ttl)
	}
	sort.Strings(ttls)
	for _, ttl := range ttls {
		var rate *Dec
		if p, ok := tier.CacheWrite[ttl]; ok {
			rate = &p
		}
		add("cache_write_"+ttl, u.CacheWrite[ttl], rate, b.CacheWrite, hasBatch)
	}
	crRate, crMult := tier.CacheRead, b.CacheRead
	if batch && b.CacheReadPrice != nil {
		one := MustDec("1")
		crRate, crMult = b.CacheReadPrice, &one
	}
	add("cache_read", u.CacheRead, crRate, crMult, hasBatch)
	add("output", u.Output, &tier.Output, b.Output, hasBatch)
	if e.Provider == Google {
		// Gemini reports thought tokens separately from candidate tokens and bills
		// them at the output price.
		add("reasoning", u.Reasoning, &tier.Output, b.Output, hasBatch)
	}

	tools := make([]string, 0, len(req.ToolCalls))
	for tool := range req.ToolCalls {
		tools = append(tools, tool)
	}
	sort.Strings(tools)
	for _, tool := range tools {
		n := req.ToolCalls[tool]
		if n == 0 {
			continue
		}
		name := "tool_" + tool
		var fee *ToolFee
		for i := range e.ToolFees {
			if e.ToolFees[i].Tool == tool {
				fee = &e.ToolFees[i]
			}
		}
		switch {
		case fee == nil:
			c.Unpriced = append(c.Unpriced, UnpricedPart{name, fmt.Sprintf("no fee on file for tool %q on %s", tool, e.Model)})
		case fee.QuotaDepends:
			c.Unpriced = append(c.Unpriced, UnpricedPart{name, fmt.Sprintf("%s fee depends on a free allowance Trove cannot see (%s)", tool, fee.QuotaNote)})
		default:
			v := callsAt(fee.USDPer1000, n)
			c.Components = append(c.Components, Component{name, n, Money{v}})
		}
	}

	for _, comp := range c.Components {
		c.Total = c.Total.Add(comp.USD)
	}
	switch {
	case len(c.Unpriced) == 0:
		c.Status = Priced
	case len(c.Components) == 0:
		c.Status = Unpriced
		c.Reason = c.Unpriced[0].Reason
	default:
		c.Status = Partial
	}
	return c
}

func (u Usage) validate() error {
	if u.UncachedInput < 0 || u.CacheRead < 0 || u.Output < 0 || u.Reasoning < 0 {
		return fmt.Errorf("token counts cannot be negative")
	}
	for ttl, n := range u.CacheWrite {
		if n < 0 {
			return fmt.Errorf("cache write count for %s cannot be negative", ttl)
		}
	}
	return nil
}

// promptTokens is everything the model read: uncached, cache writes and cache reads.
func (u Usage) promptTokens() int64 {
	n := u.UncachedInput + u.CacheRead
	for _, w := range u.CacheWrite {
		n += w
	}
	return n
}
