package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/usetrove/trove/internal/mcp"
	"github.com/usetrove/trove/internal/mcp/install"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/hooks"
	"github.com/usetrove/trove/internal/measure/limitfit"
	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/regress"
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
	c.AddCommand(newReportClaims(a))
	return c
}

// newReportClaims is `trove report claims`: each token-saving tool's own claim next to what Trove measured in a
// randomized session test on this machine, Trove's own optimizers included.
func newReportClaims(a *App) *cobra.Command {
	var markdown bool
	c := &cobra.Command{
		Use:   "claims",
		Short: "Show each tool's claim next to its measured effect on your weekly limit",
		Long: "Lists every tool and optimizer that has been in a randomized session test on this machine: its own claim\n" +
			"(quoted, not checked), the measured change in tokens per session, the estimated change in your weekly\n" +
			"limit, the pass-rate change, the sample size and the dates. Rows with no clear effect are kept.\n" +
			"--markdown prints a table ready to publish. Method: docs/benchmark-methodology.md section 11.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if markdown && a.JSON {
				return fmt.Errorf("choose one of --json and --markdown")
			}
			dir, cfg, err := loadConfig(a)
			if err != nil {
				return err
			}
			r, err := buildReport(a, periodFlags{})
			if err != nil {
				return err
			}
			db, err := openExisting(dir)
			if err != nil {
				return err
			}
			var c report.Claims
			if db == nil {
				c, err = report.BuildClaims(context.Background(), nil, report.Inputs{Config: cfg, Now: time.Now()}, r)
			} else {
				defer func() { _ = db.Close() }()
				c, err = report.BuildClaims(context.Background(), db, report.Inputs{Config: cfg, Now: time.Now(), LimitFit: limitfit.ReadCache(dir)}, r)
			}
			if err != nil {
				return fmt.Errorf("cannot read the trials: %v. Run `trove doctor` for details", err)
			}
			if markdown {
				_, err = fmt.Fprint(a.Out, report.RenderClaimsMarkdown(c))
				return err
			}
			return a.emit(c, trimNL(report.RenderClaimsText(c, a.termWidth())))
		},
	}
	c.Flags().BoolVar(&markdown, "markdown", false, "print the table as Markdown, ready to publish")
	return c
}

func runReport(a *App, pf periodFlags) error {
	r, err := buildReport(a, pf)
	if err != nil {
		return err
	}
	r.Autopilot = autopilotSummary(a)
	r.OptimizePolicy = optimizePolicyView(a)
	text := trimNL(report.Render(r, report.Options{Width: a.termWidth(), Color: a.Color()}))
	if ap := r.Autopilot.Text(); ap != "" {
		text += "\n\n" + ap
	}
	if op := r.OptimizePolicy.Text(); op != "" {
		text += "\n\n" + op
	}
	err = a.emit(r, text)
	// Only the default period is shared, so every upload covers the same span.
	// This does nothing unless the user ran `trove contribute enable`.
	if err == nil && pf == (periodFlags{}) {
		autoContribute(a, r)
	}
	return err
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
	// The status line's plan-usage samples, also for users who run the status line without the hooks.
	// Best effort: a problem here must not stop the report.
	_, _ = db.IngestLimitSpool(context.Background(), dir)
	// The plan-limit fit, recomputed only when a newer meter reading arrived (best effort, fails open).
	limitFit := limitfit.Refresh(context.Background(), db, dir, now)
	if _, serr := os.Stat(filepath.Join(dir, "hooks")); serr == nil {
		// Bring the hooks' counters and guardrail up to date first. Best effort:
		// a problem here must not stop the report.
		if merr := hooks.Maintain(context.Background(), db, dir, cfg, a.Getenv, now); merr != nil {
			_, _ = fmt.Fprintf(a.Err, "Could not refresh the hook counts (%v). Run `trove hooks sync` for details.\n", merr)
		}
	}
	on, _ := optimize.Disabled(optimize.KillFile(dir), a.Getenv)
	tbl, perr := pricing.Default()
	if perr != nil {
		tbl = nil // suggestions that need prices are skipped
	}
	// The regression watch, on the history just read (best effort; advice only, it changes nothing).
	regressions := regress.Refresh(context.Background(), db, dir, tbl, now, true)
	registered := install.Registered(dir)
	schemaTokens := 0
	if registered {
		// Sized through a real in-memory MCP session; a failure only drops the figure.
		if sz, serr := mcp.MeasureSchema(context.Background(), mcp.Options{Version: Version}); serr == nil {
			schemaTokens = sz.EstimatedTokens
		}
	}
	r, err := report.Build(context.Background(), db, report.Inputs{Config: cfg, Now: now, Prices: tbl, KillSwitch: on, HistoryPlanEvidence: planEvidence, Recoverable: registered, SchemaTokens: schemaTokens,
		RepoKey: repoKeyFor(context.Background(), db, ""), LimitFit: limitFit, Regressions: regressions, ClaudeModelSetting: claudeModelSetting(a)}, p)
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

// claudeModelSetting reads `model` from the user's Claude Code settings (~/.claude/settings.json, or
// $CLAUDE_CONFIG_DIR/settings.json), read-only. "" when it is unset or anything goes wrong: only the opusplan finding
// depends on it, and that finding is then left out.
func claudeModelSetting(a *App) string {
	dir := a.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, err := a.HomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".claude")
	}
	b, err := os.ReadFile(filepath.Join(dir, "settings.json")) //nolint:gosec // the user's own Claude Code settings
	if err != nil {
		return ""
	}
	var s struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(b, &s) != nil {
		return ""
	}
	return s.Model
}
