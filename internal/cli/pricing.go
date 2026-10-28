package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/pricing"
)

type pricingShow struct {
	DataVersion string          `json:"pricing_data_version"`
	Basis       string          `json:"basis"`
	Notes       []string        `json:"notes"`
	Entries     []pricing.Entry `json:"entries"`
}

func newPricing(a *App) *cobra.Command {
	cmd := &cobra.Command{Use: "pricing", Short: "Inspect the bundled price data", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error { return c.Help() }}
	var provider string
	show := &cobra.Command{
		Use:   "show",
		Short: "List bundled prices with the date each was last verified",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			t, err := pricing.Default()
			if err != nil {
				return fmt.Errorf("the bundled price data is invalid (%w); reinstall Trove or report this bug", err)
			}
			var entries []pricing.Entry
			for _, e := range t.Entries() {
				if provider == "" || e.Provider == strings.ToLower(provider) {
					entries = append(entries, e)
				}
			}
			if len(entries) == 0 {
				return fmt.Errorf("no prices for provider %q; use anthropic, openai or google", provider)
			}
			out := pricingShow{DataVersion: t.Version(), Basis: "estimated: published list prices, USD per million tokens", Notes: t.Notes(), Entries: entries}
			return a.emit(out, renderPricing(out))
		},
	}
	show.Flags().StringVar(&provider, "provider", "", "only show one provider: anthropic, openai or google")
	cmd.AddCommand(show)
	return cmd
}

func renderPricing(p pricingShow) string {
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "PROVIDER\tMODEL\tFROM\tINPUT\tOUTPUT\tCACHE READ\tCACHE WRITE\tVERIFIED")
	for _, e := range p.Entries {
		from := "unknown"
		if e.EffectiveFrom != nil {
			from = e.EffectiveFrom.String()
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", e.Provider, e.Model, from,
			e.Standard.Input, e.Standard.Output, optional(e.Standard.CacheRead), writes(e.Standard), e.VerifiedAt)
	}
	_ = w.Flush()
	fmt.Fprintf(&b, "\nPrices are %s. Data version %s.\n", p.Basis, p.DataVersion)
	fmt.Fprintln(&b, "Standard (non-batch) prices shown; long-context, batch, service-tier, regional and tool-fee prices are in --json.")
	fmt.Fprintln(&b, "A request dated before a model's FROM date is reported as unpriced, never guessed.")
	for _, n := range p.Notes {
		fmt.Fprintln(&b, "- "+n)
	}
	return strings.TrimRight(b.String(), "\n")
}

func optional(d *pricing.Dec) string {
	if d == nil {
		return "-"
	}
	return d.String()
}

func writes(t pricing.Tier) string {
	if len(t.CacheWrite) == 0 {
		return "-"
	}
	var parts []string
	for _, ttl := range []string{"5m", "30m", "1h", "12h"} {
		if p, ok := t.CacheWrite[ttl]; ok {
			parts = append(parts, ttl+"="+p.String())
		}
	}
	return strings.Join(parts, " ")
}
