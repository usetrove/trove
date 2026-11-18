package autopilot

import (
	_ "embed"
	"fmt"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/usetrove/trove/internal/technique"
)

//go:embed thresholds.toml
var embeddedThresholds []byte

// activeThresholds is what DefaultThresholds reads: the shipped file, except in tests.
var activeThresholds = embeddedThresholds

// UsePlaceholderThresholdsForTest makes DefaultPolicy ignore the shipped benchmark thresholds until restore is
// called, so tests of trial mechanics do not change whenever a published study regenerates thresholds.toml.
// Tests only; nothing in the binary calls it.
func UsePlaceholderThresholdsForTest() (restore func()) {
	prev := activeThresholds
	activeThresholds = []byte("version = 1\n")
	return func() { activeThresholds = prev }
}

// TierThresholds are the trial numbers a pre-registered benchmark set for one technique tier (plan item 10.7).
// `python -m trovebench thresholds` writes them from published results; bench/scripts/launch/04_report.sh runs it.
type TierThresholds struct {
	ProbationMinSessionsPerArm int     `toml:"probation_min_sessions_per_arm"`
	TestFailMarginPts          float64 `toml:"test_fail_margin_pts"`
	Pairs                      int     `toml:"pairs"`
	CostChangeSD               float64 `toml:"cost_change_sd"`
	Source                     string  `toml:"source"`
}

// Thresholds maps a tier ("T1", "T2", "T3") to its benchmark thresholds. A missing tier keeps the placeholders.
type Thresholds struct {
	Version int                       `toml:"version"`
	Tiers   map[string]TierThresholds `toml:"tiers"`
}

// LoadThresholds parses a thresholds file. Every entry must name its benchmark source and stay in the generator's
// range, so a hand-edited or damaged value is refused rather than trusted.
func LoadThresholds(b []byte) (Thresholds, error) {
	var t Thresholds
	if err := toml.Unmarshal(b, &t); err != nil {
		return Thresholds{}, fmt.Errorf("the Autopilot thresholds file is not valid: %w", err)
	}
	for tier, v := range t.Tiers {
		switch {
		case tier != "T1" && tier != "T2" && tier != "T3":
			return Thresholds{}, fmt.Errorf("the Autopilot thresholds file names an unknown tier %q", tier)
		case !strings.HasPrefix(v.Source, "bench:"):
			return Thresholds{}, fmt.Errorf("the Autopilot thresholds for %s do not name a benchmark source; regenerate with `python -m trovebench thresholds`", tier)
		case v.ProbationMinSessionsPerArm < 10 || v.ProbationMinSessionsPerArm > 200 || v.Pairs < 30:
			return Thresholds{}, fmt.Errorf("the Autopilot thresholds for %s are outside what the benchmark generator writes", tier)
		case v.TestFailMarginPts <= 0 || v.TestFailMarginPts > 20:
			return Thresholds{}, fmt.Errorf("the Autopilot test-failure margin for %s is outside 0 to 20 points", tier)
		}
	}
	return t, nil
}

// DefaultThresholds returns the thresholds shipped in the binary. An unreadable file means none (placeholders stay),
// never wrong numbers.
func DefaultThresholds() Thresholds {
	t, err := LoadThresholds(activeThresholds)
	if err != nil {
		return Thresholds{}
	}
	return t
}

// withThresholds returns p with the benchmark's numbers for every tier that has them.
func (p Policy) withThresholds(t Thresholds) Policy {
	if len(t.Tiers) == 0 {
		return p
	}
	p.ProbationMinByTier = map[technique.Tier]int{}
	p.TestFailMarginByTier = map[technique.Tier]float64{}
	p.ThresholdSource = map[technique.Tier]string{}
	for tier, v := range t.Tiers {
		k := technique.Tier(tier)
		p.ProbationMinByTier[k] = v.ProbationMinSessionsPerArm
		p.TestFailMarginByTier[k] = v.TestFailMarginPts
		p.ThresholdSource[k] = v.Source
	}
	return p
}

// probationMin is the sessions each side of a trial needs for a technique of tier t: the benchmark's number when
// the tier has one, else the placeholder.
func (p Policy) probationMin(t technique.Tier) int {
	if n, ok := p.ProbationMinByTier[t]; ok {
		return n
	}
	return p.ProbationMinSessionsPerArm
}
