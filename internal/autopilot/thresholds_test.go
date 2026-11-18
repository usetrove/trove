package autopilot

import (
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/technique"
)

const benchThresholds = `version = 1

[tiers.T1]
probation_min_sessions_per_arm = 34
test_fail_margin_pts = 5
pairs = 120
cost_change_sd = 0.07
source = "bench:launch-1@abcdef123456"
`

func TestShippedThresholdsLoad(t *testing.T) {
	if _, err := LoadThresholds(embeddedThresholds); err != nil {
		t.Fatalf("the embedded thresholds.toml must load: %v", err)
	}
}

func TestBenchmarkThresholdsReplacePlaceholdersPerTier(t *testing.T) {
	th, err := LoadThresholds([]byte(benchThresholds))
	if err != nil {
		t.Fatal(err)
	}
	p := PolicyV2()
	p.ProbationMinByTier, p.TestFailMarginByTier = nil, nil
	p = p.withThresholds(th)
	if got := p.probationMin(technique.T1); got != 34 {
		t.Errorf("T1 sessions per arm %d, want the benchmark's 34", got)
	}
	if got := p.probationMin(technique.T3); got != p.ProbationMinSessionsPerArm {
		t.Errorf("T3 has no benchmark numbers, so it keeps the placeholder; got %d", got)
	}
	if p.testFailMargin(technique.T1) != 5 || p.testFailMargin(technique.T2) != 10 || p.testFailMargin(technique.T3) != 5 {
		t.Errorf("margins: T1 from the benchmark, T2 and T3 placeholders; got %v %v %v",
			p.testFailMargin(technique.T1), p.testFailMargin(technique.T2), p.testFailMargin(technique.T3))
	}
	if !strings.HasPrefix(p.ThresholdSource[technique.T1], "bench:") {
		t.Error("the source must be kept")
	}
}

func TestDamagedThresholdsAreRefused(t *testing.T) {
	for name, bad := range map[string]string{
		"no source":    strings.Replace(benchThresholds, `source = "bench:launch-1@abcdef123456"`, `source = "me"`, 1),
		"out of range": strings.Replace(benchThresholds, "= 34", "= 3", 1),
		"few pairs":    strings.Replace(benchThresholds, "pairs = 120", "pairs = 12", 1),
		"bad tier":     strings.Replace(benchThresholds, "tiers.T1", "tiers.T9", 1),
		"bad margin":   strings.Replace(benchThresholds, "test_fail_margin_pts = 5", "test_fail_margin_pts = 50", 1),
	} {
		if _, err := LoadThresholds([]byte(bad)); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
}
