package autopilot

import (
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/measure/limitfit"
)

func TestAnnotateLimitsIsDisplayOnly(t *testing.T) {
	per := limitfit.Range{Point: 0.5, Lo: 0.4, Hi: 0.6}
	fit := &limitfit.Result{Samples: 50, TopModel: "claude-sonnet-5-5",
		Mix:      [limitfit.NTypes]float64{limitfit.TInput: 0.05, limitfit.TCacheRead: 0.9, limitfit.TWrite1h: 0.05},
		FiveHour: limitfit.MeterFit{Meter: limitfit.FiveHour, Tier: limitfit.TierPrices, PerUSD: &per},
		Week:     limitfit.MeterFit{Meter: limitfit.SevenDay, Tier: limitfit.TierNone}}
	plan := []config.AutopilotPlanItem{
		{Optimizer: "a", Verdict: VerdictOn, Sessions: 10, NetTokens: 2_000_000, Reason: "r"},
		{Optimizer: "b", Verdict: "collecting", Sessions: 0, NetTokens: 500, Reason: "r"},
		{Optimizer: "c", Verdict: "collecting", Sessions: 4, NetTokens: -100, Reason: "r"},
	}
	before := append([]config.AutopilotPlanItem(nil), plan...)
	annotateLimits(plan, fit, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	if l := plan[0].Limit; l == nil || l.FiveHour == nil || l.FiveHour.Basis != limitfit.BasisAPIPrices || l.FiveHour.Point <= 0 || l.Week != nil {
		t.Fatalf("per-session share: %+v", plan[0].Limit)
	}
	if plan[1].Limit != nil || plan[2].Limit != nil {
		t.Error("no sessions or no net saving: nothing to convert")
	}
	for i := range plan {
		p := plan[i]
		p.Limit = nil
		if p != before[i] {
			t.Errorf("annotation changed a decision field: %+v -> %+v", before[i], p)
		}
	}
	if samePlan(plan, before) {
		t.Error("a new share is a plan change worth saving")
	}
	annotateLimits(plan, nil, time.Now()) // no fit: no change, no panic

	s := &Summary{Plan: []config.AutopilotPlanItem{{Optimizer: "b", Verdict: "collecting", Reason: "needs sessions",
		Limit: &config.AutopilotLimitShare{FiveHour: &config.PercentRange{Point: 0.31, Basis: limitfit.BasisFit}}}}}
	if txt := s.Text(); !strings.Contains(txt, "about 0.31% of 5h per session, estimated from your meter") {
		t.Errorf("advice text: %s", txt)
	}
}
