package report

import (
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/measure"
)

func rule(signal string, v measure.Verdict) measure.RuleResult {
	return measure.RuleResult{Signal: signal, Comparison: measure.Comparison{Verdict: v}}
}

// "No regression detected" must name exactly the signals that were checked.
func TestQualityLineNamesCheckedSignals(t *testing.T) {
	checks := []measure.OptimizerCheck{
		{Optimizer: "a", Verdict: measure.NoRegression, Rules: []measure.RuleResult{
			rule("tool errors per session", measure.NoRegression),
			rule("sessions that re-fetched a result an optimizer removed", measure.NotEnoughData)}},
		{Optimizer: "b", Verdict: measure.NoRegression, Rules: []measure.RuleResult{
			rule("repeated identical tool calls per session", measure.NoRegression),
			rule("tool errors per session", measure.NoRegression)}},
	}
	got := qualityLine(checks)
	want := "No regression detected locally in: tool errors per session, repeated identical tool calls per session. It does not prove output quality."
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	if strings.Contains(got, "re-fetched") {
		t.Error("named a signal that had too little data")
	}
}

func TestQualityLineWithoutDataClaimsNothing(t *testing.T) {
	for _, checks := range [][]measure.OptimizerCheck{nil, {{Optimizer: "a", Verdict: measure.NotEnoughData,
		Rules: []measure.RuleResult{rule("tool errors per session", measure.NotEnoughData)}}}} {
		got := qualityLine(checks)
		if strings.Contains(got, "No regression detected") || !strings.Contains(got, "not enough") {
			t.Errorf("qualityLine(%v) = %q", checks, got)
		}
	}
}
