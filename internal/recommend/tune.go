package recommend

import (
	"context"
	"fmt"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/store"
	"github.com/usetrove/trove/internal/tuning"
)

// TuneProposal is one suggested parameter value with the row `trove tune` stores
// for it.
type TuneProposal struct {
	Rec Recommendation
	Row store.TuningRow
}

// loosenStep is how each output-trimmer parameter moves when the trimmer is
// cutting too much in a repo: trim fewer outputs (a higher size floor) and keep
// more of each (more head and tail lines). Every result is clamped to the
// parameter's range.
var loosenStep = []struct {
	param string
	next  func(cur int) int
}{
	{optimize.ParamMinBytes, func(c int) int { return c * 3 / 2 }},
	{optimize.ParamKeepHead, func(c int) int { return c + 2 }},
	{optimize.ParamKeepTail, func(c int) int { return c + 5 }},
}

// ProposeTuning suggests per-repo output-trimmer values from the repo's own
// results, or nothing. It is a pure function of what is stored.
//
// The signal is the share of trimmed outputs the agent fetched back in this repo
// (measured, from the hooks' counts). When it reaches half the guardrail limit
// (config [measure] refetch_rate_percent), with at least min_sessions outputs
// behind it, the trimmer is cutting more than this repo's agent can do without,
// so it proposes loosening it. It proposes nothing while the trimmer is not on,
// while a tuning is already applied (the review decides that one first), or after
// a tuning was reverted (`trove tune reset` clears that and allows a new try).
func ProposeTuning(ctx context.Context, db *store.DB, cfg config.Config, repoKey string, now time.Time) ([]TuneProposal, error) {
	opt := config.OptimizerOutputTrimmer
	if repoKey == "" || cfg.OptimizerState(opt) != config.StateOn {
		return nil, nil
	}
	existing, err := db.Tuning().List(ctx, repoKey)
	if err != nil {
		return nil, err
	}
	for _, r := range existing {
		if r.Optimizer == opt && (r.State == store.TuningApplied || r.State == store.TuningReverted) {
			return nil, nil
		}
	}
	acted, bad, err := tuning.Rates(ctx, db, repoKey, opt, now.Add(-tuning.Window), now.Add(time.Minute))
	if err != nil {
		return nil, err
	}
	if acted == 0 || acted < cfg.Measure.MinSessions {
		return nil, nil
	}
	rate := float64(bad) / float64(acted)
	limit := float64(cfg.Measure.RefetchRatePercent) / 100
	if rate < limit/2 {
		return nil, nil
	}
	conf := Low
	switch {
	case acted >= 100:
		conf = High
	case acted >= 50:
		conf = Medium
	}
	cur := optimize.DefaultTrimThresholds
	byName := map[string]int{optimize.ParamMinBytes: cur.MinBytes, optimize.ParamKeepHead: cur.KeepHead, optimize.ParamKeepTail: cur.KeepTail}
	var out []TuneProposal
	for _, st := range loosenStep {
		p, _ := tuning.ParamFor(opt, st.param)
		from, to := byName[st.param], p.Clamp(st.next(byName[st.param]))
		if to == from {
			continue
		}
		v := float64(to)
		out = append(out, TuneProposal{
			Rec: Recommendation{
				ID: "tune-" + opt + "-" + st.param, Kind: KindTuning, Name: optimize.TuningKey(opt, st.param), Action: ActionTune,
				Reason: fmt.Sprintf("In this repo the agent fetched back %d of %d trimmed outputs (%.0f%%; Trove rolls the trimmer back at %d%%). Changing %s from %d to %d makes the trimmer keep more of each output. It takes effect for new sessions only, and Trove goes back to the default if the fetch-back share does not fall.",
					bad, acted, rate*100, cfg.Measure.RefetchRatePercent, st.param, from, to),
				Confidence: conf, Evidence: EvidenceLocal, Label: "measured", Source: "local", Value: &v,
			},
			Row: store.TuningRow{RepoKey: repoKey, Optimizer: opt, Param: st.param, Value: to, State: store.TuningSuggested,
				TunedAt: now, Source: "local", BeforeRate: rate, NBefore: acted},
		})
	}
	return out, nil
}
