package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/setup"
	"github.com/usetrove/trove/internal/store"
)

// readRange is a PreToolUse Read with its own offset and limit.
func (r *rig) readRange(path string, offset, limit int) (string, int) {
	r.t.Helper()
	p, _ := json.Marshal(path)
	in := fmt.Sprintf(`{"session_id":%q,"prompt_id":"p1","hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":%s,"offset":%d,"limit":%d},"tool_use_id":"t1"}`, sessionID, p, offset, limit)
	return r.run(EventPreRead, in)
}

func (r *rig) countKind(kind string) int {
	n := 0
	for _, e := range r.events() {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

func TestReadingMoreOfALimitedFileIsCountedOncePerFile(t *testing.T) {
	r := newRig(t, map[string]string{rwLimit: "on"})
	r.allow(Capability{Tool: "Read", InputRewrite: CapYes})
	r.start()
	big := r.file("big.go", strings.Repeat("package x // a line of code\n", 3000))
	other := r.file("other.go", strings.Repeat("package y // a line of code\n", 3000))

	// A range read of a file we never limited says nothing.
	r.readRange(other, 400, 400)
	if r.countKind(KindExtraRead) != 0 {
		t.Fatalf("a file we did not limit was counted: %v", r.kinds())
	}
	// The first read is limited.
	if out, _ := r.run(EventPreRead, r.input("claude_pre_read.json", "{{FILE}}", big)); !strings.Contains(out, "updatedInput") {
		t.Fatalf("not limited: %q", out)
	}
	// Reading lines the agent already has is not a follow-up.
	r.readRange(big, 10, 20)
	r.readRange(big, 0, 400)
	if r.countKind(KindExtraRead) != 0 {
		t.Fatalf("a read inside the limit was counted: %v", r.kinds())
	}
	// Reading past the limit is, and only once for the file, whatever the agent reads next.
	for _, rng := range [][2]int{{400, 400}, {800, 400}, {0, 2000}} {
		if out, _ := r.readRange(big, rng[0], rng[1]); out != "" {
			t.Fatalf("the follow-up read must go through untouched: %q", out)
		}
	}
	if got := r.countKind(KindExtraRead); got != 1 {
		t.Fatalf("%d extra_read events; want 1: %v", got, r.kinds())
	}
	for _, e := range r.events() {
		if e.Kind == KindExtraRead && e.Hook != config.OptimizerHookReadLimit {
			t.Fatalf("attributed to %s", e.Hook)
		}
	}
}

func TestNothingIsCountedWhenReadLimitOnlyRunsInShadow(t *testing.T) {
	r := newRig(t, map[string]string{rwLimit: "shadow"})
	r.allow(Capability{Tool: "Read", InputRewrite: CapYes})
	r.start()
	big := r.file("big.go", strings.Repeat("package x // a line of code\n", 3000))
	r.run(EventPreRead, r.input("claude_pre_read.json", "{{FILE}}", big))
	r.readRange(big, 400, 400)
	if r.countKind(KindExtraRead) != 0 {
		t.Fatalf("shadow mode changed nothing, so there is nothing to follow up: %v", r.kinds())
	}
}

func (r *rig) runTool(tool string) {
	r.t.Helper()
	if out, code := r.run(EventPostRun, r.input("claude_post_fetch.json", "{{TOOL}}", tool, "{{ID}}", "x")); out != "" || code != 0 {
		r.t.Fatalf("post run hook: out=%q code=%d", out, code)
	}
}

func TestUsingTroveRunIsCountedOncePerSessionAndOtherToolsAreNot(t *testing.T) {
	r := newRig(t, map[string]string{config.OptimizerHookLargeOutput: "on"})
	r.start()
	r.runTool("mcp__other__trove_run")
	r.runTool(fetchTool)
	r.runTool("Bash")
	if r.countKind(KindAdopted) != 0 {
		t.Fatalf("another tool counted as trove_run: %v", r.kinds())
	}
	for i := 0; i < 3; i++ {
		r.runTool(runTool)
	}
	if got := r.countKind(KindAdopted); got != 1 {
		t.Fatalf("%d adopted events; want 1 per session: %v", got, r.kinds())
	}
	if !strings.Contains(strings.Join(r.kinds(), ","), config.OptimizerHookLargeOutput+":adopted") {
		t.Fatalf("attributed to the wrong hook: %v", r.kinds())
	}
	// Bad input never fails the call.
	r.run(EventPostRun, `{"session_id":"x","tool_name":"`+runTool+`","tool_input":"nope"}`)
	r.run(EventPostRun, "garbage")
}

// The new kinds must reach the database: an unknown kind is dropped at ingest and the CHECK constraint refuses it.
func TestNewKindsSurviveTheSpoolIntoTheDatabase(t *testing.T) {
	r := newRig(t, map[string]string{rwLimit: "on", config.OptimizerHookLargeOutput: "on"})
	r.allow(Capability{Tool: "Read", InputRewrite: CapYes})
	r.start()
	big := r.file("big.go", strings.Repeat("package x // a line of code\n", 3000))
	r.run(EventPreRead, r.input("claude_pre_read.json", "{{FILE}}", big))
	r.readRange(big, 400, 400)
	r.runTool(runTool)

	db, err := store.Open(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := IngestSpool(context.Background(), db, r.dir); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{KindExtraRead, KindAdopted} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM hook_events WHERE kind = ?`, kind).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s: %d rows, %v", kind, n, err)
		}
	}
}

// writeManifest records one change the way `trove hooks install` does, in the manifest file the setup package reads.
func writeManifest(t *testing.T, dir string, c setup.Change) {
	t.Helper()
	b, _ := json.Marshal([]setup.Change{c})
	if err := os.WriteFile(filepath.Join(dir, "changes.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAdoptionIsObservableOnlyWhenTheRunHookIsInstalled(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(t.TempDir(), "settings.json")
	if AdoptionObservable(dir) {
		t.Fatal("nothing is installed")
	}
	old := `{"hooks":{"PostToolUse":[{"matcher":"Read","hooks":[{"type":"command","command":"trove hook post_tool_read_record"}]}]}}`
	if err := os.WriteFile(settings, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, dir, setup.Change{ID: "h1", Agent: setup.ClaudeHooks, Path: settings})
	if AdoptionObservable(dir) {
		t.Fatal("an install from before the run hook existed cannot show adoption")
	}
	fresh := strings.Replace(old, "trove hook post_tool_read_record", "trove hook post_tool_read_record\"},{\"type\":\"command\",\"command\":\"trove hook "+EventPostRun, 1)
	if err := os.WriteFile(settings, []byte(fresh), 0o600); err != nil {
		t.Fatal(err)
	}
	if !AdoptionObservable(dir) {
		t.Fatal("the run hook is installed")
	}
	// A Codex-only install cannot see trove_run.
	writeManifest(t, dir, setup.Change{ID: "h2", Agent: setup.CodexHooks, Path: settings})
	if AdoptionObservable(dir) {
		t.Fatal("only a Claude Code install can observe adoption")
	}
	// A file that went missing means not observable, never an error.
	writeManifest(t, dir, setup.Change{ID: "h3", Agent: setup.ClaudeHooks, Path: filepath.Join(dir, "gone.json")})
	if AdoptionObservable(dir) {
		t.Fatal("a missing settings file cannot show adoption")
	}
}
