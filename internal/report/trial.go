package report

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/autopilot"
	"github.com/usetrove/trove/internal/experiment"
	"github.com/usetrove/trove/internal/measure/limitfit"
	"github.com/usetrove/trove/internal/store"
)

// trialIdle is how long a hook-trial session must be quiet before it counts. It matches what the A/B section uses
// (experiment.Evaluate), so the same session is never counted in one place and not the other.
const trialIdle = 30 * time.Minute

// TrialFigures is one randomized session comparison, on against off, read the same way as the A/B section.
type TrialFigures struct {
	Optimizer string `json:"optimizer"`
	// Hook is true when the hooks split the sessions (a hand-off tool, a hook technique): the comparison reads the
	// agent's local history, so it works without the gateway, and is in tokens and percent of the limits.
	Hook bool `json:"hook_trial"`
	// Tokens is tokens per session for a hook trial (measured). Cost is cost per session at API list prices for a
	// trial the gateway splits (measured usage, estimated dollars). One of them is set.
	Tokens *experiment.Result `json:"tokens,omitempty"`
	Cost   *experiment.Result `json:"cost,omitempty"`
	// Week is a hook trial judged on percent of the weekly limit per session (estimated). Nil when there is no
	// weekly fit yet; WeekNote then says why.
	Week      *experiment.Result `json:"weekly_limit,omitempty"`
	WeekBasis string             `json:"weekly_limit_basis,omitempty"`
	WeekNote  string             `json:"weekly_limit_note,omitempty"`
	// FirstAt and LastAt are when the first and last compared sessions were assigned (Unix milliseconds).
	FirstAt int64 `json:"first_assigned_at,omitempty"`
	LastAt  int64 `json:"last_assigned_at,omitempty"`
}

// HasSessions reports whether any session was assigned to the trial.
func (t TrialFigures) HasSessions() bool { return t.FirstAt > 0 }

// main is the headline comparison: tokens for a hook trial, cost for a gateway one.
func (t TrialFigures) main() *experiment.Result {
	if t.Tokens != nil {
		return t.Tokens
	}
	return t.Cost
}

// trialFigures reads one optimizer's randomized comparison. It writes nothing.
func trialFigures(ctx context.Context, db *store.DB, in Inputs, name string) (TrialFigures, error) {
	f := TrialFigures{Optimizer: name, Hook: experiment.IsHookTrial(name)}
	var first, last sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT MIN(assigned_at), MAX(assigned_at) FROM experiment_assignments WHERE optimizer = ?`, name).
		Scan(&first, &last); err != nil {
		return f, err
	}
	f.FirstAt, f.LastAt = first.Int64, last.Int64
	x := in.Config.Experiment
	if !f.Hook {
		r, err := experiment.Analyze(ctx, db, name, x, experiment.DefaultPriors())
		if err != nil {
			return f, err
		}
		f.Cost = &r
		return f, nil
	}
	r, err := experiment.AnalyzeHookTrial(ctx, db, name, x, 0, in.Now, trialIdle)
	if err != nil {
		return f, err
	}
	f.Tokens = &r
	w := autopilot.LimitWeigher(in.LimitFit, true, in.Now)
	if w.Weigh == nil {
		f.WeekNote = weekNote(in.LimitFit)
		return f, nil
	}
	wr, err := experiment.AnalyzeHookTrialBy(ctx, db, name, x, 0, in.Now, trialIdle, w)
	if err != nil {
		return f, err
	}
	f.Week = &wr
	f.WeekBasis = limitfit.BasisText[limitfit.BasisAPIPrices]
	if in.LimitFit.Week.Tier == limitfit.TierFit {
		f.WeekBasis = limitfit.BasisText[limitfit.BasisFit]
	}
	return f, nil
}

// weekNote says why a hook trial has no weekly-limit figure.
func weekNote(fit *limitfit.Result) string {
	if fit == nil || fit.Week.CleanIntervals == 0 {
		return "no weekly-limit fit yet: it needs readings of the weekly meter from the status line (see docs/limit-fit.md)"
	}
	return "no weekly-limit fit yet: the weekly meter needs more clean readings (see docs/limit-fit.md)"
}

// changeText is a relative change with its 95% range, such as "-12.0% (95% range -20.1% to -3.9%)".
func changeText(m experiment.MetricResult) string {
	return fmt.Sprintf("%s (95%% range %s to %s)", signedPct(m.Value), signedPct(m.Low), signedPct(m.High))
}

// limitPointsText is a difference in points of a limit per session, such as "-0.04 points of the weekly limit per
// session (95% range -0.05 to -0.03)". A limit figure is a percent already, so a point is one percent of the limit.
func limitPointsText(m experiment.MetricResult, limit string) string {
	d := pointDecimals(m.Low, m.High)
	if math.Abs(m.Value) < 0.5*math.Pow10(-d) {
		m.Value = 0 // printed as +0.00, not -0.00
	}
	return fmt.Sprintf("%+.*f points of the %s per session (95%% range %+.*f to %+.*f)", d, m.Value, limit, d, m.Low, d, m.High)
}

// pointDecimals is enough decimals to show two significant figures of the larger end of a range, at least two and
// at most six, so a small effect is not rounded to a range that reads "-0.01 to -0.01".
func pointDecimals(lo, hi float64) int {
	v := math.Max(math.Abs(lo), math.Abs(hi))
	if v == 0 {
		return 2
	}
	return min(max(2, 1-int(math.Floor(math.Log10(v)))), 6)
}

// passText is the pass-rate change, or "" when there is not enough to compare.
func passText(r *experiment.Result) string {
	if r == nil || r.TestFail == nil || r.TestFail.PassChange == nil {
		return ""
	}
	p := r.TestFail.PassChange
	return fmt.Sprintf("pass rate %+.1f points (95%% range %+.1f to %+.1f; %d and %d sessions that ran tests), measured",
		p.Value*100, p.Low*100, p.High*100, r.TestFail.OnSessions, r.TestFail.OffSessions)
}

// trialVerdict sums up a randomized trial in one line. measured is false when it carries no figure yet.
func trialVerdict(f TrialFigures) (text string, measured bool) {
	r := f.main()
	if r == nil || r.Cost == nil {
		reason := "not enough sessions yet"
		if r != nil && r.Reason != "" {
			reason = r.Reason
		}
		return "randomized test running; nothing measured yet: " + reason, false
	}
	var parts []string
	if f.Tokens != nil {
		parts = append(parts, "tokens per session "+changeText(*r.Cost)+", measured")
	} else {
		parts = append(parts, "cost per session at API list prices "+changeText(*r.Cost)+", estimated from measured usage")
	}
	if w := f.Week; w != nil && w.Cost != nil {
		s := "share of the weekly limit per session " + changeText(*w.Cost)
		if w.HeadlinePoints != nil {
			s += ", " + limitPointsText(*w.HeadlinePoints, "weekly limit")
		}
		parts = append(parts, s+", "+f.WeekBasis)
	}
	if p := passText(r); p != "" {
		parts = append(parts, p)
	}
	return fmt.Sprintf("in a randomized test, with it on: %s (%d sessions on, %d off)", strings.Join(parts, "; "), r.NOn, r.NOff), true
}
