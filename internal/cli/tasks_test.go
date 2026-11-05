package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTasksPlanChangesNothingAndShowsOverhead(t *testing.T) {
	h, proj, _ := settingsHarness(t)
	if code := h.run("tasks", "plan", "scout", "--project", proj, "--json"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	var rep tasksReport
	if err := json.Unmarshal(h.out.Bytes(), &rep); err != nil {
		t.Fatalf("%v: %s", err, h.out)
	}
	if len(rep.Fixes) != 2 || rep.Overhead.PerRequestTokens <= 0 {
		t.Fatalf("want agent file, snippet and a declared overhead: %+v", rep)
	}
	if _, err := os.Stat(filepath.Join(proj, ".claude")); err == nil {
		t.Fatal("plan must not write anything")
	}
}

func TestTasksCascadeAsksForTestCommand(t *testing.T) {
	h, proj, _ := settingsHarness(t)
	if code := h.run("tasks", "plan", "cascade", "--project", proj); code == 0 {
		t.Fatal("want a failure without a test command")
	}
	if !strings.Contains(h.err.String(), "test command") {
		t.Errorf("the error should say what to do: %s", h.err)
	}
}

func TestTasksApplyThenUndo(t *testing.T) {
	h, proj, _ := settingsHarness(t)
	if code := h.run("tasks", "apply", "cascade", "--project", proj, "--test-cmd", "go test ./...", "--yes"); code != 0 {
		t.Fatalf("exit %d: %s %s", code, h.out, h.err)
	}
	cmd := filepath.Join(proj, ".claude", "commands", "trove-cascade.md")
	b, err := os.ReadFile(cmd)
	if err != nil || !strings.Contains(string(b), "`go test ./...`") {
		t.Fatalf("command file: %v\n%s", err, b)
	}
	h.out.Reset()
	if code := h.run("tasks", "apply", "cascade", "--project", proj, "--test-cmd", "go test ./...", "--yes"); code != 0 || !strings.Contains(h.out.String(), "already in place") {
		t.Fatalf("second apply should find nothing to do: %d\n%s", code, h.out)
	}
	if code := h.run("undo", "--all"); code != 0 {
		t.Fatalf("undo exit %d: %s", code, h.err)
	}
	if _, err := os.Stat(cmd); err == nil {
		t.Error("undo must remove the command file Trove created")
	}
}
