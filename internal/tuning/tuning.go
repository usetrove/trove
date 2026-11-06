// Package tuning keeps optimizer parameters tuned per repo, on the device.
//
// A tuned value is a number for one parameter of one optimizer in one repo
// (identified by a salted hash of the working directory, never a path). Values
// are proposed from local results (internal/recommend), shown to the user, put in
// effect only by `trove tune apply`, and reverted to the shipped defaults when a
// review finds they did not help. They stay local unless the user opts in to share
// them as metadata.
//
// Cache safety (CLAUDE.md rule 5): an adapter reads the applied values once, when
// a session starts, and pins them; a session never sees a change. The optimizer
// side clamps every value to a safe range (optimize.TuneParam), so an edited
// database cannot make an optimizer unsafe. See docs/recommend.md.
package tuning

import (
	"context"
	"fmt"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/store"
)

// Window is how far back the signals look.
const Window = 90 * 24 * time.Hour

// Rates counts, for one repo and one optimizer, the actions the hooks took and
// how many of them did not help, between from and to. For the output trimmer: of
// the outputs it trimmed, how many the agent fetched back. They are the hooks' own
// counts ("measured"). Only sessions whose features were recorded for the repo
// count, because that is what ties a session to a repo.
func Rates(ctx context.Context, db *store.DB, repoKey, optimizer string, from, to time.Time) (acted, bad int, err error) {
	err = db.QueryRowContext(ctx, `SELECT COALESCE(SUM(he.kind = 'rewritten'), 0), COALESCE(SUM(he.kind = 'fetched_back'), 0)
		FROM hook_events he JOIN session_features sf ON sf.session_key = he.session_key
		WHERE sf.repo_key = ? AND he.hook = ? AND he.ts >= ? AND he.ts < ?`,
		repoKey, optimizer, from.UnixMilli(), to.UnixMilli()).Scan(&acted, &bad)
	return acted, bad, err
}

// params returns the tunable parameters of an optimizer.
func params(optimizer string) map[string]optimize.TuneParam {
	out := map[string]optimize.TuneParam{}
	if optimizer == config.OptimizerOutputTrimmer {
		for _, p := range optimize.TrimParams {
			out[p.Name] = p
		}
	}
	return out
}

// Applied returns the values in effect, by repo key, as session tuning keys
// ("output_trimmer.keep_tail"). Values are clamped to their ranges; rows naming
// an unknown optimizer or parameter are dropped.
func Applied(ctx context.Context, db *store.DB) (map[string]optimize.Tuning, error) {
	rows, err := db.Tuning().List(ctx, "")
	if err != nil {
		return nil, err
	}
	out := map[string]optimize.Tuning{}
	for _, r := range rows {
		if r.State != store.TuningApplied {
			continue
		}
		p, ok := params(r.Optimizer)[r.Param]
		if !ok {
			continue
		}
		if out[r.RepoKey] == nil {
			out[r.RepoKey] = optimize.Tuning{}
		}
		out[r.RepoKey][optimize.TuningKey(r.Optimizer, r.Param)] = p.Clamp(r.Value)
	}
	return out, nil
}

// ParamFor looks up a tunable parameter by optimizer and name.
func ParamFor(optimizer, param string) (optimize.TuneParam, bool) {
	p, ok := params(optimizer)[param]
	return p, ok
}

// Reverted describes one tuning undone by Review.
type Reverted struct {
	RepoKey   string  `json:"repo"`
	Optimizer string  `json:"optimizer"`
	Before    float64 `json:"rate_before"`
	After     float64 `json:"rate_after"`
	N         int     `json:"n_after"`
	Reason    string  `json:"reason"`
}

// Review reverts applied tuning that did not help (the guardrail for tuning, rule
// 6). For each repo with applied values it compares the share of trimmed outputs
// fetched back since the values were applied with the share before; once at least
// m.MinSessions outputs have been trimmed since, it reverts to the shipped
// defaults if the share did not fall, or if it reached the guardrail limit. It is
// safe to call repeatedly. Reverting only affects sessions that start later.
func Review(ctx context.Context, db *store.DB, m config.Measure, now time.Time) ([]Reverted, error) {
	rows, err := db.Tuning().List(ctx, "")
	if err != nil {
		return nil, err
	}
	type key struct{ repo, opt string }
	seen := map[key]bool{}
	var out []Reverted
	for _, r := range rows {
		k := key{r.RepoKey, r.Optimizer}
		if r.State != store.TuningApplied || seen[k] {
			continue
		}
		seen[k] = true
		acted, bad, err := Rates(ctx, db, r.RepoKey, r.Optimizer, r.TunedAt, now.Add(time.Minute))
		if err != nil {
			return out, err
		}
		if acted < m.MinSessions || acted == 0 {
			continue
		}
		after := float64(bad) / float64(acted)
		limit := float64(m.RefetchRatePercent) / 100
		var why string
		switch {
		case after >= limit:
			why = fmt.Sprintf("since the tuned values were applied the agent fetched back %.0f%% of trimmed outputs, at or above the %d%% limit", after*100, m.RefetchRatePercent)
		case after >= r.BeforeRate:
			why = fmt.Sprintf("the share of trimmed outputs the agent fetched back did not fall (%.0f%% before, %.0f%% since)", r.BeforeRate*100, after*100)
		default:
			continue
		}
		if err := db.Tuning().Revert(ctx, r.RepoKey, r.Optimizer); err != nil {
			return out, err
		}
		out = append(out, Reverted{RepoKey: r.RepoKey, Optimizer: r.Optimizer, Before: r.BeforeRate, After: after, N: acted,
			Reason: "Trove went back to the default " + r.Optimizer + " settings for this repo: " + why + ". This is measured from the hook's own counts."})
	}
	return out, nil
}
