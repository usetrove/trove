package report

import (
	"context"
	"fmt"
	"strings"

	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/store"
)

// kindSpend returns spend per session kind, or nothing when no request has
// declared a kind: an all-unknown split says nothing and would only add noise.
func kindSpend(ctx context.Context, db *store.DB, w measure.Window) ([]measure.TotalsRow, error) {
	rows, err := measure.TotalsBy(ctx, db, w, measure.ByKind)
	if err != nil {
		return nil, err
	}
	declared := false
	for _, r := range rows {
		if r.Key != store.KindUnknown {
			declared = true
		}
	}
	if !declared {
		return nil, nil
	}
	return rows, nil
}

// renderUpstreams prints one block per server: capacity first (measured), then
// cost (estimated). Capacity comes first because for these servers it usually
// matters more than the price per token.
func renderUpstreams(b *strings.Builder, rows []measure.UpstreamRow, o Options) {
	for _, u := range rows {
		b.WriteString(wrap(fmt.Sprintf("%s: %d requests, %s tokens in, %s out (measured)",
			u.Name, u.Requests, FormatTokens(float64(u.InputTokens)), FormatTokens(float64(u.OutputTokens))), o.width(), "  ", "    ") + "\n")
		line := fmt.Sprintf("latency p50 %s, p95 %s (measured)", ms(u.LatencyP50MS.Value), ms(u.LatencyP95MS.Value))
		if u.TTFTP50MS != nil && u.TTFTP95MS != nil {
			line += fmt.Sprintf("; time to first byte p50 %s, p95 %s (measured, streamed requests)", ms(u.TTFTP50MS.Value), ms(u.TTFTP95MS.Value))
		}
		if u.OutputTokensPerSec.Value > 0 {
			line += fmt.Sprintf("; %.1f output tokens/s (measured)", u.OutputTokensPerSec.Value)
		}
		b.WriteString(wrap(line, o.width(), "    ", "    ") + "\n")
		if u.CostUSD != nil {
			s := fmt.Sprintf("cost at your per-token rates: %s", Est(*u.CostUSD))
			if u.CostUSD.Unpriced > 0 {
				s += fmt.Sprintf(" (%d requests could not be priced)", u.CostUSD.Unpriced)
			}
			b.WriteString(wrap(s, o.width(), "    ", "    ") + "\n")
		}
		if g := u.GPU; g != nil {
			s := fmt.Sprintf("GPU-hour estimate at $%s/hour: $%.4f per million tokens while serving, $%.4f per million over the whole span (estimated; busy %.0f%% of the time)",
				g.HourlyUSD, g.USDPerMTokBusy, g.USDPerMTokSpan, g.Utilization*100)
			b.WriteString(wrap(s, o.width(), "    ", "    ") + "\n")
			b.WriteString(wrap(g.Note, o.width(), "    ", "    ") + "\n")
		}
	}
	b.WriteString(wrap("Time to first byte is when the first response byte reached Trove, not the first generated token.", o.width(), "  ", "  ") + "\n")
}

func ms(v float64) string {
	if v >= 1000 {
		return fmt.Sprintf("%.1f s", v/1000)
	}
	return fmt.Sprintf("%.0f ms", v)
}
