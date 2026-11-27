package report

import (
	"fmt"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/analyze"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/measure/limitfit"
)

// Opusplan is the finding for a user whose Claude Code model setting is `opusplan`. That setting "resolves to Opus
// during plan mode and Sonnet during execution, so each plan-mode toggle is a model switch and starts a fresh
// cache" (code.claude.com/docs/en/prompt-caching, read 2026-11-24). The switches are measured from history; a
// /model switch between the same two families looks the same, so they are an upper bound on what opusplan caused.
type Opusplan struct {
	Switches measure.Figure `json:"switches"` // measured
	Sessions int            `json:"sessions"`
	// PerSession is switches per session that had any (measured).
	PerSession measure.Figure `json:"switches_per_session"`
	Tokens     measure.Figure `json:"cache_write_tokens"` // measured: what the switches wrote to the cache again
	USD        measure.Figure `json:"api_equivalent_usd"` // estimated, at API list prices
	// Limits is the same as a share of the plan limits (estimated, API-price scale); nil without a fit.
	Limits *limitfit.Estimate `json:"limits,omitempty"`
	Advice string             `json:"advice"`
}

const opusplanAdvice = "Each switch between plan mode and execution rebuilds the whole context on the other model. Plan in a short, fresh session and execute in another, or pick one model for long sessions."

func opusplanFrom(entries []analyze.RebuildEntry, w measure.Window, in Inputs) *Opusplan {
	if in.ClaudeModelSetting != "opusplan" {
		return nil
	}
	t := analyze.OpusSonnetToggles(entries, w.From.UnixMilli(), w.To.UnixMilli())
	if t.Switches == 0 {
		return nil
	}
	op := &Opusplan{
		Switches: fig(float64(t.Switches), measure.Measured, t.Switches), Sessions: t.Sessions,
		PerSession: fig(float64(t.Switches)/float64(max(1, t.Sessions)), measure.Measured, t.Sessions),
		Tokens:     fig(float64(t.Tokens), measure.Measured, t.Switches),
		USD:        fig(0, measure.Estimated, 0),
		Advice:     opusplanAdvice,
	}
	priced := 0
	for _, e := range t.Entries {
		d := limitfit.Delta{Model: e.Model}
		d.Tokens[limitfit.TWrite1h] = e.Write1h
		d.Tokens[limitfit.TWrite5m] = e.CacheWrite - e.Write1h
		limitfit.PriceDelta(in.Prices, &d, time.UnixMilli(e.TS))
		if d.USD > 0 {
			op.USD.Value += d.USD
			priced++
		}
	}
	op.USD.N, op.USD.Unpriced = priced, t.Switches-priced
	if op.USD.Value > 0 {
		op.Limits = in.LimitFit.PercentUSD(op.USD.Value)
	}
	return op
}

func renderOpusplan(b *strings.Builder, op *Opusplan, o Options) {
	b.WriteString("\n" + heading("Plan mode with opusplan"))
	line := func(s string) { b.WriteString(wrap(s, o.width(), "  ", "  ") + "\n") }
	s := fmt.Sprintf("Your model setting is opusplan. %d switches between Opus and Sonnet in %d %s (%.1f per session) wrote %s tokens to the cache again (measured)",
		int(op.Switches.Value), op.Sessions, plural(op.Sessions, "session", "sessions"), op.PerSession.Value, FormatTokens(op.Tokens.Value))
	if op.USD.Value > 0 {
		s += fmt.Sprintf(", about $%.2f at API prices (estimated)", op.USD.Value)
	}
	if e := op.Limits; e != nil {
		var parts []string
		if e.FiveHour != nil {
			parts = append(parts, "~"+pctText(e.FiveHour.Range)+" of your 5-hour limit")
		}
		if e.Week != nil {
			parts = append(parts, "~"+pctText(e.Week.Range)+" of your weekly limit")
		}
		if len(parts) > 0 {
			s += ", " + strings.Join(parts, " and ") + " (estimated from API prices)"
		}
	}
	line(s + ".")
	line(op.Advice)
	line("A /model switch between the same two models looks the same in the log, so these counts are an upper bound.")
}
