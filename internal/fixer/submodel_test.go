package fixer

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/setup"
	"github.com/usetrove/trove/internal/tasks"
)

func subagentModelFixes(t *testing.T, e *Env) []*Fix {
	t.Helper()
	all, errs := Detect(context.Background(), e)
	if len(errs) > 0 {
		t.Fatalf("detect errors: %v", errs)
	}
	var out []*Fix
	for _, x := range all {
		if x.Kind == TaskSubagentModel {
			out = append(out, x)
		}
	}
	return out
}

func TestSubagentModelNeedsAChosenModel(t *testing.T) {
	f := newFx(t)
	f.env.Tasks = &TasksRequest{Feature: tasks.FeatureSubagentModel}
	if _, errs := Detect(context.Background(), f.env); len(errs) == 0 || !strings.Contains(errs[0].Error(), "--model") {
		t.Fatalf("want an error asking for --model, got %v", errs)
	}
	f.env.Tasks.Options.SubagentModel = "inherit"
	if _, errs := Detect(context.Background(), f.env); len(errs) == 0 {
		t.Fatal("inherit must be refused: it is what an omitted model already means")
	}
}

func TestSubagentModelOnlyFillsUnsetFilesAndUndoes(t *testing.T) {
	f := newFx(t)
	f.env.Tasks = &TasksRequest{Feature: tasks.FeatureSubagentModel, Options: tasks.Options{SubagentModel: "haiku"}}
	orig := "---\nname: reviewer\ndescription: reviews\n---\n\nReview it.\n"
	unset := f.write(".claude/agents/reviewer.md", orig)
	chosen := f.write(".claude/agents/planner.md", "---\nname: planner\nmodel: opus\n---\nplan\n")
	f.write(".claude/agents/trove-scout.md", "---\nname: trove-scout\nmodel: haiku\n---\n"+tasks.Managed+"\n")
	fixes := subagentModelFixes(t, f.env)
	if len(fixes) != 1 || fixes[0].Path != unset || !fixes[0].Applicable() || !fixes[0].Suggestion {
		t.Fatalf("want one applicable suggestion for the unset file, got %+v", fixes)
	}
	if f.applyAllCount(TaskSubagentModel) != 0 {
		t.Fatal("must not be part of --all")
	}
	if _, err := Apply(context.Background(), f.env, ids(fixes)); err != nil {
		t.Fatal(err)
	}
	if got := f.read(unset); !strings.Contains(got, "description: reviews\n"+tasks.ModelMarker+"\nmodel: haiku\n---\n") {
		t.Errorf("file after apply:\n%s", got)
	}
	if !strings.Contains(f.read(chosen), "model: opus") || strings.Contains(f.read(chosen), "haiku") {
		t.Error("a model the user chose must never change")
	}
	if again := subagentModelFixes(t, f.env); len(again) != 0 {
		t.Errorf("planned again after apply: %v", ids(again))
	}
	if _, err := setup.Undo(f.env.DataDir); err != nil {
		t.Fatal(err)
	}
	if f.read(unset) != orig {
		t.Errorf("undo must restore the file byte for byte:\n%q", f.read(unset))
	}
}

func TestSubagentModelRespectsEnvChoice(t *testing.T) {
	for _, k := range []string{"CLAUDE_CODE_SUBAGENT_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL_FORCE"} {
		f := newFx(t)
		f.env.Tasks = &TasksRequest{Feature: tasks.FeatureSubagentModel, Options: tasks.Options{SubagentModel: "sonnet"}}
		f.write(".claude/agents/a.md", "---\nname: a\n---\n")
		f.env.Getenv = func(name string) string {
			if name == k {
				return "1"
			}
			return ""
		}
		fx := subagentModelFixes(t, f.env)
		if len(fx) != 1 || fx[0].Applicable() || !strings.Contains(fx[0].Blocked, k) {
			t.Errorf("%s set: want a blocked fix naming it, got %+v", k, fx)
		}
	}
}

func TestSubagentModelAllowlistNeedsTheFeature(t *testing.T) {
	f := newFx(t)
	p := filepath.Join(f.proj, ".claude", "agents", "mine.md")
	if f.env.Allowed(p) == nil {
		t.Fatal("a user's own agent file must be unreachable without the subagent-model feature")
	}
	f.env.Tasks = &TasksRequest{Feature: tasks.FeatureSubagentModel}
	if err := f.env.Allowed(p); err != nil {
		t.Errorf("subagent-model must reach it: %v", err)
	}
	if f.env.Allowed(filepath.Join(f.proj, ".claude", "agents", "sub", "x.md")) == nil {
		t.Error("nested folders must stay unreachable")
	}
}
