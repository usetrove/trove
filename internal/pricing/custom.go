package pricing

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
)

// CustomRates are a user's own per-million-token rates for a server that has no
// published list price (a self-hosted model, or an open-weight provider whose
// prices Trove does not track). They bypass pricing.json: there is no source URL
// to check, so every number computed from them is "estimated: your configured
// rates", never a provider price.
type CustomRates struct {
	Name      string
	Input     Dec
	Output    Dec
	CacheRead *Dec // nil: cache reads on this server are not priced
}

// NewCustomRates parses decimal strings (USD per million tokens). cacheRead may be empty.
func NewCustomRates(name, input, output, cacheRead string) (CustomRates, error) {
	in, err := ParseDec(input)
	if err != nil {
		return CustomRates{}, fmt.Errorf("input rate: %w", err)
	}
	out, err := ParseDec(output)
	if err != nil {
		return CustomRates{}, fmt.Errorf("output rate: %w", err)
	}
	c := CustomRates{Name: name, Input: in, Output: out}
	if cacheRead != "" {
		cr, err := ParseDec(cacheRead)
		if err != nil {
			return CustomRates{}, fmt.Errorf("cache read rate: %w", err)
		}
		c.CacheRead = &cr
	}
	return c, nil
}

// Version labels rows priced with these rates, so a later change of rates is visible.
func (c CustomRates) Version() string {
	cr := ""
	if c.CacheRead != nil {
		cr = c.CacheRead.String()
	}
	sum := sha256.Sum256([]byte(c.Name + "|" + c.Input.String() + "|" + c.Output.String() + "|" + cr))
	return "custom:" + hex.EncodeToString(sum[:4])
}

// Cost prices u with the configured rates. Cache writes have no custom rate, so
// a request that reports them comes back Partial rather than understated.
func (c CustomRates) Cost(u Usage) Cost {
	cost := Cost{PricingVersion: c.Version(), Total: zeroMoney()}
	if err := u.validate(); err != nil {
		cost.Status, cost.Reason = Unpriced, err.Error()
		return cost
	}
	add := func(name string, units int64, rate *Dec) {
		if units == 0 {
			return
		}
		if rate == nil {
			cost.Unpriced = append(cost.Unpriced, UnpricedPart{name, fmt.Sprintf("no custom %s rate is configured for %s", name, c.Name)})
			return
		}
		cost.Components = append(cost.Components, Component{name, units, Money{tokensAt(*rate, units)}})
	}
	add("input", u.UncachedInput, &c.Input)
	for ttl, n := range u.CacheWrite {
		add("cache_write_"+ttl, n, nil)
	}
	add("cache_read", u.CacheRead, c.CacheRead)
	add("output", u.Output, &c.Output)
	for _, comp := range cost.Components {
		cost.Total = cost.Total.Add(comp.USD)
	}
	switch {
	case len(cost.Unpriced) == 0:
		cost.Status = Priced
	case len(cost.Components) == 0:
		cost.Status, cost.Reason = Unpriced, cost.Unpriced[0].Reason
	default:
		cost.Status = Partial
	}
	return cost
}

// GPUHourUSDPerMTok turns a machine's hourly cost into an effective price per
// million tokens: hourly cost times the seconds the machine was counted for,
// divided by the tokens served in that time. It reports false when there is
// nothing to divide by. The result is an estimate: it depends on which seconds
// the caller counts (busy time or wall-clock time).
func GPUHourUSDPerMTok(hourly Dec, tokens int64, seconds float64) (float64, bool) {
	if !hourly.IsSet() || tokens <= 0 || seconds <= 0 {
		return 0, false
	}
	h, _ := new(big.Rat).Set(hourly.r).Float64()
	return h * (seconds / 3600) / float64(tokens) * 1e6, true
}
