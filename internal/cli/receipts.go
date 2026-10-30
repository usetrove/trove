package cli

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/report"
)

type receiptsOutput struct {
	Receipts []measure.Receipt `json:"receipts"`
}

func newReceipts(a *App) *cobra.Command {
	var limit int
	c := &cobra.Command{
		Use:   "receipts",
		Short: "List one-line summaries of recent sessions",
		Long: "A receipt is written when a session ends: its cost, model, number of calls, cache-hit rate and what\n" +
			"optimizers saved. The gateway cannot print into your agent's terminal, so receipts show up here and\n" +
			"in `trove status-line`. Turn on desktop notifications with notifications.desktop in config.toml.",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if limit < 1 || limit > 1000 {
				return fmt.Errorf("--limit is %d; use a number from 1 to 1000", limit)
			}
			dir, cfg, err := loadConfig(a)
			if err != nil {
				return err
			}
			out := receiptsOutput{Receipts: []measure.Receipt{}}
			db, err := openExisting(dir)
			if err != nil {
				return err
			}
			if db != nil {
				defer func() { _ = db.Close() }()
				ctx := context.Background()
				// Catch up on sessions that ended while no gateway was sweeping.
				if _, err := measure.SweepReceipts(ctx, db, time.Duration(cfg.Gateway.IdleMinutes)*time.Minute, time.Now()); err != nil {
					return fmt.Errorf("cannot write receipts: %v. Run `trove doctor` for details", err)
				}
				if out.Receipts, err = measure.ListReceipts(ctx, db, limit); err != nil {
					return fmt.Errorf("cannot read receipts: %v. Run `trove doctor` for details", err)
				}
			}
			return a.emit(out, trimNL(report.RenderReceipts(out.Receipts, cfg.Gateway.IdleMinutes, report.Options{Width: a.termWidth()})))
		},
	}
	c.Flags().IntVar(&limit, "limit", 10, "how many recent sessions to list")
	return c
}

// notifier returns a function that shows a desktop notification, or nil where
// Trove has no way to. It never blocks the gateway and ignores failures.
func notifier() func(measure.Receipt) {
	show := notifierSync()
	if show == nil {
		return nil
	}
	return func(r measure.Receipt) { go show(r) }
}

// notifierSync is notifier without the goroutine, for a short-lived process
// (the session-end hook) that would exit before a background one finished.
func notifierSync() func(measure.Receipt) {
	var name string
	var args func(title, body string) []string
	switch runtime.GOOS {
	case "darwin":
		name = "osascript"
		args = func(title, body string) []string {
			return []string{"-e", fmt.Sprintf("display notification %q with title %q", body, title)}
		}
	case "linux":
		name = "notify-send"
		args = func(title, body string) []string { return []string{title, body} }
	default:
		return nil
	}
	if _, err := exec.LookPath(name); err != nil {
		return nil
	}
	return func(r measure.Receipt) {
		body := strings.NewReplacer("\"", "", "\\", "").Replace("Session finished: " + report.ReceiptLine(r))
		_ = exec.Command(name, args("Trove", body)...).Run() //nolint:gosec // fixed program, text built from numbers and model names
	}
}
