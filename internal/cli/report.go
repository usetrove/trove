package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/report"
	"github.com/usetrove/trove/internal/store"
)

// periodFlags are the --today/--week/--month/--since selectors.
type periodFlags struct {
	today, week, month bool
	since              string
}

func (f *periodFlags) add(c *cobra.Command) {
	c.Flags().BoolVar(&f.today, "today", false, "report on today only")
	c.Flags().BoolVar(&f.week, "week", false, "report on this week (since Monday)")
	c.Flags().BoolVar(&f.month, "month", false, "report on this month (since the 1st)")
	c.Flags().StringVar(&f.since, "since", "", "report since a date, such as 2026-10-01")
}

// resolve picks the period. With none chosen it uses the budget period when a
// cap is set (so the report and the budget cover the same days), else the month.
func (f periodFlags) resolve(cfg config.Config, now time.Time) (report.Period, error) {
	chosen := 0
	name := ""
	for n, on := range map[string]bool{report.Today: f.today, report.Week: f.week, report.Month: f.month, report.Since: f.since != ""} {
		if on {
			chosen++
			name = n
		}
	}
	if chosen > 1 {
		return report.Period{}, fmt.Errorf("choose one of --today, --week, --month or --since, not several")
	}
	if chosen == 0 {
		name = report.Month
		if cfg.Budget.CapUSD > 0 {
			name = report.PeriodName(cfg.Budget.Period)
		}
	}
	return report.NewPeriod(name, f.since, now)
}

func newReport(a *App) *cobra.Command {
	var pf periodFlags
	c := &cobra.Command{
		Use:   "report",
		Short: "Show token usage, waste and savings",
		Long: "Shows what you spent, the waste Trove found, what optimizers saved and what to do next.\n" +
			"Every number is labeled \"measured\" (counted from provider-reported usage) or \"estimated\"\n" +
			"(derived from list prices or an assumption). Dollar figures are estimates, not invoices.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return runReport(a, pf) },
	}
	pf.add(c)
	return c
}

func runReport(a *App, pf periodFlags) error {
	r, err := buildReport(a, pf)
	if err != nil {
		return err
	}
	return a.emit(r, trimNL(report.Render(r, report.Options{Width: a.termWidth(), Color: a.Color()})))
}

// buildReport reads the data directory and builds the report for the chosen
// period. A data directory with no database gives the empty report and creates
// nothing.
func buildReport(a *App, pf periodFlags) (report.Report, error) {
	dir, cfg, err := loadConfig(a)
	if err != nil {
		return report.Report{}, err
	}
	now := time.Now()
	p, err := pf.resolve(cfg, now)
	if err != nil {
		return report.Report{}, err
	}
	// Read the agent's own session history first, so the report works without
	// the gateway. This fails open: a problem here never stops the report.
	var planEvidence []string
	if cfg.History.Enabled && !a.NoHistory {
		res, err := scanHistory(context.Background(), a, dir, cfg, false, true)
		if err != nil {
			_, _ = fmt.Fprintf(a.Err, "Could not read your agent history (%v). Showing what Trove already has. Run `trove logs status` for details.\n", err)
		}
		planEvidence = append(append([]string{}, res.Plan.Evidence...), res.Codex.Evidence...)
	}
	db, err := openExisting(dir)
	if err != nil {
		return report.Report{}, err
	}
	if db == nil {
		return report.EmptyReport(p), nil
	}
	defer func() { _ = db.Close() }()
	on, _ := optimize.Disabled(optimize.KillFile(dir), a.Getenv)
	tbl, perr := pricing.Default()
	if perr != nil {
		tbl = nil // suggestions that need prices are skipped
	}
	r, err := report.Build(context.Background(), db, report.Inputs{Config: cfg, Now: now, Prices: tbl, KillSwitch: on, HistoryPlanEvidence: planEvidence}, p)
	if err != nil {
		return r, fmt.Errorf("cannot read recorded usage: %v. Run `trove doctor` for details", err)
	}
	return r, nil
}

// openExisting opens the database if there is one. It returns nil, nil when the
// data directory has none yet, so read-only commands never create files.
func openExisting(dir string) (*store.DB, error) {
	if _, err := os.Stat(filepath.Join(dir, store.DBFileName)); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("cannot read %s: %v. Check its permissions", dir, err)
	}
	db, err := store.Open(dir)
	if err != nil {
		return nil, fmt.Errorf("cannot open Trove's database: %v. Run `trove doctor` for details", err)
	}
	return db, nil
}

func trimNL(s string) string {
	for len(s) > 0 && s[len(s)-1] == '\n' {
		s = s[:len(s)-1]
	}
	return s
}
