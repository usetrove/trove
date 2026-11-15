package stress

import (
	"fmt"
	"strings"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/pricing"
)

// What limits the saving? This file answers three questions about the scripted workflows, so nobody has to guess:
//
//  1. Ceiling: even an oracle that deleted every tool output would save how much? That bounds what any optimizer of tool
//     output can do on a workflow, because the system prompt, the model's own replies and the cache rules stay.
//  2. Size: how does the saving move when the outputs are half as large, twice as large, four times as large?
//  3. Tuning: what would the saving be if the output trimmer ran at the smallest thresholds it allows?
//
// Every figure is estimated, from scripted sessions. The size and tuning rows are what-ifs, not claims.

// CeilingRow is one workflow's saving today against the saving of an oracle that removes every tool output.
type CeilingRow struct {
	Name       string  `json:"name"`
	Tier       string  `json:"tier"`
	SavedPct   float64 `json:"saved_pct"`   // estimated, shipped settings
	CeilingPct float64 `json:"ceiling_pct"` // estimated: every tool output replaced by a ten-byte stub
	ToolShare  float64 `json:"tool_output_share_pct"`
}

// VariantRow is the whole workflow set run under one what-if.
type VariantRow struct {
	Label      string      `json:"label"`
	Everyday   TierSummary `json:"everyday"`
	Heavy      TierSummary `json:"heavy"`
	KeyLines   int         `json:"key_lines"`
	KeyKept    int         `json:"key_lines_kept"`
	Violations int         `json:"violations"`
}

// WorkflowAnalysis is the full what-limits-the-saving report.
type WorkflowAnalysis struct {
	Seed     uint64       `json:"seed"`
	Model    string       `json:"model"`
	Pricing  string       `json:"pricing_date"`
	Ceiling  []CeilingRow `json:"ceiling"`
	Variants []VariantRow `json:"variants"`
	Notes    []string     `json:"notes"`
}

// minTuning is the trimmer at the smallest thresholds its parameters allow.
func minTuning() optimize.Tuning {
	t := optimize.Tuning{}
	for _, p := range optimize.TrimParams {
		switch p.Name {
		case optimize.ParamMinBytes, optimize.ParamMinLines, optimize.ParamMinTrimmedLines:
			t[optimize.TuningKey(config.OptimizerOutputTrimmer, p.Name)] = p.Min
		}
	}
	return t
}

// RunWorkflowAnalysis runs the three questions.
func RunWorkflowAnalysis(o WorkflowOptions) (WorkflowAnalysis, error) {
	if o.Model == "" {
		o.Model = "claude-sonnet-5-5"
	}
	tab, err := pricing.Default()
	if err != nil {
		return WorkflowAnalysis{}, fmt.Errorf("the built-in pricing table did not load: %w", err)
	}
	base := o
	base.Scale, base.Tuning = 1, nil
	rep, err := RunWorkflows(base)
	if err != nil {
		return WorkflowAnalysis{}, err
	}
	out := WorkflowAnalysis{Seed: o.Seed, Model: o.Model, Pricing: PricingDate.Format("2006-01-02")}

	for _, w := range workflowSpecs() {
		turns := buildWorkflow(w, o.Seed, 1)
		off, oracle, tool := oracleCosts(w, turns, o, tab)
		var row WorkflowRow
		for _, r := range rep.Rows {
			if r.Name == w.name {
				row = r
			}
		}
		out.Ceiling = append(out.Ceiling, CeilingRow{Name: w.name, Tier: w.tier, SavedPct: row.SavedPct,
			CeilingPct: round1(savedPct(off, oracle)), ToolShare: round1(tool)})
	}

	variants := []struct {
		label string
		o     WorkflowOptions
	}{
		{"shipped settings, sizes as scripted", base},
		{"outputs half as large", with(base, 0.5, nil)},
		{"outputs twice as large", with(base, 2, nil)},
		{"outputs four times as large", with(base, 4, nil)},
		{"trimmer at its smallest allowed thresholds, sizes as scripted", with(base, 1, minTuning())},
		{"trimmer at its smallest allowed thresholds, outputs twice as large", with(base, 2, minTuning())},
		{"MODELLED, no such optimizer exists: a re-read of a changed file sent as only its changed lines", withDiff(base, 1)},
		{"MODELLED, same, outputs twice as large", withDiff(base, 2)},
	}
	for _, v := range variants {
		r, err := RunWorkflows(v.o)
		if err != nil {
			return out, fmt.Errorf("%s: %w", v.label, err)
		}
		h := r.Headline
		out.Variants = append(out.Variants, VariantRow{Label: v.label, Everyday: h.Everyday, Heavy: h.Heavy,
			KeyLines: h.KeyLines, KeyKept: h.KeyLinesKept, Violations: h.Violations})
	}
	out.Notes = []string{
		"Everything here is estimated from scripted sessions at list price. The size and tuning rows are what-ifs: they say what would happen if real outputs were that large or the trimmer ran that tight, not that they are.",
		"The ceiling is generous: it removes every tool output, including the ones a model needs to see. No safe optimizer reaches it.",
		"Smaller trimmer thresholds trim more outputs, and each trimmed output costs the model a fetch if it needs the rest. The key-lines column shows no failure line was lost; it does not measure whether the model loses time fetching.",
		"A threshold or default change is not a result until it has been measured on real tasks (success, not just cost). Nothing here changes a shipped default.",
	}
	return out, nil
}

func withDiff(o WorkflowOptions, scale float64) WorkflowOptions {
	o.Scale, o.RereadDiff = scale, true
	return o
}

func with(o WorkflowOptions, scale float64, t optimize.Tuning) WorkflowOptions {
	o.Scale, o.Tuning = scale, t
	return o
}

// oracleCosts prices one workflow three ways: unchanged, with every tool output replaced by a stub, and returns the share of
// the unchanged request bytes that tool outputs make up on the last request.
func oracleCosts(w wfSpec, turns []turn, o WorkflowOptions, tab *pricing.Table) (off, oracle, toolShare float64) {
	spec := SessionSpec{Name: "wf-" + w.name, PrefixTokens: workflowPrefixTokens, Model: o.Model, Turns: len(turns)}
	stub := make([]turn, len(turns))
	copy(stub, turns)
	toolBytes := 0
	for i := range stub {
		toolBytes += len(turns[i].Output)
		stub[i].Output = "[omitted]"
	}
	price := func(ts []turn) float64 {
		bodies := buildBodies(spec, ts)
		exp := rng(o.Seed, "wf-expiry", spec.Name)
		var prev []byte
		var total float64
		for k, b := range bodies {
			expired := k > 0 && exp.Float64() < 0.1
			total += requestUSD(tab, spec.Model, prev, b, ts[k].Reply, expired)
			prev = b
		}
		return total
	}
	bodies := buildBodies(spec, turns)
	last := len(bodies[len(bodies)-1])
	return price(turns), price(stub), 100 * float64(toolBytes) / float64(max(last, 1))
}

// WorkflowAnalysisMarkdown renders the analysis.
func WorkflowAnalysisMarkdown(a WorkflowAnalysis) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# What limits the saving on scripted Claude Code workflows\n\nModel %s, list prices as of %s, seed %d. All figures estimated, from scripted sessions.\n\n", a.Model, a.Pricing, a.Seed)
	sb.WriteString("## 1. Ceiling: what an oracle that deleted every tool output would save\n\n| workflow | tier | saving now (est.) | ceiling (est.) | tool output share of the last request |\n|---|---|---|---|---|\n")
	for _, c := range a.Ceiling {
		fmt.Fprintf(&sb, "| %s | %s | %.1f%% | %.1f%% | %.1f%% |\n", c.Name, c.Tier, c.SavedPct, c.CeilingPct, c.ToolShare)
	}
	sb.WriteString("\n## 2. What-ifs: output size and trimmer thresholds\n\n| what-if | everyday median (range) | heavy median (range) | failure lines kept | violations |\n|---|---|---|---|---|\n")
	for _, v := range a.Variants {
		fmt.Fprintf(&sb, "| %s | %.1f%% (%.1f%% to %.1f%%) | %.1f%% (%.1f%% to %.1f%%) | %d/%d | %d |\n", v.Label,
			v.Everyday.MedianPct, v.Everyday.MinPct, v.Everyday.MaxPct, v.Heavy.MedianPct, v.Heavy.MinPct, v.Heavy.MaxPct, v.KeyKept, v.KeyLines, v.Violations)
	}
	sb.WriteString("\n## Read this before quoting anything\n\n")
	for _, n := range a.Notes {
		fmt.Fprintf(&sb, "- %s\n", n)
	}
	return sb.String()
}
