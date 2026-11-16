package stress

import (
	"strings"
	"sync"
	"testing"
)

// Under `go test -short` (which CI runs with the race detector) these tests share ONE small report, because the race
// detector makes the engine many times slower and every extra run adds minutes to a package that already has other
// slow tests. Without -short they run the full set, several times, as a harder check.
var shortReport struct {
	once sync.Once
	rep  WorkflowReport
	err  error
}

func report(t *testing.T, o WorkflowOptions) WorkflowReport {
	t.Helper()
	if testing.Short() {
		shortReport.once.Do(func() {
			shortReport.rep, shortReport.err = RunWorkflows(WorkflowOptions{Seed: 20261108, Only: []string{"quick-question", "fix-failing-test"}})
		})
		if shortReport.err != nil {
			t.Fatal(shortReport.err)
		}
		return shortReport.rep
	}
	rep, err := RunWorkflows(o)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func skipIfShort(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("runs the workflows several more times; skipped with -short (CI runs it with the race detector)")
	}
}

func TestWorkflowsRunCleanAndDeterministic(t *testing.T) {
	rep := report(t, WorkflowOptions{Seed: 20261108})
	if len(rep.Rows) < 3 {
		t.Fatalf("want at least 3 workflow rows, got %d", len(rep.Rows))
	}
	h := rep.Headline
	if h.KeyLinesKept != h.KeyLines || h.KeyLines == 0 {
		t.Errorf("%d of %d failure lines survived optimizing; every one must, and there must be some to check", h.KeyLinesKept, h.KeyLines)
	}
	if !testing.Short() && (h.Heavy.Workflows == 0 || h.Everyday.Workflows == 0) {
		t.Error("both tiers must have workflows")
	}
	if h.Violations != 0 {
		t.Errorf("%d broken invariants in scripted workflows", h.Violations)
	}
	if !h.AllDeterministic {
		t.Error("a workflow produced different bytes on a second run")
	}
	if h.PrefixPreserved != h.Requests {
		t.Errorf("only %d of %d requests kept the cached prefix", h.PrefixPreserved, h.Requests)
	}
	for _, r := range rep.Rows {
		if r.Tier == tierCustom && r.SavedPct <= 0 {
			t.Errorf("%s: a custom agent with no caching should gain from the cache markers (%.1f%%)", r.Name, r.SavedPct)
		}
		if r.SavedPct < -1 {
			t.Errorf("%s: the optimized arm costs more than the original (%.1f%%)", r.Name, r.SavedPct)
		}
		if r.OffUSD <= 0 {
			t.Errorf("%s: not priced", r.Name)
		}
	}
}

func TestWorkflowsSameSeedSameReport(t *testing.T) {
	skipIfShort(t)
	a, err := RunWorkflows(WorkflowOptions{Seed: 7})
	if err != nil {
		t.Fatal(err)
	}
	b, err := RunWorkflows(WorkflowOptions{Seed: 7})
	if err != nil {
		t.Fatal(err)
	}
	for i := range a.Rows {
		x, y := a.Rows[i], b.Rows[i]
		if x.OffUSD != y.OffUSD || x.OnUSD != y.OnUSD || x.TokensCut != y.TokensCut {
			t.Errorf("%s differs between runs of one seed", x.Name)
		}
	}
}

func TestWorkflowMarkdownLabelsEveryFigure(t *testing.T) {
	md := WorkflowMarkdown(report(t, WorkflowOptions{Seed: 1}))
	for _, want := range []string{"Estimated", "Measured", "scripted", "not a forecast"} {
		if !strings.Contains(strings.ToLower(md), strings.ToLower(want)) {
			t.Errorf("report lacks %q", want)
		}
	}
}

// A saving only counts if nothing was lost. Across several seeds, every failure line must survive, trimmed output must
// be recoverable, and a heavy session must never cost more than the original.
func TestWorkflowsHoldAcrossSeeds(t *testing.T) {
	skipIfShort(t)
	for _, seed := range []uint64{1, 2, 3, 20261108, 99999} {
		rep, err := RunWorkflows(WorkflowOptions{Seed: seed})
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		h := rep.Headline
		if h.Violations != 0 || !h.AllDeterministic || h.KeyLinesKept != h.KeyLines || h.PrefixPreserved != h.Requests {
			t.Errorf("seed %d: violations=%d deterministic=%v key lines %d/%d prefix %d/%d", seed, h.Violations,
				h.AllDeterministic, h.KeyLinesKept, h.KeyLines, h.PrefixPreserved, h.Requests)
		}
		for _, r := range rep.Rows {
			if r.Tier == tierHeavy && r.SavedPct < 0 {
				t.Errorf("seed %d: heavy workflow %s costs more optimized (%.1f%%)", seed, r.Name, r.SavedPct)
			}
		}
	}
}

// A harder pass: the same sessions at other sizes, with the trimmer at its tightest, and with the modelled re-read diff.
// Nothing may break, whatever the size.
func TestWorkflowsHoldAtOtherSizesAndTunings(t *testing.T) {
	skipIfShort(t)
	cases := []WorkflowOptions{
		{Seed: 5, Scale: 0.5},
		{Seed: 5, Scale: 4},
		{Seed: 5, Scale: 2, Tuning: minTuning()},
		{Seed: 5, Scale: 2, RereadDiff: true},
	}
	for _, c := range cases {
		rep, err := RunWorkflows(c)
		if err != nil {
			t.Fatalf("%+v: %v", c, err)
		}
		h := rep.Headline
		if h.Violations != 0 || !h.AllDeterministic || h.KeyLinesKept != h.KeyLines || h.PrefixPreserved != h.Requests {
			t.Errorf("scale %v tuning %v diff %v: violations=%d deterministic=%v key lines %d/%d prefix %d/%d", c.Scale, c.Tuning != nil, c.RereadDiff,
				h.Violations, h.AllDeterministic, h.KeyLinesKept, h.KeyLines, h.PrefixPreserved, h.Requests)
		}
	}
}
