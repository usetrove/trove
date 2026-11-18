package experiment

import (
	"fmt"
	"strings"
)

// TrialVerdict is the plain-English verdict of a rule-set trial: the status, then the four figures a rule set
// that asks for shorter replies is judged on (output tokens per turn, cost per turn, test-fail rate and turns),
// each labeled measured, or saying why there is no figure yet. A missing figure is said, never shown as zero.
func (r Result) TrialVerdict() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s", r.Status, r.Reason)
	fig := func(name string, m *MetricResult) {
		if m == nil {
			fmt.Fprintf(&b, "\n  %s: not enough sessions to compare yet", name)
			return
		}
		fmt.Fprintf(&b, "\n  %s: %s with it on (%s, 95%% range %s to %s; %d on, %d off)", name, signed(m.Value), m.Label, signed(m.Low), signed(m.High), m.NOn, m.NOff)
	}
	fig("output tokens per turn", r.OutputPerTurn)
	fig("cost per turn", r.CostPerTurn)
	switch t := r.TestFail; {
	case t == nil || t.OnSessions == 0 || t.OffSessions == 0:
		b.WriteString("\n  test-fail rate: no comparison yet (needs sessions on both sides that ran an allowlisted test)")
	default:
		fmt.Fprintf(&b, "\n  test-fail rate: %.0f%% with it on (%d sessions ran tests) against %.0f%% without (%d) (%s)",
			t.OnRate*100, t.OnSessions, t.OffRate*100, t.OffSessions, t.Label)
	}
	fig("turns", r.outcome(MetricTurns))
	return b.String()
}

func (r Result) outcome(metric string) *MetricResult {
	for i := range r.Outcomes {
		if r.Outcomes[i].Metric == metric {
			return &r.Outcomes[i]
		}
	}
	return nil
}
