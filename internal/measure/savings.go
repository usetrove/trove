package measure

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"sort"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/store"
)

// RequestSavings is the money one request saved: what it would have cost had the
// removed tokens still been sent, minus what was billed. It uses the pricing
// engine's counterfactual helper, so cache reads and writes are priced at their
// own rates and long-context tiers are re-evaluated. req is the request as
// billed (the provider-reported usage); removed is how many prompt tokens the
// optimizer took out. ok is false when either side could not be priced exactly.
func RequestSavings(tbl *pricing.Table, req pricing.Request, removed int64) (savings pricing.Money, ok bool, err error) {
	cf, err := tbl.CostWithAdded(req, removed)
	if err != nil {
		return pricing.Money{}, false, err
	}
	return cf.Savings, cf.Valid, nil
}

// SumSavings adds per-request savings exactly. Items that could not be priced
// are counted, not guessed.
type SumSavings struct {
	Total    pricing.Money
	Priced   int
	Unpriced int
}

// Add includes one request's result.
func (s *SumSavings) Add(m pricing.Money, ok bool) {
	if !ok {
		s.Unpriced++
		return
	}
	s.Total = s.Total.Add(m)
	s.Priced++
}

// OptimizerSavings is one optimizer's savings in a window.
type OptimizerSavings struct {
	Optimizer string `json:"optimizer"`
	// USD is the money saved. Estimated: the removed-token count is derived
	// from bytes removed, and the saving of a request with several optimizers
	// is shared out by each one's token share.
	USD Figure `json:"usd"`
	// Tokens removed (estimated, same derivation). N is requests.
	Tokens   Figure `json:"tokens"`
	Sessions int    `json:"sessions"`
	// Corroboration compares provider-reported prompt tokens of on and off
	// sessions. It is nil unless both kinds of session exist.
	Corroboration *Corroboration `json:"corroboration,omitempty"`
}

// SavingsByOptimizer sums the recorded savings of requests in w, per optimizer.
// Nothing is mixed across optimizers, so the rows can be added only
// approximately: an overlap is shared, not double counted.
func SavingsByOptimizer(ctx context.Context, db *store.DB, w Window, m config.Measure) ([]OptimizerSavings, error) {
	rows, err := db.QueryContext(ctx, `SELECT session_key, optimizations_applied, tokens_removed, cost_diff_usd
		FROM requests WHERE ts >= ? AND ts < ? AND tokens_removed > 0 AND optimizations_applied IS NOT NULL`,
		w.From.UnixMilli(), w.To.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	type acc struct {
		usd      float64
		tokens   int64
		n        int
		unpriced int
		sessions map[string]bool
	}
	by := map[string]*acc{}
	for rows.Next() {
		var key, ap string
		var removed int64
		var diff sql.NullFloat64
		if err := rows.Scan(&key, &ap, &removed, &diff); err != nil {
			return nil, err
		}
		var a appliedJSON
		if json.Unmarshal([]byte(ap), &a) != nil {
			continue
		}
		for _, o := range a.Optimizers {
			if o.State != config.StateOn || o.TokensRemoved <= 0 {
				continue
			}
			x := by[o.Name]
			if x == nil {
				x = &acc{sessions: map[string]bool{}}
				by[o.Name] = x
			}
			// Savings are net of what the optimizer itself adds (rule 15).
			net := o.TokensRemoved - int64(o.OverheadTokens)
			x.n++
			x.tokens += net
			x.sessions[key] = true
			if diff.Valid && removed > 0 {
				x.usd += diff.Float64 * float64(net) / float64(removed)
			} else {
				x.unpriced++
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(by))
	for n := range by {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]OptimizerSavings, 0, len(names))
	for _, n := range names {
		x := by[n]
		os := OptimizerSavings{
			Optimizer: n, Sessions: len(x.sessions),
			USD:    Figure{Value: roundUSD(x.usd), Label: Estimated, N: x.n, Unpriced: x.unpriced},
			Tokens: Figure{Value: float64(x.tokens), Label: Estimated, N: x.n},
		}
		if c, err := Corroborate(ctx, db, n, m); err == nil && c != nil {
			os.Corroboration = c
		}
		out = append(out, os)
	}
	return out, nil
}

// roundUSD rounds to a millionth of a dollar, hiding float noise from summing.
func roundUSD(v float64) float64 { return math.Round(v*1e6) / 1e6 }

// Corroboration checks, with provider-reported numbers only, whether sessions
// with an optimizer on really sent fewer prompt tokens per request than
// comparable sessions with it off. It cannot prove the optimizer is the cause:
// sessions differ in size and task. It is the only place savings evidence earns
// the label "measured".
type Corroboration struct {
	Verdict Verdict `json:"verdict"` // NoRegression here means "no reduction shown"
	// Reduced is true only when the on sessions' mean is lower and the gap is
	// beyond chance.
	Reduced bool    `json:"reduced"`
	OnMean  float64 `json:"on_prompt_tokens_per_request"`
	OffMean float64 `json:"off_prompt_tokens_per_request"`
	NOn     int     `json:"n_on_sessions"`
	NOff    int     `json:"n_off_sessions"`
	Label   Label   `json:"label"`
	Text    string  `json:"text"`
}

// Corroborate returns nil when there are no off sessions to compare with.
func Corroborate(ctx context.Context, db *store.DB, optimizer string, m config.Measure) (*Corroboration, error) {
	rows, err := db.QueryContext(ctx, `SELECT p.states, p.pinned_at,
		CAST(s.total_input_uncached_tokens + s.total_cache_write_tokens + s.total_cache_read_tokens AS REAL) / s.request_count
		FROM sessions s JOIN session_pins p ON p.session_key = s.session_key WHERE s.request_count > 0`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	type pt struct {
		on     bool
		pinned int64
		v      float64
	}
	var all []pt
	var firstOn int64
	for rows.Next() {
		var st string
		var pinned int64
		var v float64
		if err := rows.Scan(&st, &pinned, &v); err != nil {
			return nil, err
		}
		var states map[string]string
		if json.Unmarshal([]byte(st), &states) != nil {
			continue
		}
		on := states[optimizer] == config.StateOn
		if on && (firstOn == 0 || pinned < firstOn) {
			firstOn = pinned
		}
		all = append(all, pt{on, pinned, v})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var on, off []float64
	for _, p := range all {
		switch {
		case p.on:
			on = append(on, p.v)
		case firstOn != 0 && p.pinned >= firstOn:
			off = append(off, p.v)
		}
	}
	if len(off) == 0 {
		return nil, nil
	}
	c := &Corroboration{Label: Measured, NOn: len(on), NOff: len(off), Verdict: NotEnoughData}
	if len(on) < m.MinSessions || len(off) < m.MinSessions {
		c.Text = TextNotEnough + " to compare on and off sessions"
		return c, nil
	}
	c.OnMean, c.OffMean = mean(on), mean(off)
	se := math.Sqrt(sampleVar(on)/float64(len(on)) + sampleVar(off)/float64(len(off)))
	c.Verdict = NoRegression
	c.Reduced = c.OffMean-c.OnMean > z95*se && c.OffMean > c.OnMean
	if c.Reduced {
		c.Text = "sessions with the optimizer on sent fewer prompt tokens per request than sessions with it off; sessions differ, so this supports but does not prove the saving"
	} else {
		c.Text = "no clear reduction in prompt tokens per request between on and off sessions yet"
	}
	return c, nil
}
