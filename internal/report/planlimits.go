package report

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/measure/limitfit"
)

// PlanLimits is what Trove learned about how this user's tokens move Claude Code's plan-usage meters
// (see docs/limit-fit.md). Every number in it is an estimate for this account from Claude Code's own
// meter, never an official weighting (PRINCIPLES.md rules 6 and 9).
type PlanLimits struct {
	Label    string      `json:"label"` // always "estimated"
	FiveHour *LimitMeter `json:"five_hour,omitempty"`
	Week     *LimitMeter `json:"seven_day,omitempty"`
	// Waste and Savings are the headline's dollar figures as percent of each limit, by the API-price
	// scale ("estimated from API prices"). Nil when there is no scale yet or nothing to convert.
	Waste   *limitfit.Estimate `json:"waste,omitempty"`
	Savings *limitfit.Estimate `json:"savings,omitempty"`
}

// LimitMeter is one meter's fit without the bootstrap rounds.
type LimitMeter struct {
	limitfit.MeterFit
	Label string `json:"label"` // always "estimated"
	Basis string `json:"basis,omitempty"`
}

func limitMeter(m limitfit.MeterFit) *LimitMeter {
	m.Reps, m.USDReps = nil, nil
	lm := &LimitMeter{MeterFit: m, Label: "estimated"}
	switch m.Tier {
	case limitfit.TierFit:
		lm.Basis = limitfit.BasisText[limitfit.BasisFit]
	case limitfit.TierPrices:
		lm.Basis = limitfit.BasisText[limitfit.BasisAPIPrices]
	}
	return lm
}

// planLimitsFrom builds the section. It is nil when the meter has never been seen.
func planLimitsFrom(fit *limitfit.Result, h Headline) *PlanLimits {
	if fit == nil || fit.Samples == 0 {
		return nil
	}
	pl := &PlanLimits{Label: "estimated", FiveHour: limitMeter(fit.FiveHour), Week: limitMeter(fit.Week)}
	if h.Waste.Value > 0 {
		pl.Waste = fit.PercentUSD(h.Waste.Value)
	}
	if h.Savings.Value > 0 {
		pl.Savings = fit.PercentUSD(h.Savings.Value)
	}
	return pl
}

// num shows a weight or percentage with two significant digits: 12, 2.5, 0.25, 0.031.
func num(v float64) string {
	if v == 0 {
		return "0"
	}
	d := max(0, 1-int(math.Floor(math.Log10(math.Abs(v)))))
	s := fmt.Sprintf("%.*f", d, v)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

func rangeText(r limitfit.Range) string {
	return fmt.Sprintf("%s (%s to %s)", num(r.Point), num(r.Lo), num(r.Hi))
}

// pctText is "0.52% (0.44 to 0.61%)".
func pctText(r limitfit.Range) string {
	return fmt.Sprintf("%s%% (%s to %s%%)", num(r.Point), num(r.Lo), num(r.Hi))
}

func windowsText(n int) string { return fmt.Sprintf("%d %s", n, plural(n, "window", "windows")) }

// shareText is "1.2% of 5h (0.9 to 1.5%)".
func shareText(s *limitfit.Share, window string) string {
	return fmt.Sprintf("%s%% of %s (%s to %s%%)", num(s.Point), window, num(s.Lo), num(s.Hi))
}

var typeTitles = map[string]string{
	"input": "Input", "output": "Output", "cache_read": "Cache read",
	"cache_write_5m": "Cache write (5 min)", "cache_write_1h": "Cache write (1 hour)",
}

func renderPlanLimits(b *strings.Builder, pl *PlanLimits, o Options) {
	b.WriteString("\n" + heading("Plan limits (your estimate, not official weights)"))
	line := func(s, indent string) { b.WriteString(wrap(s, o.width(), indent, indent) + "\n") }
	for _, m := range []*LimitMeter{pl.FiveHour, pl.Week} {
		name, short := "5-hour limit", "5h"
		if m.Meter == limitfit.SevenDay {
			name, short = "Weekly limit", "the week"
		}
		switch m.Tier {
		case limitfit.TierNone:
			s := fmt.Sprintf("%s: not enough data yet (%d clean %s over %s). It %s", name, m.CleanIntervals,
				plural(m.CleanIntervals, "interval", "intervals"), windowsText(m.Windows), needText(m))
			line(s, "  ")
			continue
		case limitfit.TierPrices:
			line(fmt.Sprintf("%s, %s: $1 of API-equivalent usage is about %s of %s, from %d clean intervals over %s.",
				name, m.Basis, pctText(*m.PerUSD), short, m.CleanIntervals, windowsText(m.Windows)), "  ")
			line("Weights per token type: "+needText(m), "  ")
		case limitfit.TierFit:
			s := fmt.Sprintf("%s, %s, from %d clean intervals over %s", name, m.Basis, m.CleanIntervals, windowsText(m.Windows))
			if m.Trimmed > 0 {
				s += fmt.Sprintf(" (%d set aside as outside use)", m.Trimmed)
			}
			if m.Profile {
				s += "; it uses the 5-hour limit's mix of weights with a scale of its own, until it has enough data for its own"
			}
			line(s+".", "  ")
			data := [][]string{{"Token type", "% of " + short + " per 1M tokens"}}
			for _, w := range m.Weights {
				data = append(data, []string{typeTitles[w.Type], rangeText(w.Range)})
			}
			b.WriteString(table(data, o.width()))
			if m.InputDetermined {
				var rs []string
				for _, r := range m.Ratios[1:] {
					rs = append(rs, fmt.Sprintf("%s %s", strings.ToLower(typeTitles[r.Type]), rangeText(r.Range)))
				}
				line("Relative to input = 1: "+strings.Join(rs, "; ")+".", "  ")
			} else {
				line("Relative to input: not pinned down yet, because input is a small share of your tokens and its weight is still uncertain.", "  ")
			}
			if fam := familiesText(m); fam != "" {
				line(fam, "  ")
			}
		}
		if m.Outside != nil {
			line(fmt.Sprintf("Not explained by this machine's Claude Code: %s of the meter's movement. That is claude.ai, Cowork, other devices, and calls Claude Code does not log, such as compaction summaries.",
				pctText(*m.Outside)), "  ")
		}
		if m.RegimeSince > 0 {
			line(fmt.Sprintf("The meter seems to count differently since %s; only data since then is used.", day(time.UnixMilli(m.RegimeSince).Local())), "  ")
		}
	}
	for _, c := range []struct {
		what string
		e    *limitfit.Estimate
	}{{"Waste found", pl.Waste}, {"Savings", pl.Savings}} {
		if c.e == nil {
			continue
		}
		var parts []string
		if c.e.FiveHour != nil {
			parts = append(parts, shareText(c.e.FiveHour, "5h"))
		}
		if c.e.Week != nil {
			parts = append(parts, shareText(c.e.Week, "the week"))
		}
		line(fmt.Sprintf("%s in these terms: about %s (estimated from API prices).", c.what, strings.Join(parts, ", ")), "  ")
	}
	line("Numbers in brackets are 90% intervals. These are estimates for your account from Claude Code's own meter, not official weights.", "  ")
}

func needText(m *LimitMeter) string {
	var parts []string
	if m.NeedIntervals > 0 {
		parts = append(parts, fmt.Sprintf("%d more clean %s", m.NeedIntervals, plural(m.NeedIntervals, "interval", "intervals")))
	}
	if m.NeedWindows > 0 {
		w := "5-hour windows"
		if m.Meter == limitfit.SevenDay {
			w = "weekly windows"
		}
		if m.NeedWindows == 1 {
			w = strings.TrimSuffix(w, "s")
		}
		parts = append(parts, fmt.Sprintf("%d more %s", m.NeedWindows, w))
	}
	s := "needs " + strings.Join(parts, " and ")
	if m.HoursPerInterval > 0 && m.NeedIntervals > 0 {
		s += fmt.Sprintf(" (about %s hours of use with the status line on)", num(float64(m.NeedIntervals)*m.HoursPerInterval))
	}
	if len(parts) == 0 {
		return "needs more data."
	}
	return s + "."
}

func familiesText(m *LimitMeter) string {
	var sep, pooled []string
	for _, f := range m.Families {
		switch {
		case f.Family == m.Reference:
		case f.Separate:
			sep = append(sep, fmt.Sprintf("%s ×%s", f.Family, rangeText(f.Range)))
		default:
			pooled = append(pooled, f.Family)
		}
	}
	var s []string
	if len(sep) > 0 {
		s = append(s, "Model families relative to "+m.Reference+": "+strings.Join(sep, "; ")+".")
	}
	if len(pooled) > 0 {
		p := strings.Join(pooled, ", ")
		s = append(s, strings.ToUpper(p[:1])+p[1:]+" counted like "+m.Reference+" (too little data to tell).")
	}
	return strings.Join(s, " ")
}
