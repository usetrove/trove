package report

import (
	"context"
	"fmt"

	"github.com/usetrove/trove/internal/experiment"
	"github.com/usetrove/trove/internal/store"
)

// buildExperiments recomputes every optimizer's A/B result from stored
// metadata, so the report always matches what a re-run would show.
func buildExperiments(ctx context.Context, db *store.DB, in Inputs) ([]experiment.Result, error) {
	return experiment.Evaluate(ctx, db, in.Config.Experiment, experiment.DefaultPriors())
}

// experimentLines renders one result as lines for the text report. Every figure
// carries its label, range and sample size.
func experimentLines(r experiment.Result) []string {
	out := []string{fmt.Sprintf("%s: %s", r.Optimizer, experimentStatus(r.Status))}
	if r.Cost == nil {
		return append(out, fmt.Sprintf("  %s (sessions with it on: %d, off: %d)", r.Reason, r.NOn, r.NOff))
	}
	out = append(out, "  "+r.Reason)
	out = append(out, "  "+metricLine("cost per session", *r.Cost))
	for _, o := range r.Outcomes {
		out = append(out, "  "+metricLine(o.Metric, o))
	}
	if r.Combined != nil {
		out = append(out, fmt.Sprintf("  cost per session, with benchmark prior (%s): %s (95%% range %s to %s), measured on your sessions combined with benchmark evidence",
			r.PriorSrc, signedPct(r.Combined.Value), signedPct(r.Combined.Low), signedPct(r.Combined.High)))
	}
	out = append(out, fmt.Sprintf("  its own overhead: about %.0f tokens per session (%s; already inside the measured cost, not subtracted again)", r.OverheadTokensPerSession, r.OverheadLabel))
	out = append(out, "  suggestion: "+r.SuggestReason)
	return out
}

func metricLine(name string, m experiment.MetricResult) string {
	f := signedPct
	if m.Kind == "points" {
		f = func(v float64) string { return fmt.Sprintf("%+.1f points", v*100) }
	}
	return fmt.Sprintf("%s: %s %s (95%% range %s to %s), n=%d on / %d off, confidence %s",
		name, m.Label, f(m.Value), f(m.Low), f(m.High), m.NOn, m.NOff, m.Confidence)
}

func signedPct(v float64) string { return fmt.Sprintf("%+.1f%%", v*100) }

func experimentStatus(s experiment.Status) string {
	switch s {
	case experiment.NotEnoughData:
		return "not enough data yet"
	case experiment.NoClearEffect:
		return "no clear difference"
	case experiment.Saves:
		return "saves money (measured)"
	case experiment.CostsMore:
		return "costs more (measured)"
	case experiment.WorseOutcomes:
		return "worse outcomes (measured)"
	case experiment.RolledBack:
		return "rolled back to shadow"
	}
	return string(s)
}
