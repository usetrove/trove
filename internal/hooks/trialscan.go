package hooks

import (
	"context"
	"os"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/histscan"
	"github.com/usetrove/trove/internal/logs"
	"github.com/usetrove/trove/internal/logs/cchistory"
	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/store"
)

// trialScanBudget caps how long a session end spends reading history for a trial. SessionEnd has about two and
// a half seconds in all and the receipt is written after this, so the scan gets a small slice and gives up
// quietly: the files it did not finish are read next time, or by the next `trove` command.
const trialScanBudget = 700 * time.Millisecond

// scanTrialHistory reads the agent's local history, but only while an optimizer that runs in the hooks is on
// trial. A user with no gateway has no priced sessions, so the trial's comparison is built from this history
// (see experiment.AnalyzeHookTrial). It runs at a session boundary, never in the agent's way, and reads only
// files changed since the earliest trial began. It uses the same reader as `trove logs scan`, so the rows it
// writes are priced and annotated exactly as that command would have. Best effort: an error or a timeout
// leaves nothing worse than a comparison that has less data.
func scanTrialHistory(ctx context.Context, db *store.DB, dir string, cfg config.Config, getenv func(string) string) {
	f, err := config.LoadAutopilot(dir)
	if err != nil || !f.Active() || !cfg.Experiment.Enabled || !cfg.History.Enabled {
		return
	}
	since := int64(0)
	for _, n := range hookTrials(cfg.WithAutopilot(f)) {
		if at := f.Entries[n].SinceMS; since == 0 || at < since {
			since = at
		}
	}
	if since == 0 {
		return // nothing in the hooks is on trial
	}
	if getenv == nil {
		getenv = os.Getenv
	}
	src := &cchistory.Source{Getenv: getenv, Home: os.UserHomeDir}
	tbl, perr := pricing.Default()
	if perr != nil {
		tbl = nil
	}
	plan := logs.DetectClaudeCodePlan(getenv, src.ConfigDir(), cfg.History.Plan).Plan
	in := histscan.New(db, tbl, src, nil, plan, logs.PlanUnknown)
	ctx, cancel := context.WithTimeout(ctx, trialScanBudget)
	defer cancel()
	_, _ = in.Scan(ctx, logs.Options{Since: time.UnixMilli(since).Add(-24 * time.Hour)})
}
