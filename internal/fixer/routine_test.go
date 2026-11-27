package fixer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/setup"
	"github.com/usetrove/trove/internal/store"
	"github.com/usetrove/trove/internal/tasks"
)

func featureFixes(t *testing.T, e *Env, feature string) []*Fix {
	t.Helper()
	all, errs := Detect(context.Background(), e)
	if len(errs) > 0 {
		t.Fatalf("detect errors: %v", errs)
	}
	var out []*Fix
	for _, x := range all {
		if x.Kind == feature {
			out = append(out, x)
		}
	}
	return out
}

func openFxDB(t *testing.T, f *fx) *store.DB {
	t.Helper()
	db, err := store.Open(f.env.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f.env.DB = db.DB
	return db
}

func TestRoutineInstallRecordsAndRemoves(t *testing.T) {
	f := newFx(t)
	db := openFxDB(t, f)
	f.env.Tasks = &TasksRequest{Feature: tasks.FeatureRoutine, Options: tasks.Options{TestCmd: "go test ./..."}}
	fixes := featureFixes(t, f.env, TaskRoutine)
	if len(fixes) != 4 {
		t.Fatalf("want two agents and two skills, got %d", len(fixes))
	}
	for _, x := range fixes {
		if !x.Applicable() || !x.Suggestion {
			t.Fatalf("must be an applicable suggestion: %+v", x)
		}
		if x.Tier == "" || x.TokensPerSession != 0 {
			t.Errorf("no saving may be claimed: %+v", x)
		}
	}
	if f.applyAllCount(TaskRoutine) != 0 {
		t.Fatal("routine agents are suggestions and must not be part of --all")
	}
	res, err := Apply(context.Background(), f.env, ids(fixes))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.Status != "applied" {
			t.Fatalf("result: %+v", r)
		}
	}
	runner := filepath.Join(f.proj, ".claude", "agents", "trove-test-runner.md")
	skill := filepath.Join(f.proj, ".claude", "skills", "trove-test", "SKILL.md")
	if !strings.Contains(f.read(runner), "effort: low\n") || !strings.Contains(f.read(skill), "agent: trove-test-runner\n") {
		t.Fatal("agent or skill not written as rendered")
	}
	var n int
	var effort, model string
	if err := db.QueryRow(`SELECT COUNT(*), MAX(effort), MAX(worker_model) FROM task_installs WHERE feature = ?`, TaskRoutine).Scan(&n, &effort, &model); err != nil {
		t.Fatal(err)
	}
	if n != 4 || effort != "low" || model != "haiku" {
		t.Errorf("task_installs: n=%d effort=%q model=%q", n, effort, model)
	}
	if again := featureFixes(t, f.env, TaskRoutine); len(again) != 0 {
		t.Errorf("already in place, planned again: %v", ids(again))
	}

	// Uninstall: the feature's own changes in this project, and nothing else.
	other := f.write("CLAUDE.md", "mine\n")
	chg, err := TaskChangeIDs(context.Background(), f.env, TaskRoutine)
	if err != nil || len(chg) != 4 {
		t.Fatalf("change ids: %v %v", chg, err)
	}
	if none, _ := TaskChangeIDs(context.Background(), f.env, TaskExplore); len(none) != 0 {
		t.Fatalf("another feature must have no changes: %v", none)
	}
	undone, err := setup.UndoSelected(f.env.DataDir, chg)
	if err != nil {
		t.Fatal(err)
	}
	if err := MarkReverted(f.env.DataDir, undone, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{runner, skill} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s must be removed", p)
		}
	}
	if f.read(other) != "mine\n" {
		t.Error("a file outside the feature must not change")
	}
	if left, _ := TaskChangeIDs(context.Background(), f.env, TaskRoutine); len(left) != 0 {
		t.Errorf("after remove nothing is left in place: %v", left)
	}
}

func TestRoutineNeverOverwritesUsersFiles(t *testing.T) {
	f := newFx(t)
	f.env.Tasks = &TasksRequest{Feature: tasks.FeatureRoutine}
	agent := f.write(".claude/agents/trove-test-runner.md", "---\nname: trove-test-runner\n---\nmine\n")
	skill := f.write(".claude/skills/trove-lint/SKILL.md", "---\nname: trove-lint\n---\nmine\n")
	for _, x := range featureFixes(t, f.env, TaskRoutine) {
		if (x.Path == agent || x.Path == skill) && (x.Applicable() || !strings.Contains(x.Blocked, "Trove did not write it")) {
			t.Fatalf("a user-written file must be blocked: %+v", x)
		}
	}
}

func TestRoutineBlockedByForcedModelAndWarnsOnEffortEnv(t *testing.T) {
	f := newFx(t)
	f.env.Tasks = &TasksRequest{Feature: tasks.FeatureRoutine}
	env := map[string]string{"CLAUDE_CODE_EFFORT_LEVEL": "high"}
	f.env.Getenv = func(k string) string { return env[k] }
	for _, x := range featureFixes(t, f.env, TaskRoutine) {
		if !x.Applicable() || !strings.Contains(strings.Join(x.Notes, "\n"), "CLAUDE_CODE_EFFORT_LEVEL is set") {
			t.Fatalf("the effort variable weakens but does not block, and must be explained: %+v", x)
		}
	}
	env["CLAUDE_CODE_SUBAGENT_MODEL_FORCE"] = "1"
	for _, x := range featureFixes(t, f.env, TaskRoutine) {
		if x.Applicable() || !strings.Contains(x.Blocked, "SUBAGENT_MODEL_FORCE") {
			t.Fatalf("a forced subagent model must block: %+v", x)
		}
	}
}

func TestExploreOverrideRespectsUsersOwnExplore(t *testing.T) {
	f := newFx(t)
	f.env.Tasks = &TasksRequest{Feature: tasks.FeatureExplore}
	fixes := featureFixes(t, f.env, TaskExplore)
	if len(fixes) != 1 || !fixes[0].Applicable() {
		t.Fatalf("want one applicable explore fix: %+v", fixes)
	}
	// The user's own Explore, in either folder and under any file name, is never replaced.
	for _, where := range []string{"project", "user"} {
		g := newFx(t)
		g.env.Tasks = f.env.Tasks
		body := "---\r\nname: Explore\r\nmodel: sonnet\r\n---\r\nmine\r\n"
		if where == "project" {
			g.write(".claude/agents/explore.md", body)
		} else {
			if err := os.MkdirAll(filepath.Join(g.env.ClaudeDir, "agents"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(g.env.ClaudeDir, "agents", "search.md"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		got := featureFixes(t, g.env, TaskExplore)
		if len(got) != 1 || got[0].Applicable() || !strings.Contains(got[0].Blocked, "named Explore") {
			t.Fatalf("%s: a user's Explore must block: %+v", where, got)
		}
	}
}

func TestSkillAllowlistIsNarrow(t *testing.T) {
	f := newFx(t)
	for rel, ok := range map[string]bool{
		".claude/skills/trove-test/SKILL.md":      true,
		".claude/skills/trove-test/other.md":      false,
		".claude/skills/my-skill/SKILL.md":        false,
		".claude/skills/trove-test/sub/SKILL.md":  false,
		".claude/skills/SKILL.md":                 false,
		"skills/trove-test/SKILL.md":              false,
		".claude/agents/trove-explore.md":         true,
		".claude/skills/trove-Test/SKILL.md":      false,
		".claude/skills/trove-test/SKILL.md.orig": false,
	} {
		err := f.env.Allowed(filepath.Join(f.proj, filepath.FromSlash(rel)))
		if (err == nil) != ok {
			t.Errorf("%s: allowed=%v, want %v (%v)", rel, err == nil, ok, err)
		}
	}
}
