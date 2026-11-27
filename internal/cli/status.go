package cli

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/autopilot"
	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/measure/limitfit"
	"github.com/usetrove/trove/internal/store"
)

// troveStatusJSON is `trove status --json`.
type troveStatusJSON struct {
	Autopilot bool   `json:"autopilot_on"`
	Policy    string `json:"policy,omitempty"`
	// View is what the optimize policy turned on, measured and rolled back. Nil without that policy.
	View *autopilot.PolicyView `json:"optimize_policy,omitempty"`
}

func newStatus(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show what the optimize policy turned on, what it measured and what it rolled back",
		Args:  cobra.NoArgs,
		RunE:  func(*cobra.Command, []string) error { return runStatus(a) },
	}
}

func runStatus(a *App) error {
	dir, err := a.ResolveDataDir()
	if err != nil {
		return err
	}
	f, err := config.LoadAutopilot(dir)
	if err != nil {
		return err
	}
	st := troveStatusJSON{Autopilot: f.Active(), Policy: f.Policy}
	st.View = policyView(dir, f)
	switch {
	case st.View != nil:
		return a.emit(st, st.View.Text()+"\n\nMore detail: `trove autopilot` (states and planner advice) and `trove report` (spend and savings).")
	case f.Enabled:
		return a.emit(st, "Autopilot is on with the "+f.Policy+" policy. See it with `trove autopilot`; `trove autopilot on` offers the optimize policy.")
	}
	return a.emit(st, "The optimize policy is off. Turn it on with `trove autopilot on` (or `trove setup`).")
}

// policyView reads the optimize policy's view, with figures when a database exists. Nil without that policy; a read
// problem only drops the figures, so neither `trove status` nor the report fails for it.
func policyView(dir string, f config.AutopilotFile) *autopilot.PolicyView {
	if !f.Enabled || !f.IsOptimize() {
		return nil
	}
	ctx := context.Background()
	now := time.Now()
	var db *store.DB
	gateway := false
	if _, err := os.Stat(filepath.Join(dir, store.DBFileName)); err == nil {
		if d, err := store.Open(dir); err == nil {
			defer func() { _ = d.Close() }()
			db = d
			if tr, err := measure.TrafficSeen(ctx, db, measure.Window{From: now.AddDate(0, 0, -autopilot.PolicyViewDays), To: now}); err == nil {
				gateway = tr.Gateway
			}
		}
	}
	return autopilot.BuildPolicyView(ctx, db, f, gateway, limitfit.ReadCache(dir), now)
}

// optimizePolicyView is the view for `trove report`. Nil unless the user agreed to the optimize policy.
func optimizePolicyView(a *App) *autopilot.PolicyView {
	dir, err := a.ResolveDataDir()
	if err != nil {
		return nil
	}
	f, err := config.LoadAutopilot(dir)
	if err != nil {
		return nil
	}
	return policyView(dir, f)
}
