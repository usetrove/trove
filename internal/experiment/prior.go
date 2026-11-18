package experiment

import (
	_ "embed"
	"fmt"
	"math"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

//go:embed priors.toml
var embeddedPriors []byte

// Ship rule from the benchmark lab (bench/config.yaml). An optimizer is only
// suggested when the lab's evidence clears it.
const (
	ShipMinCostReduction = 0.05 // min_cost_reduction_pct
	ShipSuccessMargin    = 0.05 // success_margin_pts, as a fraction
	ShipMinPairs         = 30   // min_pairs
)

// heterogeneity is the spread (as a relative cost change) between users that a
// benchmark average cannot capture. It widens the prior so one user's data can
// move it.
const heterogeneity = 0.05

// Prior is one optimizer's benchmark-lab result.
type Prior struct {
	CostChange   float64 `toml:"cost_change"`
	CostCILow    float64 `toml:"cost_ci_low"`
	CostCIHigh   float64 `toml:"cost_ci_high"`
	SuccessDiff  float64 `toml:"success_diff"`
	SuccessCILow float64 `toml:"success_ci_low"`
	Pairs        int     `toml:"pairs"`
	Source       string  `toml:"source"`
}

// Priors maps optimizer name to its benchmark prior.
type Priors struct {
	Version    int              `toml:"version"`
	Optimizers map[string]Prior `toml:"optimizers"`
}

// LoadPriors parses a priors file. A nil or empty input means no priors.
func LoadPriors(b []byte) (Priors, error) {
	var p Priors
	if len(b) > 0 {
		if err := toml.Unmarshal(b, &p); err != nil {
			return Priors{}, fmt.Errorf("the benchmark priors file is not valid: %w", err)
		}
	}
	for n, v := range p.Optimizers {
		if err := v.validate(n); err != nil {
			return Priors{}, err
		}
	}
	return p, nil
}

// sourcePrefixes are the evidence kinds a prior may come from, in the order the
// plan uses them: benchmark studies first, then opt-in contributed data, then
// team pools.
var sourcePrefixes = []string{"bench:", "contribute:", "team:"}

// validate rejects a prior that does not name where it came from or whose
// numbers contradict each other. A prior nobody can trace, or one whose
// estimate sits outside its own range, is dropped with an error rather than
// trusted.
func (p Prior) validate(name string) error {
	if p.Pairs < 1 || p.CostCIHigh < p.CostCILow {
		return fmt.Errorf("benchmark prior for %q needs pairs of at least 1 and cost_ci_high not below cost_ci_low", name)
	}
	named := false
	src := strings.TrimSpace(p.Source)
	for _, pre := range sourcePrefixes {
		if strings.HasPrefix(src, pre) && len(src) > len(pre) {
			named = true
		}
	}
	if !named {
		return fmt.Errorf("benchmark prior for %q must name its source (a source starting with bench:, contribute: or team:); regenerate the file with `python -m trovebench defaults`", name)
	}
	if p.CostChange < p.CostCILow || p.CostChange > p.CostCIHigh {
		return fmt.Errorf("benchmark prior for %q has a cost_change that is not inside its own cost range", name)
	}
	if p.SuccessDiff < p.SuccessCILow {
		return fmt.Errorf("benchmark prior for %q has a success_diff below its own success_ci_low, so its success range is wrong", name)
	}
	return nil
}

// DefaultPriors returns the priors shipped in the binary.
func DefaultPriors() Priors {
	p, err := LoadPriors(embeddedPriors)
	if err != nil {
		return Priors{} // an unreadable embedded file means no prior, never a wrong one
	}
	return p
}

// sd is the prior's standard deviation for the effect on one user: the
// benchmark's own uncertainty plus the spread between users.
func (p Prior) sd() float64 {
	se := (p.CostCIHigh - p.CostCILow) / (2 * 1.96)
	return math.Sqrt(se*se + heterogeneity*heterogeneity)
}

// Supports reports whether the benchmark evidence alone clears the ship rule:
// enough pairs, the whole cost interval at least ShipMinCostReduction below
// zero, and success not worse than the margin.
func (p Prior) Supports() (bool, string) {
	switch {
	case p.Pairs < ShipMinPairs:
		return false, fmt.Sprintf("the benchmark has %d paired runs; it needs at least %d", p.Pairs, ShipMinPairs)
	case p.CostCIHigh > -ShipMinCostReduction:
		return false, fmt.Sprintf("the benchmark's cost range does not clearly reach a %.0f%% reduction", ShipMinCostReduction*100)
	case p.SuccessCILow < -ShipSuccessMargin:
		return false, fmt.Sprintf("the benchmark could not rule out a success drop of more than %.0f points", ShipSuccessMargin*100)
	}
	return true, "the benchmark lab's paired runs clear the ship rule"
}

// Combined is the user's own estimate shrunk toward the prior.
type Combined struct {
	Value float64 `json:"value"`
	Low   float64 `json:"low"`
	High  float64 `json:"high"`
}

// combine is a normal-normal update. The data's standard error comes from its
// bootstrap interval. The prior pulls a noisy estimate toward the lab result;
// a precise estimate from many sessions barely moves.
//
// A prior never hides a loss: if the user's own range is wholly above zero (their
// sessions measurably cost more), it is shown unchanged, however good the
// benchmark looked.
func combine(est, lo, hi float64, p Prior) Combined {
	if lo > 0 {
		return Combined{Value: est, Low: lo, High: hi}
	}
	se := (hi - lo) / (2 * 1.96)
	if se < 1e-6 {
		se = 1e-6
	}
	ps := p.sd()
	wd, wp := 1/(se*se), 1/(ps*ps)
	mean := (est*wd + p.CostChange*wp) / (wd + wp)
	half := 1.96 / math.Sqrt(wd+wp)
	return Combined{Value: mean, Low: mean - half, High: mean + half}
}

// confidence names how much the range can be trusted: how narrow it is and how
// many sessions stand behind it. It is a plain label, not a probability.
func confidence(low, high float64, perArm, minPerArm int) string {
	half := (high - low) / 2
	switch {
	case half <= 0.03 && perArm >= 2*minPerArm:
		return "high"
	case half <= 0.10 && perArm >= minPerArm:
		return "medium"
	}
	return "low"
}
