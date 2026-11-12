package stress

import (
	"fmt"
	"strings"
)

// Markdown renders the report. Savings appear only in the realism layer's section and are always labeled
// estimated. Every other number is labeled measured or is a pass/fail count.
func Markdown(r Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# trove bench stress: %s. %d checks passed, %d failed, %d skipped\n\n",
		strings.ToUpper(string(r.Status)), r.Checks.Passed, r.Checks.Failed, r.Checks.Skipped)
	fmt.Fprintf(&b, "mode %s, seed %d, %s/%s, %s, started %s, took %s\n\n",
		r.Mode, r.Seed, r.GoOS, r.GoArch, r.GoVer, r.Started, r.Elapsed)
	b.WriteString("Offline: no provider call, no spend. Local folders only. Replay, when requested, reads your logs in memory and writes only aggregates.\n\n")

	b.WriteString("| Layer | Status | Cases | Passed | Failed | Skipped | Took |\n|---|---|---|---|---|---|---|\n")
	for _, l := range r.Layers {
		fmt.Fprintf(&b, "| %s | %s | %d | %d | %d | %d | %s |\n", l.Name, strings.ToUpper(string(l.Status)), l.Cases, l.Passed, l.Failed, l.Skipped, l.Took)
	}
	b.WriteString("\n")

	for _, l := range r.Layers {
		fmt.Fprintf(&b, "## %s\n\n", l.Name)
		if l.Savings != nil {
			writeSavings(&b, l.Savings)
		}
		if len(l.Measured) > 0 {
			b.WriteString("Measured (not savings):\n\n")
			for _, m := range l.Measured {
				fmt.Fprintf(&b, "- %s: %.2f %s (%s)\n", m.Name, m.Value, m.Unit, m.Label)
			}
			b.WriteString("\n")
		}
		for _, n := range l.Notes {
			fmt.Fprintf(&b, "- note: %s\n", n)
		}
		if len(l.Notes) > 0 {
			b.WriteString("\n")
		}
		if len(l.Failures) > 0 {
			fmt.Fprintf(&b, "Failures (%d):\n\n", len(l.Failures))
			for _, f := range l.Failures {
				fmt.Fprintf(&b, "- **%s** / %s: %s", f.Case, f.Invariant, f.Detail)
				fmt.Fprintf(&b, " (input %d bytes, hash %s). Repro: `%s`\n", f.Input.Bytes, f.Input.Hash, f.Repro)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

func writeSavings(b *strings.Builder, s *Savings) {
	fmt.Fprintf(b, "Estimated savings (%s, %d sessions, priced %s at list price):\n\n", s.Label, s.Samples, s.Pricing)
	fmt.Fprintf(b, "- median %.1f%%, 10th to 90th percentile %.1f%% to %.1f%%\n\n", s.Overall.MedianPct, s.Overall.P10Pct, s.Overall.P90Pct)
	b.WriteString("What moves the saving (ranked; numeric = |Spearman rho|, categorical = spread of group medians in points).\n")
	b.WriteString("Noise is the 95th percentile after shuffling savings across sessions; a parameter counts only if it clears its noise.\n\n")
	b.WriteString("| Rank | Parameter | Kind | Strength | Noise (p95) | Clears noise |\n|---|---|---|---|---|---|\n")
	for _, d := range s.Drivers {
		clears := "no, not distinguishable from noise"
		if d.Distinct {
			clears = "yes"
		}
		fmt.Fprintf(b, "| %d | %s | %s | %.2f | %.2f | %s |\n", d.Rank, d.Parameter, d.Kind, d.Strength, d.Noise, clears)
	}
	b.WriteString("\nBy model (median, estimated):\n\n")
	for _, g := range s.ByModel {
		fmt.Fprintf(b, "- %s: %.1f%% over %d sessions\n", g.Model, g.MedianPct, g.N)
	}
	if len(s.Edges) > 0 {
		b.WriteString("\nNamed edge cases (estimated saving, invariants):\n\n")
		for _, e := range s.Edges {
			state := "invariants held"
			if !e.Passed {
				state = "INVARIANT FAILED"
			}
			fmt.Fprintf(b, "- %s: %d turns, %.1f%% estimated saved, %s\n", e.Name, e.Turns, e.SavedPct, state)
		}
	}
	if r := s.Replay; r != nil {
		fmt.Fprintf(b, "\nReplay of local session logs (%s; aggregates only):\n\n", r.Label)
		fmt.Fprintf(b, "- %d log files (%d could not be read), %d sessions, %d assistant turns, up to %d requests sampled per session\n",
			r.LogFiles, r.FilesUnreadable, r.Sessions, r.Turns, r.PerSession)
		b.WriteString("\n| Optimizer | Mode | Requests | Changed | Failures | Bytes before | Bytes removed | Removed % | Est. tokens removed |\n")
		b.WriteString("|---|---|---|---|---|---|---|---|---|\n")
		for _, o := range r.Optimizers {
			fmt.Fprintf(b, "| %s | %s | %d | %d | %d | %d | %d | %.2f | %d |\n", o.Name, o.Mode, o.Requests, o.Changed,
				o.Failures, o.BytesBefore, o.BytesRemoved, o.BytesRemovedPct, o.EstTokensRemoved)
		}
		for _, l := range r.Limits {
			fmt.Fprintf(b, "\n- limit: %s", l)
		}
		b.WriteString("\n")
	}
	for _, n := range s.Notes {
		fmt.Fprintf(b, "\n- note: %s", n)
	}
	b.WriteString("\n\n")
}
