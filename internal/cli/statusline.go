package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/measure/limitfit"
	"github.com/usetrove/trove/internal/regress"
	"github.com/usetrove/trove/internal/report"
	"github.com/usetrove/trove/internal/store"
)

// statusOutput is the --json shape. Line is the text the other formats print.
type statusOutput struct {
	Line   string         `json:"line"`
	Status *report.Status `json:"status,omitempty"`
	// PlanUsage is Claude Code's subscription meter from the status line input (claude format only).
	PlanUsage *report.PlanUsage `json:"plan_usage,omitempty"`
	// LimitSample says what happened to Claude Code's plan-usage meter: written, unchanged, or why it
	// was skipped. Empty when the input carried no meter.
	LimitSample string `json:"limit_sample,omitempty"`
	Error       string `json:"error,omitempty"`
}

func newStatusLine(a *App) *cobra.Command {
	var format string
	c := &cobra.Command{
		Use:   "status-line",
		Short: "Print a one-line status for agent status bars",
		Long: "Prints spend, budget pace, the current session's cost and savings on one line, for example:\n" +
			"  ~$612 / $1,500 · 41% used · 48% of month · on pace · session ~$3.20 · saved ~$47\n" +
			"\"~\" marks an estimate from list prices. It reads a small summary the gateway keeps current, so it\n" +
			"returns in a few milliseconds. Formats: claude (Claude Code's statusLine setting), tmux, shell.\n" +
			"With claude, it also shows Claude Code's own plan usage when Claude Code sends it (\"5h 23% · wk 41%\")\n" +
			"and records each change locally; it never calls a usage endpoint.\n\n" +
			"Claude Code:  settings.json -> \"statusLine\": {\"type\": \"command\", \"command\": \"trove status-line --format claude\"}\n" +
			"tmux:         set -g status-right '#(trove status-line --format tmux)'\n" +
			"bash/zsh:     add $(trove status-line) to your prompt",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			switch format {
			case report.FormatClaude, report.FormatTmux, report.FormatShell:
			default:
				return fmt.Errorf("--format is %q; use claude, tmux or shell", format)
			}
			return runStatusLine(a, format)
		},
	}
	c.Flags().StringVar(&format, "format", report.FormatShell, "claude, tmux or shell")
	return c
}

// runStatusLine never fails the agent's status bar: any problem becomes a short
// line telling the user what to run, with exit code 0.
func runStatusLine(a *App, format string) error {
	in, now := readClaudeInput(a, format), time.Now()
	out := statusLine(a, format, in, now)
	// Claude Code's plan-usage meter, shown whatever state Trove itself is in: it is the agent's number.
	if u := report.CurrentPlanUsage(in.planUsage(), now); u != nil {
		out.PlanUsage = u
		out.Line += " · " + report.RenderPlanUsage(*u)
	}
	return a.emit(out, out.Line)
}

func statusLine(a *App, format string, in claudeInput, now time.Time) statusOutput {
	dir, cfg, err := loadConfig(a)
	if err != nil {
		return statusOutput{Line: "Trove: settings problem, run `trove doctor`", Error: err.Error()}
	}
	db, err := store.OpenReadOnly(dir)
	if err != nil {
		// The meter is Claude Code's, not Trove's, so it is recorded even before Trove has a database.
		if os.IsNotExist(err) {
			return statusOutput{Line: "Trove: no usage yet", LimitSample: recordLimitSample(nil, dir, in, now)}
		}
		return statusOutput{Line: "Trove: database problem, run `trove doctor`", Error: err.Error(), LimitSample: recordLimitSample(nil, dir, in, now)}
	}
	defer func() { _ = db.Close() }()

	status, err := report.BuildStatus(context.Background(), db, cfg, now, in.SessionID)
	if err != nil {
		return statusOutput{Line: "Trove: database problem, run `trove doctor`", Error: err.Error(), LimitSample: recordLimitSample(db, dir, in, now)}
	}
	if status.Saved.Value >= 0.01 {
		// Savings as a share of the 5-hour limit, from the saved API-price scales (one tiny file read;
		// no fit means no percentage).
		status.SavedLimit = limitfit.ReadScales(dir).PercentUSD(status.Saved.Value)
	}
	color := format != report.FormatShell && !a.NoColor && a.Getenv("NO_COLOR") == ""
	line := report.RenderStatus(status, format, color) + autopilotSuffix(dir) + regressSuffix(dir, now)
	return statusOutput{Line: line, Status: &status, LimitSample: recordLimitSample(db, dir, in, now)}
}

// regressSuffix is the regression watch's strongest alert, in one sentence, from the saved result (one small
// file read; the watch is recomputed at session end and by `trove report`). Empty when there is none.
func regressSuffix(dir string, now time.Time) string {
	if s := regress.StatusSentence(regress.ReadCache(dir), now); s != "" {
		return " · " + s
	}
	return ""
}
