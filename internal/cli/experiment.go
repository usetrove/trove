package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/experiment"
	"github.com/usetrove/trove/internal/store"
)

type experimentStatusOut struct {
	Enabled  bool                `json:"enabled"`
	Note     string              `json:"note"`
	Results  []experiment.Result `json:"results"`
	ConfigAt string              `json:"config_file"`
}

func newExperiment(a *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "experiment",
		Short: "See the session-level A/B comparison of your optimizers",
		Long: "For optimizers you set to \"on\", Trove alternates whole sessions between on and shadow and compares\n" +
			"your real cost and outcomes. Sessions are never switched mid-way. To opt out, set enabled = false under\n" +
			"[experiment] in config.toml; you then get shadow estimates only.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error { return c.Help() },
	}
	cmd.AddCommand(&cobra.Command{Use: "status", Short: "Show A/B results with their ranges and sample sizes", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error { return experimentStatusCmd(c.Context(), a) }})
	return cmd
}

func experimentStatusCmd(ctx context.Context, a *App) error {
	dir, cfg, err := loadConfig(a)
	if err != nil {
		return err
	}
	// A fresh install has no database yet. That means no A/B results, not an error
	// (report and status-line behave the same way).
	var res []experiment.Result
	db, err := store.OpenReadOnly(dir)
	switch {
	case os.IsNotExist(err):
	case err != nil:
		return err
	default:
		defer func() { _ = db.Close() }()
		res, err = experiment.Evaluate(ctx, db, cfg.Experiment, experiment.DefaultPriors())
		if err != nil {
			return fmt.Errorf("could not read A/B results: %v. Run `trove doctor` or check %s", err, dir)
		}
	}
	out := experimentStatusOut{Enabled: cfg.Experiment.Enabled, Results: res, ConfigAt: filepath.Join(dir, config.FileName)}
	if res == nil {
		out.Results = []experiment.Result{}
	}
	var b strings.Builder
	if !cfg.Experiment.Enabled {
		out.Note = "A/B comparison is off (you opted out). You get shadow estimates only; they " + experiment.ShadowCaveat + "."
	} else {
		out.Note = "A/B comparison is on for optimizers set to \"on\". To opt out, set enabled = false under [experiment] in " + out.ConfigAt + "."
	}
	b.WriteString(out.Note + "\n")
	if len(res) == 0 {
		b.WriteString("No optimizer has been in an experiment yet.\n")
	}
	for _, r := range res {
		fmt.Fprintf(&b, "\n%s: %s\n", r.Optimizer, r.Status)
		fmt.Fprintf(&b, "  %s\n", r.Reason)
		if r.Cost != nil {
			c := r.Cost
			fmt.Fprintf(&b, "  cost per session: %s %+.1f%% (95%% range %+.1f%% to %+.1f%%), n=%d on / %d off, confidence %s\n",
				c.Label, c.Value*100, c.Low*100, c.High*100, c.NOn, c.NOff, c.Confidence)
		}
	}
	return a.emit(out, b.String())
}
