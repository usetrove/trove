package limitfit

import (
	"time"

	"github.com/usetrove/trove/internal/pricing"
)

// Bases of a converted figure. Both are estimates.
const (
	BasisFit       = "fit"        // "estimated from your meter": weights per token type
	BasisAPIPrices = "api_prices" // "estimated from API prices": API-price weights with a fitted scale
)

// BasisText is how each basis reads in output.
var BasisText = map[string]string{
	BasisFit:       "estimated from your meter",
	BasisAPIPrices: "estimated from API prices",
}

// Delta is a change in tokens to express as a share of the limits.
type Delta struct {
	Model  string
	Tokens [NTypes]int64
	USD    float64 // the same tokens at API list prices; 0 = unknown (then only a type fit converts it)
}

// Share is a delta as percentage points of one limit, with its 90% interval.
type Share struct {
	Range
	Basis string `json:"basis"`
}

// Estimate is a delta in percent of each limit. Always "estimated". A nil window has no fit yet.
type Estimate struct {
	FiveHour *Share `json:"five_hour_pct,omitempty"`
	Week     *Share `json:"seven_day_pct,omitempty"`
	Label    string `json:"label"`
}

// Percent converts d into percent of each limit: by the type weights where the meter has them,
// otherwise by the API-price scale (which needs d.USD). It returns nil when neither limit converts.
func (r *Result) Percent(d Delta) *Estimate {
	if r == nil {
		return nil
	}
	e := Estimate{FiveHour: r.FiveHour.percent(d), Week: r.Week.percent(d), Label: "estimated"}
	if e.FiveHour == nil && e.Week == nil {
		return nil
	}
	return &e
}

// PercentUSD converts API-equivalent dollars into percent of each limit, by the API-price scale only.
func (r *Result) PercentUSD(usd float64) *Estimate {
	return r.Percent(Delta{USD: usd})
}

func (m *MeterFit) percent(d Delta) *Share {
	if m.Tier == TierFit && len(m.Weights) == NTypes && d.Tokens != ([NTypes]int64{}) {
		fam := Family(d.Model)
		fi := -1
		for i, f := range m.Families {
			if f.Family == fam && f.Separate {
				fi = i
			}
		}
		at := func(w []float64, mult float64) float64 {
			var p float64
			for k := 0; k < NTypes; k++ {
				p += w[k] * mult * float64(d.Tokens[k]) / 1e6
			}
			return p
		}
		point := make([]float64, NTypes)
		for k, w := range m.Weights {
			point[k] = w.Point
		}
		mult := 1.0
		if fi >= 0 {
			mult = m.Families[fi].Point
		}
		var xs []float64
		for _, rep := range m.Reps {
			rm := 1.0
			if fi >= 0 && NTypes+fi < len(rep) {
				rm = rep[NTypes+fi]
			}
			xs = append(xs, at(rep, rm))
		}
		return &Share{Range: percentiles(at(point, mult), xs), Basis: BasisFit}
	}
	if m.PerUSD != nil && d.USD > 0 {
		p := m.PerUSD
		return &Share{Range: Range{Point: p.Point * d.USD, Lo: p.Lo * d.USD, Hi: p.Hi * d.USD}, Basis: BasisAPIPrices}
	}
	return nil
}

// ContextDelta spreads n context tokens (tokens the agent reads: input, cache reads and writes) over
// the types in this user's own mix, on their most-used model.
func (r *Result) ContextDelta(n int64) Delta {
	d := Delta{}
	if r == nil {
		return d
	}
	d.Model = r.TopModel
	for k, share := range r.Mix {
		d.Tokens[k] = int64(float64(n)*share + 0.5)
	}
	return d
}

// PriceDelta fills d.USD from the price table (Anthropic list prices). It leaves d alone when the
// model has no price.
func PriceDelta(tbl *pricing.Table, d *Delta, at time.Time) {
	if tbl == nil || d.Model == "" {
		return
	}
	c := tbl.Cost(pricing.Request{Provider: pricing.Anthropic, Model: d.Model, Time: at, Usage: pricing.Usage{
		UncachedInput: d.Tokens[TInput], CacheRead: d.Tokens[TCacheRead], Output: d.Tokens[TOutput],
		CacheWrite: map[string]int64{"5m": d.Tokens[TWrite5m], "1h": d.Tokens[TWrite1h]},
	}})
	if c.Status == pricing.Priced {
		d.USD = c.Total.Float64()
	}
}
