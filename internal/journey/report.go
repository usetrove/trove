package journey

import (
	"fmt"
	"strings"
)

// Markdown renders the result as the tables that a README and a methodology
// doc can reuse. The calibration status comes first. When the calibration gate
// fails, no savings table is rendered at all. Estimated and measured numbers are
// kept in separate sections, and every table carries its label.
func Markdown(r Result) string {
	var b strings.Builder
	writeCalibrationStatus(&b, r.Calibration)
	if !r.Calibration.Pass {
		writeWithheld(&b, "the generator does not reproduce real session output within tolerance")
		writeLimits(&b, r.Caveats)
		return b.String()
	}

	b.WriteString("## Estimated savings (offline tier)\n\n")
	fmt.Fprintf(&b, "**Label: %s.** Not billed. Synthetic sessions that pass the calibration gate, seed %d, %d simulated week(s), optimizer budget %d ms. Prices: pinned table %s, as of %s. Pilot model: `%s`.\n\n",
		r.Label, r.Seed, r.Weeks, r.BudgetMS, r.PricingVersion, r.PricingDate, r.PilotModel)

	gateway := profilesOf(r, ModelGateway)
	fmt.Fprintf(&b, "### API-key users: cost per session on `%s` (estimated, list price)\n\n", r.PilotModel)
	b.WriteString("| Profile | N | Median turns | Median cost off → on | Saved, median per session (95% CI) | Within noise | Billed input tokens off → on | Saved, input tokens | Structure ok off / on | Verbatim recovery (on) | Trove tool overhead (tokens) |\n")
	b.WriteString("|---|---:|---:|---:|---:|:-:|---:|---:|:-:|:-:|---:|\n")
	for _, p := range gateway {
		c := costOf(p, r.PilotModel)
		fmt.Fprintf(&b, "| %s | %d | %.0f | $%.4f → $%.4f | %+.1f%% [%+.1f, %+.1f] | %s | %s → %s | %.1f%% | %s / %s | %s | %s |\n",
			p.Name, p.N, p.MedianTurns, c.MedianOffUSD, c.MedianOnUSD,
			c.SavedPctMedian, c.SavedPctCILow, c.SavedPctCIHigh, yesNo(c.WithinNoise),
			fmtInt(inputTokens(p.TokensOff)), fmtInt(inputTokens(p.TokensOn)), p.SavedTokensPctTotal,
			pct(p.StructureOkOff), pct(p.StructureOkOn), verbatimCell(p), fmtInt(p.OverheadTokens))
	}

	b.WriteString("\n### API-key users: the other pinned models (estimated, same tokens)\n\n")
	b.WriteString("| Profile | Sonnet 5.5 saved, median (95% CI) | Opus 5.5 saved, median (95% CI) |\n")
	b.WriteString("|---|---:|---:|\n")
	for _, p := range gateway {
		s, o := costOf(p, "claude-sonnet-5-5"), costOf(p, "claude-opus-5-5")
		fmt.Fprintf(&b, "| %s | %+.1f%% [%+.1f, %+.1f] | %+.1f%% [%+.1f, %+.1f] |\n", p.Name,
			s.SavedPctMedian, s.SavedPctCILow, s.SavedPctCIHigh,
			o.SavedPctMedian, o.SavedPctCILow, o.SavedPctCIHigh)
	}

	hooks := profilesOf(r, ModelHooks)
	b.WriteString("\n### Subscription users: hook level, tokens only (estimated, no dollars)\n\n")
	b.WriteString("Subscription traffic is never routed through the gateway. These numbers come from the plugin's hooks on tool calls, and are reported in context tokens. They are not dollars.\n\n")
	b.WriteString("| Profile | N | Median turns | Context tokens off → on | Saved, median per session (95% CI) | Within noise | Reads denied | Inputs rewritten (not re-run) | Structure ok off / on | Verbatim recovery (on) |\n")
	b.WriteString("|---|---:|---:|---:|---:|:-:|---:|---:|:-:|:-:|\n")
	for _, p := range hooks {
		h := p.Hook
		fmt.Fprintf(&b, "| %s | %d | %.0f | %s → %s | %+.1f%% [%+.1f, %+.1f] | %s | %s | %s | %s / %s | %s |\n",
			p.Name, p.N, p.MedianTurns, fmtInt(h.ContextTokensOff), fmtInt(h.ContextTokensOn),
			h.SavedPctMedian, h.SavedPctCILow, h.SavedPctCIHigh, yesNo(h.WithinNoise),
			fmtInt(int64(h.DeniedReads)), fmtInt(int64(h.RewrittenInput)),
			pct(p.StructureOkOff), pct(p.StructureOkOn), verbatimCell(p))
	}

	b.WriteString("\n### Not measured by this tier\n\n")
	none := true
	for _, p := range r.Profiles {
		if p.Model == string(ModelNone) {
			none = false
			fmt.Fprintf(&b, "- **%s**: %s\n", p.Name, p.Reason)
		}
	}
	if none {
		b.WriteString("None.\n")
	}

	b.WriteString("\n## Measured savings (live tier)\n\n")
	b.WriteString("**Label: measured.** Not run yet. The live tier runs real Claude Code sessions against a Console API key and reports the provider's own cost. It will appear here, on its own table, once it has been run and approved.\n")

	writeLimits(&b, r.Caveats)
	return b.String()
}

// SensitivityMarkdown compares the main run with the production-budget run for
// the same seed. A difference here is what the machine and the budget cost. It
// is withheld, like every savings table, unless both runs pass the gate.
func SensitivityMarkdown(main, prod Result) string {
	var b strings.Builder
	writeCalibrationStatus(&b, main.Calibration)
	if !main.Calibration.Pass || !prod.Calibration.Pass {
		writeWithheld(&b, "the calibration gate failed for at least one of the two runs")
		return b.String()
	}
	b.WriteString("## Sensitivity: production optimizer budget (estimated)\n\n")
	fmt.Fprintf(&b, "**Label: estimated.** Same seed %d and weeks. Main run budget %d ms with parallel sessions; this run %d ms, one session at a time. Each optimizer that runs past the budget forwards its input unchanged, so a timeout here shows up as lost savings, not as an error.\n\n",
		main.Seed, main.BudgetMS, prod.BudgetMS)
	b.WriteString("| Profile | Saved, median (main) | Saved, median (production budget) | Difference (points) | Fail-open requests (main / production) |\n")
	b.WriteString("|---|---:|---:|---:|---:|\n")
	for i, p := range main.Profiles {
		if i >= len(prod.Profiles) {
			break
		}
		q := prod.Profiles[i]
		if p.Model == string(ModelGateway) {
			a, c := costOf(p, main.PilotModel), costOf(q, prod.PilotModel)
			fmt.Fprintf(&b, "| %s | %+.1f%% | %+.1f%% | %+.1f | %d / %d |\n", p.Name,
				a.SavedPctMedian, c.SavedPctMedian, c.SavedPctMedian-a.SavedPctMedian, p.FailOpenRequests, q.FailOpenRequests)
		} else if p.Model == string(ModelHooks) && p.Hook != nil && q.Hook != nil {
			fmt.Fprintf(&b, "| %s | %+.1f%% (context) | %+.1f%% (context) | %+.1f | %d / %d |\n", p.Name,
				p.Hook.SavedPctMedian, q.Hook.SavedPctMedian, q.Hook.SavedPctMedian-p.Hook.SavedPctMedian, p.FailOpenRequests, q.FailOpenRequests)
		}
	}
	return b.String()
}

// writeCalibrationStatus prints the gate's verdict and each tool class at the top.
func writeCalibrationStatus(b *strings.Builder, st CalibrationStatus) {
	verdict := "PASS"
	if !st.Pass {
		verdict = "FAIL"
	}
	fmt.Fprintf(b, "**Calibration: %s.** Source: %s. Split: %s. Gate: holdout half, sizes within ±%.0f%%, shares within ±%.0f points.\n\n",
		verdict, st.Source, st.Split, (st.Tolerance.SizeRatio-1)*100, st.Tolerance.ShareAbs*100)
	for _, c := range st.Checks {
		res := "pass"
		if !c.Pass {
			res = "FAIL: " + c.Why
		}
		fmt.Fprintf(b, "- %s: %s\n", c.Tool, res)
	}
	b.WriteString("\n")
}

func writeWithheld(b *strings.Builder, why string) {
	b.WriteString("## Savings tables withheld\n\n")
	fmt.Fprintf(b, "No savings are shown: %s. Fix the gate or the generator, then run again. The calibration table above names the tool classes that differ.\n\n", why)
}

func writeLimits(b *strings.Builder, caveats []string) {
	b.WriteString("## Limits of this model\n\n")
	for _, c := range caveats {
		fmt.Fprintf(b, "- %s\n", c)
	}
}

func profilesOf(r Result, m Model) []ProfileResult {
	var out []ProfileResult
	for _, p := range r.Profiles {
		if p.Model == string(m) {
			out = append(out, p)
		}
	}
	return out
}

func costOf(p ProfileResult, model string) CostStat {
	for _, c := range p.Cost {
		if c.Model == model {
			return c
		}
	}
	return CostStat{}
}

// verbatimCell shows "—" when Trove stored nothing, since there was nothing to check.
func verbatimCell(p ProfileResult) string {
	if p.TrimmedSessions == 0 {
		return "—"
	}
	return fmt.Sprintf("%s of %d trimmed", pct(p.VerbatimRecoveredOn), p.TrimmedSessions)
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func pct(f float64) string { return fmt.Sprintf("%.0f%%", f*100) }

func fmtInt(n int64) string {
	s := fmt.Sprint(n)
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}
