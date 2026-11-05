package fixer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/setup"
	"github.com/usetrove/trove/internal/tasks"
)

func taskFixes(t *testing.T, e *Env) []*Fix {
	t.Helper()
	all, errs := Detect(context.Background(), e)
	if len(errs) > 0 {
		t.Fatalf("detect errors: %v", errs)
	}
	var out []*Fix
	for _, x := range all {
		if x.Kind == TaskScout || x.Kind == TaskCascade {
			out = append(out, x)
		}
	}
	return out
}

func ids(fs []*Fix) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.ID)
	}
	return out
}

func TestTasksOnlyWhenRequested(t *testing.T) {
	f := newFx(t)
	if got := taskFixes(t, f.env); len(got) != 0 {
		t.Fatalf("plain detection must not plan tasks files: %+v", got)
	}
}

func TestScoutApplyKeepsUserTextAndUndoIsByteExact(t *testing.T) {
	f := newFx(t)
	f.env.Tasks = &TasksRequest{Feature: tasks.FeatureScout}
	orig := "# My project\r\n\r\nUse tabs.\r\n"
	md := f.write("CLAUDE.md", orig)
	fixes := taskFixes(t, f.env)
	if len(fixes) != 2 {
		t.Fatalf("want agent file and snippet, got %d", len(fixes))
	}
	for _, x := range fixes {
		if !x.Applicable() || !x.Suggestion {
			t.Fatalf("must be an applicable suggestion: %+v", x)
		}
		if x.Label != "estimated" || x.TokensPerSession != 0 {
			t.Errorf("no saving may be claimed before a measurement: %+v", x)
		}
		if !strings.Contains(strings.Join(x.Notes, "\n"), "Overhead (estimated)") {
			t.Errorf("overhead must be declared: %v", x.Notes)
		}
	}
	if f.applyAllCount(TaskScout) != 0 {
		t.Fatal("tasks fixes are suggestions and must not be part of --all")
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
	scout := filepath.Join(f.proj, ".claude", "agents", "trove-scout.md")
	if body := f.read(scout); !strings.Contains(body, "tools: Read, Grep, Glob\n") || !strings.Contains(body, tasks.Managed) {
		t.Errorf("scout file:\n%s", body)
	}
	got := f.read(md)
	if !strings.HasPrefix(got, orig) || !strings.Contains(got, "`trove-scout`") {
		t.Errorf("user text must stay first and the snippet follow:\n%q", got)
	}
	if again := taskFixes(t, f.env); len(again) != 0 {
		t.Errorf("already applied, but planned again: %v", ids(again))
	}
	if _, err := setup.Undo(f.env.DataDir); err != nil {
		t.Fatal(err)
	}
	if f.read(md) != orig {
		t.Errorf("undo must restore CLAUDE.md byte for byte:\n%q", f.read(md))
	}
	if _, err := os.Stat(scout); err == nil {
		t.Error("undo must remove the scout file Trove created")
	}
}

func TestTasksNeverOverwriteUsersOwnFile(t *testing.T) {
	f := newFx(t)
	f.env.Tasks = &TasksRequest{Feature: tasks.FeatureScout}
	p := f.write(".claude/agents/trove-scout.md", "---\nname: trove-scout\n---\nmine\n")
	for _, x := range taskFixes(t, f.env) {
		if x.Path == p && (x.Applicable() || !strings.Contains(x.Blocked, "Trove did not write it")) {
			t.Fatalf("a user-written file must be blocked: %+v", x)
		}
	}
}

func TestTasksAllowlistIsNarrow(t *testing.T) {
	f := newFx(t)
	for rel, ok := range map[string]bool{
		".claude/agents/trove-scout.md":      true,
		".claude/commands/trove-cascade.md":  true,
		".claude/agents/my-agent.md":         false,
		".claude/agents/sub/trove-x.md":      false,
		".claude/skills/trove-x.md":          false,
		".claude/commands/trove-cascade.sh":  false,
		".claude/agents/trove-scout.md.orig": false,
	} {
		err := f.env.Allowed(filepath.Join(f.proj, filepath.FromSlash(rel)))
		if (err == nil) != ok {
			t.Errorf("%s: allowed=%v, want %v (%v)", rel, err == nil, ok, err)
		}
	}
}

func TestCascadeNeedsTestCommandAndRespectsForcedModel(t *testing.T) {
	f := newFx(t)
	f.env.Tasks = &TasksRequest{Feature: tasks.FeatureCascade}
	if _, errs := Detect(context.Background(), f.env); len(errs) == 0 || !strings.Contains(errs[0].Error(), "test command") {
		t.Fatalf("want an error that asks for the test command, got %v", errs)
	}
	f.env.Tasks.Options.TestCmd = "go test ./..."
	if got := taskFixes(t, f.env); len(got) != 3 {
		t.Fatalf("want worker, escalation and command, got %d", len(got))
	}
	f.env.Getenv = func(k string) string {
		if k == "CLAUDE_CODE_SUBAGENT_MODEL_FORCE" {
			return "1"
		}
		return ""
	}
	for _, x := range taskFixes(t, f.env) {
		if x.Applicable() || !strings.Contains(x.Blocked, "SUBAGENT_MODEL_FORCE") {
			t.Fatalf("a forced subagent model makes the cascade pointless; it must be blocked: %+v", x)
		}
	}
}

func TestDamagedSnippetMarkerIsLeftAlone(t *testing.T) {
	f := newFx(t)
	f.env.Tasks = &TasksRequest{Feature: tasks.FeatureScout}
	f.write("CLAUDE.md", "intro\n"+tasks.SnippetOpen+"\nhalf a block\n")
	for _, x := range taskFixes(t, f.env) {
		if x.Keys[0] == tasks.RoleSnippet && x.Applicable() {
			t.Fatal("a damaged block must not be edited")
		}
	}
}

func TestSnippetUpdatesInPlace(t *testing.T) {
	old := "top\n\n" + tasks.SnippetOpen + "\nold text\n" + tasks.SnippetClose + "\n\nbottom\n"
	got := string(upsertBlock([]byte(old), tasks.SnippetOpen+"\nnew\n"+tasks.SnippetClose+"\n"))
	if want := "top\n\n" + tasks.SnippetOpen + "\nnew\n" + tasks.SnippetClose + "\n\nbottom\n"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestTasksQueuedDuringSessionThenApplied(t *testing.T) {
	f := newFx(t)
	f.env.Tasks = &TasksRequest{Feature: tasks.FeatureScout}
	f.sessions(6, []string{"Read"}, now.Add(-5*time.Minute))
	fixes := taskFixes(t, f.env)
	res, err := Apply(context.Background(), f.env, ids(fixes))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.Status != "queued" {
			t.Fatalf("want queued, got %+v", r)
		}
	}
	if _, err := os.Stat(filepath.Join(f.proj, ".claude", "agents", "trove-scout.md")); err == nil {
		t.Fatal("nothing may be written during an active session")
	}
	later := *f.env
	later.Tasks = nil
	later.Now = now.Add(2 * time.Hour)
	res, err = ApplyQueued(context.Background(), &later)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.Status != "applied" {
			t.Fatalf("idle: %+v", r)
		}
	}
}
