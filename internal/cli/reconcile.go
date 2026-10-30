package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/reconcile"
)

func newReconcile(a *App) *cobra.Command {
	var (
		provider, from, to, export string
		invoice, tolerance         float64
		utc                        bool
	)
	c := &cobra.Command{
		Use:   "reconcile",
		Short: "Compare Trove's recorded total with your provider invoice",
		Long: "Enter the total from your provider invoice for a period and Trove compares it with what it\n" +
			"recorded for the same dates. This checks Trove's accuracy; nothing is sent anywhere.\n\n" +
			"The recorded figure is counted from provider-reported token usage and priced at list prices\n" +
			"(measured tokens, estimated dollars). The invoice figure is the number you type in.\n" +
			"Dates are inclusive, in your local time zone unless you pass --utc.",
		Example: "  trove reconcile --provider anthropic --from 2026-10-01 --to 2026-10-31 --invoice 123.45",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !cmd.Flags().Changed("invoice") {
				return fmt.Errorf("pass the invoice total with --invoice, for example --invoice 123.45")
			}
			if invoice < 0 {
				return fmt.Errorf("--invoice must not be negative")
			}
			if tolerance < 0 {
				return fmt.Errorf("--tolerance must not be negative")
			}
			p := strings.ToLower(provider)
			if p != "anthropic" && p != "openai" {
				return fmt.Errorf("--provider must be anthropic or openai")
			}
			loc := time.Local
			if utc {
				loc = time.UTC
			}
			start, err := parseDay(from, loc, "--from")
			if err != nil {
				return err
			}
			endDay, err := parseDay(to, loc, "--to")
			if err != nil {
				return err
			}
			if endDay.Before(start) {
				return fmt.Errorf("--to (%s) is before --from (%s). Swap them", to, from)
			}
			in := reconcile.Input{Provider: p, From: start, To: endDay.AddDate(0, 0, 1), InvoiceUSD: invoice, TolerancePct: tolerance}

			dir, _, err := loadConfig(a)
			if err != nil {
				return err
			}
			db, err := openExisting(dir)
			if err != nil {
				return err
			}
			var rows []reconcile.ModelRow
			var versions []string
			if db != nil {
				defer func() { _ = db.Close() }()
				if rows, versions, err = reconcile.Query(context.Background(), db.DB, in); err != nil {
					return err
				}
			}
			res := reconcile.Compare(in, rows, versions)
			if export != "" {
				b, err := json.MarshalIndent(res, "", "  ")
				if err != nil {
					return err
				}
				if err := os.WriteFile(export, append(b, '\n'), 0o600); err != nil {
					return fmt.Errorf("cannot write %s: %v. Choose a folder you can write to", export, err)
				}
			}
			return a.emit(res, renderReconcile(res, export))
		},
	}
	f := c.Flags()
	f.StringVar(&provider, "provider", "anthropic", "provider of the invoice: anthropic or openai")
	f.StringVar(&from, "from", "", "first day of the invoice period, such as 2026-10-01")
	f.StringVar(&to, "to", "", "last day of the invoice period (inclusive), such as 2026-10-31")
	f.Float64Var(&invoice, "invoice", 0, "the invoice total in USD")
	f.Float64Var(&tolerance, "tolerance", reconcile.DefaultTolerancePct, "percent difference still counted as a match")
	f.BoolVar(&utc, "utc", false, "read --from and --to as UTC days (use if your provider bills in UTC)")
	f.StringVar(&export, "export", "", "write the result to this file so you can share it; nothing is sent automatically")
	_ = c.MarkFlagRequired("from")
	_ = c.MarkFlagRequired("to")
	return c
}

func parseDay(s string, loc *time.Location, flag string) (time.Time, error) {
	t, err := time.ParseInLocation("2006-01-02", s, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must be a date like 2026-10-01, got %q", flag, s)
	}
	return t, nil
}

func renderReconcile(r reconcile.Result, export string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Reconciliation: %s, %s to %s\n", r.Provider, r.From, lastDay(r.To))
	fmt.Fprintf(&b, "  Trove recorded (estimated from list prices): $%.2f  (%d requests", r.RecordedUSD, r.Requests)
	if r.UnpricedRequests > 0 {
		fmt.Fprintf(&b, ", %d unpriced", r.UnpricedRequests)
	}
	b.WriteString(")\n")
	fmt.Fprintf(&b, "  Your invoice (entered by you):               $%.2f\n", r.InvoiceUSD)
	fmt.Fprintf(&b, "  Difference:                                  $%+.2f (%+.2f%%)\n", r.DiffUSD, r.DiffPct)
	if r.Verdict == reconcile.VerdictMatch {
		fmt.Fprintf(&b, "\nResult: match, within %.1f%%.\n", r.TolerancePct)
	} else {
		fmt.Fprintf(&b, "\nResult: MISMATCH, outside %.1f%%.\n", r.TolerancePct)
	}
	for _, h := range r.Hints {
		fmt.Fprintf(&b, "  - %s\n", h)
	}
	if export != "" {
		fmt.Fprintf(&b, "\nSaved to %s. It holds totals and token counts only. Share it only if you choose to.", export)
	}
	return strings.TrimRight(b.String(), "\n")
}

// lastDay turns the exclusive end date back into the inclusive day shown to the user.
func lastDay(exclusive string) string {
	t, err := time.Parse("2006-01-02", exclusive)
	if err != nil {
		return exclusive
	}
	return t.AddDate(0, 0, -1).Format("2006-01-02")
}
