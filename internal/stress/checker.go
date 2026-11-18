package stress

import (
	"fmt"

	"github.com/usetrove/trove/internal/optimize"
)

// The per-optimizer checker (plan item 2.5). Every shipped optimizer runs ALONE, with its state "on", over the scripted
// workflows that carry failure output, once as Claude Code and once as a custom agent with no cache markers. It must:
//   - keep every failure line of the original output visible to the model,
//   - leave the previous request a byte prefix of the next one (cache markers aside, which cache_breakpoints may place),
//   - give byte-identical output on a repeat run of the same session,
//   - break no other invariant CheckRequest and CheckRecovery know about.
//
// TestEveryOptimizerPassesItsChecker runs it in CI for every name in optimize.Pipeline and optimize.ToolPipeline, so a new
// transform cannot merge without passing it. Results are counts only; no request text leaves the check (rule 7).

// checkerWorkflows are the scripted sessions the checker plays: failing tests, noisy repeated logs, repeated build output,
// application logs with stack traces, and structured JSON-lines logs.
var checkerWorkflows = []string{"fix-failing-test", "debug-with-logs", "dependency-upgrade", "incident-log-forensics", "json-log-triage"}

// checkerSeed is fixed so a failure reproduces exactly.
const checkerSeed uint64 = 20261117

// OptimizerCheck is one optimizer's checker result. Every number is measured on the scripted workflows.
type OptimizerCheck struct {
	Name          string   `json:"name"`
	Requests      int      `json:"requests"`
	KeyLines      int      `json:"failure_lines"`
	KeyLinesKept  int      `json:"failure_lines_kept"`
	PrefixBroken  int      `json:"prefix_broken"`
	Deterministic bool     `json:"deterministic"`
	Violations    int      `json:"violations"`
	Problems      []string `json:"problems,omitempty"`
}

// Passed reports whether the optimizer met every rule.
func (c OptimizerCheck) Passed() bool { return len(c.Problems) == 0 }

// CheckOptimizer runs the checker for one shipped optimizer.
func CheckOptimizer(name string) (OptimizerCheck, error) {
	return checkWith(name, func(custom bool, rec *memStore) *optimize.Engine {
		if custom {
			return newCustomEngine([]string{name}, rec, PricingDate)
		}
		return newEngine([]string{name}, rec, PricingDate)
	})
}

// checkerNames lists every shipped optimizer once, request-level first. A name in both pipelines (the request-level and
// tool-level halves of one optimizer) is switched on as one setting, so it is checked once.
func checkerNames() []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range allNames() {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// CheckEveryOptimizer runs the checker for every shipped optimizer.
func CheckEveryOptimizer() ([]OptimizerCheck, error) {
	var out []OptimizerCheck
	for _, n := range checkerNames() {
		c, err := CheckOptimizer(n)
		if err != nil {
			return out, err
		}
		out = append(out, c)
	}
	return out, nil
}

// checkWith runs the checker with the engine mk builds. Tests pass engines holding deliberately broken optimizers.
func checkWith(name string, mk func(custom bool, rec *memStore) *optimize.Engine) (OptimizerCheck, error) {
	rep, err := RunWorkflows(WorkflowOptions{Seed: checkerSeed, Only: checkerWorkflows, engine: mk})
	if err != nil {
		return OptimizerCheck{Name: name}, fmt.Errorf("checker for %s: %w", name, err)
	}
	h := rep.Headline
	c := OptimizerCheck{
		Name: name, Requests: h.Requests, KeyLines: h.KeyLines, KeyLinesKept: h.KeyLinesKept,
		PrefixBroken: h.Requests - h.PrefixPreserved, Deterministic: h.AllDeterministic, Violations: h.Violations,
	}
	if c.KeyLines == 0 {
		c.Problems = append(c.Problems, "the checker workflows held no failure lines to check")
	}
	if c.KeyLinesKept < c.KeyLines {
		c.Problems = append(c.Problems, fmt.Sprintf("dropped %d of %d failure lines", c.KeyLines-c.KeyLinesKept, c.KeyLines))
	}
	if c.PrefixBroken > 0 {
		c.Problems = append(c.Problems, fmt.Sprintf("changed an earlier part of the request on %d of %d turns", c.PrefixBroken, c.Requests))
	}
	if !c.Deterministic {
		c.Problems = append(c.Problems, "a repeat run of the same session produced different bytes")
	}
	// A row that dropped failure lines counts that as one violation, already reported above; report only the rest here.
	other, unstored := 0, 0
	for _, r := range rep.Rows {
		other += r.Violations - r.Unstored
		unstored += r.Unstored
		if r.KeyLinesKept < r.KeyLines {
			other--
		}
	}
	if unstored > 0 {
		c.Problems = append(c.Problems, fmt.Sprintf("%d trim markers name output that was never stored byte for byte", unstored))
	}
	if other > 0 {
		c.Problems = append(c.Problems, fmt.Sprintf("%d other broken invariants (pairing, cost bound, recovery or fail-open)", other))
	}
	return c, nil
}
