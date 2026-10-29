package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/report"
)

// budgetOutput is the --json shape of budget commands. Budget is null when no cap is set.
type budgetOutput struct {
	Message string         `json:"message,omitempty"`
	Budget  *report.Pacing `json:"budget"`
}

func newBudget(a *App) *cobra.Command {
	c := &cobra.Command{
		Use:   "budget",
		Short: "Show or set a spending budget",
		Long: "A budget is a cap for a day, week or month. Trove shows how much is used against how much of the\n" +
			"period has passed, and when you would reach the cap at the current rate. It only informs: it never\n" +
			"blocks a request. Spend is estimated from list prices, so it can differ from your invoice.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return budgetShow(a) },
	}
	var period string
	set := &cobra.Command{
		Use:   "set <amount>",
		Short: "Set the cap in US dollars (0 removes it)",
		Example: "  trove budget set 1500 --period month\n" +
			"  trove budget set 0",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error { return budgetSet(a, args[0], period) },
	}
	set.Flags().StringVar(&period, "period", "", "day, week or month (default: keep the current period)")
	c.AddCommand(set, &cobra.Command{
		Use: "show", Short: "Show spend against the budget", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return budgetShow(a) },
	})
	return c
}

func budgetSet(a *App, amount, period string) error {
	dir, cfg, err := loadConfig(a)
	if err != nil {
		return err
	}
	v, err := strconv.ParseFloat(strings.TrimPrefix(strings.ReplaceAll(amount, ",", ""), "$"), 64)
	if err != nil || v < 0 {
		return fmt.Errorf("%q is not a budget amount; use a number of US dollars such as 1500, or 0 to remove the cap", amount)
	}
	per := cfg.Budget.Period
	if period != "" {
		p, ok := report.BudgetPeriod(period)
		if !ok {
			return fmt.Errorf("--period is %q; use day, week or month", period)
		}
		per = p
	}
	if err := config.SetBudget(dir, per, v); err != nil {
		return err
	}
	msg := fmt.Sprintf("Budget set to %s per %s.", report.FormatUSD(v), strings.TrimSuffix(per, "ly"))
	if per == "daily" {
		msg = fmt.Sprintf("Budget set to %s per day.", report.FormatUSD(v))
	}
	if v == 0 {
		msg = "Budget removed. Trove no longer tracks a cap."
	}
	cfg.Budget.CapUSD, cfg.Budget.Period = v, per
	p, err := pacingFor(a, dir, cfg)
	if err != nil {
		return err
	}
	text := msg
	if p != nil {
		text += "\n\n" + trimNL(report.RenderPacing(p, report.Options{Width: a.termWidth(), Color: a.Color()}))
	}
	return a.emit(budgetOutput{Message: msg, Budget: p}, text)
}

func budgetShow(a *App) error {
	dir, cfg, err := loadConfig(a)
	if err != nil {
		return err
	}
	p, err := pacingFor(a, dir, cfg)
	if err != nil {
		return err
	}
	return a.emit(budgetOutput{Budget: p}, trimNL(report.RenderPacing(p, report.Options{Width: a.termWidth(), Color: a.Color()})))
}

// pacingFor computes pacing from the database, or from zero spend when nothing
// has been recorded yet. It is nil when no cap is set.
func pacingFor(_ *App, dir string, cfg config.Config) (*report.Pacing, error) {
	if cfg.Budget.CapUSD <= 0 {
		return nil, nil
	}
	now := time.Now()
	db, err := openExisting(dir)
	if err != nil {
		return nil, err
	}
	if db == nil {
		name := report.PeriodName(cfg.Budget.Period)
		from := report.StartOf(name, now)
		none := measure.Figure{Label: measure.Estimated}
		return report.Pace(cfg.Budget, name, none, from, report.EndOf(name, from), now), nil
	}
	defer func() { _ = db.Close() }()
	p, err := report.BuildPacing(context.Background(), db, cfg, now)
	if err != nil {
		return nil, fmt.Errorf("cannot read recorded usage: %v. Run `trove doctor` for details", err)
	}
	return p, nil
}
