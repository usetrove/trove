package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/report"
	"github.com/usetrove/trove/internal/store"
)

// statusOutput is the --json shape. Line is the text the other formats print.
type statusOutput struct {
	Line   string         `json:"line"`
	Status *report.Status `json:"status,omitempty"`
	Error  string         `json:"error,omitempty"`
}

func newStatusLine(a *App) *cobra.Command {
	var format string
	c := &cobra.Command{
		Use:   "status-line",
		Short: "Print a one-line status for agent status bars",
		Long: "Prints spend, budget pace, the current session's cost and savings on one line, for example:\n" +
			"  ~$612 / $1,500 · 41% used · 48% of month · on pace · session ~$3.20 · saved ~$47\n" +
			"\"~\" marks an estimate from list prices. It reads a small summary the gateway keeps current, so it\n" +
			"returns in a few milliseconds. Formats: claude (Claude Code's statusLine setting), tmux, shell.\n\n" +
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
	line, st, problem := statusLine(a, format)
	out := statusOutput{Line: line, Status: st, Error: problem}
	return a.emit(out, line)
}

func statusLine(a *App, format string) (line string, st *report.Status, problem string) {
	dir, cfg, err := loadConfig(a)
	if err != nil {
		return "Trove: settings problem, run `trove doctor`", nil, err.Error()
	}
	db, err := store.OpenReadOnly(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "Trove: no usage yet", nil, ""
		}
		return "Trove: database problem, run `trove doctor`", nil, err.Error()
	}
	defer func() { _ = db.Close() }()

	status, err := report.BuildStatus(context.Background(), db, cfg, time.Now(), claudeSessionID(a, format))
	if err != nil {
		return "Trove: database problem, run `trove doctor`", nil, err.Error()
	}
	color := format != report.FormatShell && !a.NoColor && a.Getenv("NO_COLOR") == ""
	return report.RenderStatus(status, format, color), &status, ""
}

// claudeSessionID reads the session id Claude Code sends as JSON on stdin. It
// reads only when stdin is a pipe or file, never a terminal, so running the
// command by hand does not wait for input.
func claudeSessionID(a *App, format string) string {
	if format != report.FormatClaude || a.In == nil {
		return ""
	}
	if f, ok := a.In.(*os.File); ok {
		if fi, err := f.Stat(); err != nil || fi.Mode()&os.ModeCharDevice != 0 {
			return ""
		}
	}
	var in struct {
		SessionID string `json:"session_id"`
	}
	if json.NewDecoder(io.LimitReader(a.In, 1<<20)).Decode(&in) != nil {
		return ""
	}
	return in.SessionID
}
