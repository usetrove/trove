package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/store"
)

type effortCheckOutput struct {
	From    time.Time                  `json:"from"`
	To      time.Time                  `json:"to"`
	Models  []measure.EffortCacheModel `json:"models"`
	Message string                     `json:"message"`
}

func newOptimizeEffortCheck(a *App) *cobra.Command {
	var days int
	return func() *cobra.Command {
		c := &cobra.Command{
			Use:   "effort-check",
			Short: "Measure, per model, whether changing effort mid-session loses the prompt cache",
			Long: "Reads recorded gateway requests. For consecutive turns of a session less than 4 minutes apart, it\n" +
				"compares how often the cached prefix was read back when the effort level changed against when it did\n" +
				"not. The verdict per model is breaks, keeps, unclear or insufficient (fewer than 5 of each). Lowering\n" +
				"effort on tool turns is only worth a session A/B for a model whose verdict is keeps. Counts come from\n" +
				"provider-reported usage, so they are labeled measured. It changes nothing. See docs/effort-check.md.",
			Args: cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error {
				if days < 1 || days > 3650 {
					return fmt.Errorf("--days is %d; use a number from 1 to 3650", days)
				}
				return optimizeEffortCheck(a, days)
			},
		}
		c.Flags().IntVar(&days, "days", 30, "how many days of recorded requests to read")
		return c
	}()
}

func optimizeEffortCheck(a *App, days int) error {
	dir, err := a.ResolveDataDir()
	if err != nil {
		return err
	}
	to := time.Now().UTC()
	out := effortCheckOutput{From: to.AddDate(0, 0, -days), To: to, Models: []measure.EffortCacheModel{}}
	if _, err := os.Stat(filepath.Join(dir, store.DBFileName)); err == nil {
		db, err := store.Open(dir)
		if err != nil {
			return fmt.Errorf("cannot open Trove's database: %v. Run `trove doctor` for details", err)
		}
		defer func() { _ = db.Close() }()
		if out.Models, err = measure.EffortCacheCheck(context.Background(), db, out.From, to.Add(time.Minute)); err != nil {
			return fmt.Errorf("cannot read recorded usage: %v. Run `trove doctor` for details", err)
		}
	}
	if len(out.Models) == 0 {
		out.Message = "No turns with a cached prefix were recorded yet. Run API-key sessions through `trove gateway`, some with a changed effort level, then run this again (docs/effort-check.md)."
	}
	return a.emit(out, renderEffortCheck(out))
}

func renderEffortCheck(o effortCheckOutput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Effort cache-break check, %s to %s\n", o.From.Format("2006-01-02"), o.To.Format("2006-01-02"))
	for _, m := range o.Models {
		fmt.Fprintf(&b, "  %-10s %s\n", m.Verdict, m.Text)
	}
	if o.Message != "" {
		b.WriteString(o.Message + "\n")
	}
	return b.String()
}
