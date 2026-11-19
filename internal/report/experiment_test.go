package report

import (
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/experiment"
	"github.com/usetrove/trove/internal/measure"
)

// Every figure in the A/B section must carry its label, a 95% range and a
// sample size, and the rolled-back case must say why shadow missed it.
func TestExperimentLinesCarryLabelRangeAndSampleSize(t *testing.T) {
	cost := experiment.MetricResult{Metric: experiment.MetricCost, Kind: "relative", Value: 0.20, Low: 0.12, High: 0.28,
		Label: measure.Measured, Confidence: "medium", NOn: 40, NOff: 40, Strata: 1}
	turns := experiment.MetricResult{Metric: experiment.MetricTurns, Kind: "relative", Value: 0.55, Low: 0.40, High: 0.70,
		Label: measure.Measured, Confidence: "medium", NOn: 40, NOff: 40}
	ab := experiment.MetricResult{Metric: experiment.MetricAbandoned, Kind: "points", Value: 0.05, Low: -0.02, High: 0.12,
		Label: measure.Measured, Confidence: "low", NOn: 40, NOff: 40}
	r := experiment.Result{Optimizer: "shrinker", Status: experiment.RolledBack, NOn: 40, NOff: 40, Cost: &cost,
		Outcomes: []experiment.MetricResult{turns, ab}, Reason: "Trove set shrinker back to shadow because in your A/B comparison it costs more",
		OverheadTokensPerSession: 120, OverheadLabel: "estimated", SuggestReason: "no benchmark evidence for this optimizer yet"}
	lines := experimentLines(r)
	text := strings.Join(lines, "\n")
	for _, l := range lines {
		if strings.Contains(l, "n=") {
			for _, want := range []string{"measured", "95% range", "on /", "confidence"} {
				if !strings.Contains(l, want) {
					t.Errorf("line %q is missing %q", l, want)
				}
			}
		}
	}
	for _, want := range []string{"rolled back to shadow", "+20.0%", "+5.0 points", "estimated"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}

	// Cost per turn and per uncached token are primary; cost per session is secondary.
	cpt := experiment.MetricResult{Metric: experiment.MetricCostPerTurn, Kind: "relative", Value: -0.10, Low: -0.15, High: -0.05,
		Label: measure.Measured, Confidence: "medium", NOn: 40, NOff: 40}
	cpu := experiment.MetricResult{Metric: experiment.MetricCostPerUncached, Kind: "relative", Value: -0.08, Low: -0.14, High: -0.02,
		Label: measure.Measured, Confidence: "medium", NOn: 40, NOff: 40}
	r.CostPerTurn, r.CostPerUncached = &cpt, &cpu
	var perTurn, perUncached, perSession int
	for i, l := range experimentLines(r) {
		switch {
		case strings.HasPrefix(strings.TrimSpace(l), "cost per turn:"):
			perTurn = i + 1
		case strings.HasPrefix(strings.TrimSpace(l), "cost per uncached input token:"):
			perUncached = i + 1
		case strings.HasPrefix(strings.TrimSpace(l), "cost per session:"):
			perSession = i + 1
		}
		if strings.Contains(l, "cost per turn:") || strings.Contains(l, "cost per uncached input token:") {
			for _, want := range []string{"measured", "95% range", "n="} {
				if !strings.Contains(l, want) {
					t.Errorf("line %q is missing %q", l, want)
				}
			}
		}
	}
	if perTurn == 0 || perUncached == 0 || perSession == 0 || perTurn >= perUncached || perUncached >= perSession {
		t.Errorf("want cost per turn, then per uncached token, then per session; got lines %d %d %d", perTurn, perUncached, perSession)
	}

	if strings.Contains(strings.Join(experimentLines(r), "\n"), "adjusted") {
		t.Error("an unadjusted figure must not claim an adjustment")
	}
	adj := cost
	adj.Adjusted, adj.Covariate = true, experiment.CovariatePriorCost
	adj.UnadjustedValue, adj.UnadjustedLow, adj.UnadjustedHigh = 0.18, 0.02, 0.34
	r.Cost = &adj
	if got := strings.Join(experimentLines(r), "\n"); !strings.Contains(got, "adjusted for each session's prior cost (unadjusted +18.0%, range +2.0% to +34.0%)") {
		t.Errorf("an adjusted figure must show the unadjusted range next to it:\n%s", got)
	}

	few := experiment.Result{Optimizer: "x", Status: experiment.NotEnoughData, NOn: 3, NOff: 4, Reason: measure.TextNotEnough}
	if got := strings.Join(experimentLines(few), " "); !strings.Contains(got, "not enough data yet") || strings.Contains(got, "95%") {
		t.Errorf("a result with too little data must show no figure: %s", got)
	}
}
