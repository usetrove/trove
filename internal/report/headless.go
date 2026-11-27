package report

import (
	"fmt"
	"strings"

	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/measure/limitfit"
)

// Headless is the finding for print-mode and Agent SDK runs: how much of the last week's Claude Code use they were.
// On Max, Ultra and Team plans, monthly API credits can pay for these runs when they use an API key from the linked
// Console organisation, instead of the plan's limits (support.claude.com/en/articles/17154008, read 2026-11-24).
type Headless struct {
	measure.HeadlessShare
	// Limits is USD as a share of the plan limits (estimated, API-price scale); nil without a fit.
	Limits *limitfit.Estimate `json:"limits,omitempty"`
	Advice string             `json:"advice"`
}

const headlessAdvice = "Max, Ultra and Team plans include monthly API credits that pay for `claude -p` and Agent SDK runs made with an API key from your linked Console organisation, so those runs stop using your plan's limits. `trove headless` shows how to set that up for those jobs only. Runs that already use an API key are not on your plan's limits, so this is an upper bound."

func headlessFrom(h measure.HeadlessShare, in Inputs) *Headless {
	if h.Sessions == 0 {
		return nil
	}
	out := &Headless{HeadlessShare: h, Advice: headlessAdvice}
	if h.USD.Value > 0 {
		out.Limits = in.LimitFit.PercentUSD(h.USD.Value)
	}
	return out
}

func renderHeadless(b *strings.Builder, h *Headless, o Options) {
	b.WriteString("\n" + heading("Headless and SDK runs"))
	line := func(s string) { b.WriteString(wrap(s, o.width(), "  ", "  ") + "\n") }
	s := fmt.Sprintf("In the last 7 days %d %s started as %s made %d requests (measured)",
		h.Sessions, plural(h.Sessions, "session", "sessions"), strings.Join(h.Entrypoints, ", "), h.Requests)
	if h.USD.Value > 0 {
		s += fmt.Sprintf(", about $%.2f at API prices (estimated)", h.USD.Value)
	}
	if h.Share != nil {
		s += fmt.Sprintf(", %.0f%% of Claude Code's use in that time (estimated)", h.Share.Value)
	}
	if e := h.Limits; e != nil && e.Week != nil {
		s += ", ~" + pctText(e.Week.Range) + " of your weekly limit (estimated from API prices)"
	}
	line(s + ".")
	line("Which sessions are headless comes from the entrypoint Claude Code writes to its log. It is observed, not documented.")
	line(h.Advice)
}
