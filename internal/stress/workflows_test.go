package stress

import (
	"strings"
	"testing"
)

func TestWorkflowsRunCleanAndDeterministic(t *testing.T) {
	rep, err := RunWorkflows(WorkflowOptions{Seed: 20261108})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rows) < 6 {
		t.Fatalf("want at least 6 workflows, got %d", len(rep.Rows))
	}
	h := rep.Headline
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
		if r.SavedPct < -1 {
			t.Errorf("%s: the optimized arm costs more than the original (%.1f%%)", r.Name, r.SavedPct)
		}
		if r.OffUSD <= 0 {
			t.Errorf("%s: not priced", r.Name)
		}
	}
}

func TestWorkflowsSameSeedSameReport(t *testing.T) {
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
	rep, err := RunWorkflows(WorkflowOptions{Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	md := WorkflowMarkdown(rep)
	for _, want := range []string{"Estimated", "Measured", "scripted", "not a forecast"} {
		if !strings.Contains(strings.ToLower(md), strings.ToLower(want)) {
			t.Errorf("report lacks %q", want)
		}
	}
}
