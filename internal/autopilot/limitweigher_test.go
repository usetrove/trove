package autopilot

import (
	"testing"

	"github.com/usetrove/trove/internal/experiment"
	"github.com/usetrove/trove/internal/measure/limitfit"
)

// The weekly weigher reads the weekly meter only: a fit that has only the 5-hour meter cannot weigh the week.
func TestLimitWeigherPicksTheWeek(t *testing.T) {
	u := experiment.SessionUsage{USD: 2, HasUSD: true, FromGateway: true}
	only5h := &limitfit.Result{FiveHour: limitfit.MeterFit{PerUSD: &limitfit.Range{Point: 0.5, Lo: 0.4, Hi: 0.6}}}
	if w := LimitWeigher(only5h, true, optT0); w.Weigh != nil {
		t.Error("a fit with no weekly meter weighed the week")
	}
	both := &limitfit.Result{
		FiveHour: limitfit.MeterFit{PerUSD: &limitfit.Range{Point: 0.5, Lo: 0.4, Hi: 0.6}},
		Week:     limitfit.MeterFit{PerUSD: &limitfit.Range{Point: 0.05, Lo: 0.04, Hi: 0.06}},
	}
	w := LimitWeigher(both, true, optT0)
	if v, ok := w.Weigh(u); w.Metric != experiment.MetricWeekPct || !ok || v != 0.1 {
		t.Errorf("week: %s %v %v", w.Metric, v, ok)
	}
	w = LimitWeigher(both, false, optT0)
	if v, ok := w.Weigh(u); w.Metric != experiment.MetricLimitPct || !ok || v != 1 {
		t.Errorf("5-hour: %s %v %v", w.Metric, v, ok)
	}
}
