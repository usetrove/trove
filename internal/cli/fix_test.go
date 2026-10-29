package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/fixer"
)

const dateClaudeMD = "# Demo\n\nToday's date: 2026-10-29\n\nNever edit gen/.\n"

// fixHarness sets up a fake home with a project whose CLAUDE.md has one
// changing line, so exactly one fix (cache_breaker_fix) is available.
func fixHarness(t *testing.T) (*harness, string, string) {
	t.Helper()
	h := newHarness(t, nil)
	home, _ := h.app.HomeDir()
	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(proj, "CLAUDE.md")
	if err := os.WriteFile(p, []byte(dateClaudeMD), 0o644); err != nil {
		t.Fatal(err)
	}
	return h, proj, p
}

func listFixes(t *testing.T, h *harness, proj string) fixListReport {
	t.Helper()
	h.out.Reset()
	if code := h.run("fix", "--project", proj, "--json"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	var rep fixListReport
	if err := json.Unmarshal(h.out.Bytes(), &rep); err != nil {
		t.Fatalf("%v: %s", err, h.out)
	}
	return rep
}

func TestFixListShowApplyAndUndo(t *testing.T) {
	h, proj, claude := fixHarness(t)

	rep := listFixes(t, h, proj)
	if len(rep.Fixes) != 1 || rep.Fixes[0].Kind != fixer.CacheBreaker || rep.Fixes[0].Label != "estimated" {
		t.Fatalf("list: %+v", rep.Fixes)
	}
	id := rep.Fixes[0].ID
	if rep.Fixes[0].Diff != "" {
		t.Fatal("the list should not carry diffs")
	}

	// Showing a fix prints the diff and changes nothing.
	h.out.Reset()
	if code := h.run("fix", id, "--project", proj); code != 0 || !strings.Contains(h.out.String(), "-Today's date: 2026-10-29") {
		t.Fatalf("show: %d %s %s", code, h.out, h.err)
	}
	// --apply with --json but without --yes must not apply anything.
	h.out.Reset()
	if code := h.run("fix", id, "--apply", "--project", proj, "--json"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if b, _ := os.ReadFile(claude); string(b) != dateClaudeMD {
		t.Fatalf("a file changed without approval: %q", b)
	}
	// Without a terminal and without --yes, the question cannot be answered: no change.
	h.out.Reset()
	h.run("fix", id, "--apply", "--project", proj)
	if b, _ := os.ReadFile(claude); string(b) != dateClaudeMD || !strings.Contains(h.out.String(), "No changes made") {
		t.Fatalf("no approval, no change: %q %s", b, h.out)
	}

	h.out.Reset()
	if code := h.run("fix", id, "--apply", "--yes", "--project", proj, "--json"); code != 0 {
		t.Fatalf("apply: %d %s", code, h.err)
	}
	var ap fixApplyReport
	if err := json.Unmarshal(h.out.Bytes(), &ap); err != nil || ap.Applied != 1 {
		t.Fatalf("apply report: %+v %v %s", ap, err, h.out)
	}
	if b, _ := os.ReadFile(claude); strings.Contains(string(b), "Today's date") || !strings.Contains(string(b), "Never edit gen/.") {
		t.Fatalf("after apply: %q", b)
	}

	// Undo by fix id restores the file exactly.
	if code := h.run("undo", id); code != 0 {
		t.Fatalf("undo: %s", h.err)
	}
	if b, _ := os.ReadFile(claude); string(b) != dateClaudeMD {
		t.Fatalf("not byte-identical after undo: %q", b)
	}
	if code := h.run("undo", "nope"); code != 1 || !strings.Contains(h.err.String(), "nope") {
		t.Fatalf("unknown id should fail: %d %s", code, h.err)
	}
}

func TestFixAllNeedsApplyAndSkipsNothingElse(t *testing.T) {
	h, proj, claude := fixHarness(t)
	if code := h.run("fix", "--all", "--project", proj); code != 1 || !strings.Contains(h.err.String(), "--apply") {
		t.Fatalf("--all alone must be refused: %d %s", code, h.err)
	}
	h.out.Reset()
	if code := h.run("fix", "--all", "--apply", "--yes", "--project", proj, "--json"); code != 0 {
		t.Fatalf("%d %s", code, h.err)
	}
	if b, _ := os.ReadFile(claude); strings.Contains(string(b), "Today's date") {
		t.Fatalf("--all --apply --yes should apply: %q", b)
	}
	if code := h.run("undo", "--all"); code != 0 {
		t.Fatal(h.err.String())
	}
	if b, _ := os.ReadFile(claude); string(b) != dateClaudeMD {
		t.Fatalf("undo --all: %q", b)
	}
}

func TestFixIsQueuedWhileAnAgentIsActive(t *testing.T) {
	h, proj, claude := fixHarness(t)
	home, _ := h.app.HomeDir()
	hist := filepath.Join(home, ".claude", "projects", "p")
	if err := os.MkdirAll(hist, 0o755); err != nil {
		t.Fatal(err)
	}
	// A history file written just now means a session may be open.
	if err := os.WriteFile(filepath.Join(hist, "s.jsonl"), []byte(`{"type":"mode"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep := listFixes(t, h, proj)
	if !rep.Active || len(rep.Fixes) != 1 {
		t.Fatalf("expected an active agent: %+v", rep)
	}
	h.out.Reset()
	if code := h.run("fix", rep.Fixes[0].ID, "--apply", "--yes", "--project", proj, "--json"); code != 0 {
		t.Fatalf("%d %s", code, h.err)
	}
	var ap fixApplyReport
	_ = json.Unmarshal(h.out.Bytes(), &ap)
	if ap.Queued != 1 || ap.Applied != 0 {
		t.Fatalf("want queued: %+v", ap)
	}
	if b, _ := os.ReadFile(claude); string(b) != dateClaudeMD {
		t.Fatal("nothing may change while an agent is active")
	}
	if rep := listFixes(t, h, proj); len(rep.Queued) != 1 {
		t.Fatalf("queue not shown: %+v", rep)
	}
}

func TestFixRefusesBadInput(t *testing.T) {
	h, proj, _ := fixHarness(t)
	if code := h.run("fix", "nope-123", "--project", proj); code != 1 || !strings.Contains(h.err.String(), "trove fix") {
		t.Fatalf("unknown id: %d %s", code, h.err)
	}
	if code := h.run("fix", "x", "--all", "--apply", "--project", proj); code != 1 {
		t.Fatal("id with --all must be refused")
	}
	if code := h.run("fix", "--yes", "--project", proj); code != 1 {
		t.Fatal("--yes without --apply must be refused")
	}
}
