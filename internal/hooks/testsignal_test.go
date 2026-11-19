package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/store"
)

// The failure payload follows the PostToolUseFailure example on code.claude.com/docs/en/hooks
// (read 2026-11-17): no exit-code field, the code is the first line of "error" ("Exit code N").
// The success payload follows the Bash PostToolUse recorded from Claude Code 2.1.318
// (testdata/recorded/2.1.318/bash_post.json), which carries no exit code either: success means 0.

const commandsTOML = `[commands.test]
run = ["go", "test", "./..."]

[commands.unit-test]
run = ["npm", "test"]

[commands.lint]
run = ["golangci-lint", "run"]
`

func (r *rig) commands(body string) {
	r.t.Helper()
	if err := os.MkdirAll(filepath.Join(r.files, ".trove"), 0o700); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.files, ".trove", "commands.toml"), []byte(body), 0o600); err != nil {
		r.t.Fatal(err)
	}
}

// esc returns s as the inside of a JSON string.
func esc(s string) string {
	b, _ := json.Marshal(s)
	return strings.Trim(string(b), `"`)
}

func (r *rig) bashOK(cwd, cmd string, interrupted bool) {
	r.t.Helper()
	in := map[string]any{"session_id": sessionID, "cwd": cwd, "hook_event_name": "PostToolUse", "tool_name": "Bash",
		"tool_input":    map[string]any{"command": cmd, "description": "Run tests"},
		"tool_response": map[string]any{"stdout": "ok", "stderr": "", "interrupted": interrupted, "isImage": false}, "tool_use_id": "t1"}
	b, _ := json.Marshal(in)
	if out, code := r.run(EventTestResult, string(b)); out != "" || code != 0 {
		r.t.Fatalf("the test-result hook must print nothing: out=%q code=%d", out, code)
	}
}

func (r *rig) bashFail(cwd, cmd, errText string, interrupt bool) {
	r.t.Helper()
	intr := "false"
	if interrupt {
		intr = "true"
	}
	in := r.input("claude_post_bash_failure.json", "{{CWD}}", esc(cwd), "{{CMD}}", esc(cmd), "{{ERROR}}", esc(errText), "{{INTERRUPT}}", intr)
	if out, code := r.run(EventTestResult, in); out != "" || code != 0 {
		r.t.Fatalf("the test-result hook must print nothing: out=%q code=%d", out, code)
	}
}

func (r *rig) testRuns() []event {
	var out []event
	for _, e := range r.events() {
		if e.Kind == KindTestRun {
			out = append(out, e)
		}
	}
	return out
}

func TestTestResultRecordsExitCodesOfAllowlistedTestCommandsOnly(t *testing.T) {
	r := newRig(t, nil)
	r.commands(commandsTOML)

	r.bashOK(r.files, "go test ./...", false)
	r.bashOK(r.files, "  npm   test ", false) // whitespace differences are not a different command
	r.bashFail(r.files, "go test ./...", "Exit code 1\n--- FAIL: TestX (0.00s)", false)
	r.bashFail(r.files, "go test ./...", "Exit code 137", false)

	// None of these is a result of an allowlisted test command.
	r.bashOK(r.files, "golangci-lint run", false)                             // allowlisted, but not a test command
	r.bashOK(r.files, "go test ./internal/x", false)                          // not the exact allowlisted command
	r.bashOK(r.files, "echo hi", false)                                       // not allowlisted
	r.bashOK(r.files, "go test ./...", true)                                  // interrupted: no result
	r.bashFail(r.files, "go test ./...", "Exit code 1", true)                 // aborted: no result
	r.bashFail(r.files, "go test ./...", "Command timed out after 2m", false) // no exit code to read
	r.bashFail(r.files, "go test ./...", "stderr\nExit code 1", false)        // the code must be the first line
	r.bashOK(t.TempDir(), "go test ./...", false)                             // a folder without commands.toml

	got := r.testRuns()
	want := []int{0, 0, 1, 137}
	if len(got) != len(want) {
		t.Fatalf("%d test results; want %d: %+v", len(got), len(want), got)
	}
	for i, e := range got {
		if e.Exit != want[i] || e.Key != r.key() {
			t.Fatalf("result %d = %+v; want exit %d for this session", i, e, want[i])
		}
	}
	if got[0].Hook != got[2].Hook || got[0].Hook == got[1].Hook || len(got[0].Hook) != 16 {
		t.Fatalf("command ids must be a stable 16-char hash per command: %q %q %q", got[0].Hook, got[1].Hook, got[2].Hook)
	}
	// Metadata only: the spool never holds the command, its output or the folder.
	spool, _ := os.ReadFile(filepath.Join(r.dir, "hooks", spoolName))
	for _, bad := range []string{"go test", "npm", "FAIL", esc(r.files)} {
		if strings.Contains(string(spool), bad) {
			t.Fatalf("the spool holds %q: %s", bad, spool)
		}
	}
}

// The failure payload recorded from a live Claude Code 2.1.326 session (2026-11-17, headless, a
// throwaway Go project; paths and ids redacted) gives the same result as the documented one.
func TestTestResultReadsTheRecordedFailurePayload(t *testing.T) {
	r := newRig(t, nil)
	r.commands(commandsTOML)
	in := r.input("recorded/2.1.326/bash_post_failure.json", "{{CWD}}", esc(r.files))
	if out, code := r.run(EventTestResult, in); out != "" || code != 0 {
		t.Fatalf("out=%q code=%d", out, code)
	}
	if got := r.testRuns(); len(got) != 1 || got[0].Exit != 1 {
		t.Fatalf("recorded %+v; want one run with exit 1", got)
	}
}

func TestTestResultIgnoresOtherToolsAndBadInput(t *testing.T) {
	r := newRig(t, nil)
	r.commands(commandsTOML)
	in := map[string]any{"session_id": sessionID, "cwd": r.files, "hook_event_name": "PostToolUse", "tool_name": "Grep",
		"tool_input": map[string]any{"command": "go test ./..."}, "tool_response": map[string]any{}}
	b, _ := json.Marshal(in)
	r.run(EventTestResult, string(b))
	r.run(EventTestResult, "garbage")
	r.commands("not = [valid toml")
	r.bashOK(r.files, "go test ./...", false)
	if got := r.testRuns(); len(got) != 0 {
		t.Fatalf("recorded %+v", got)
	}
}

func TestTestResultsReachTheTestRunsTable(t *testing.T) {
	r := newRig(t, nil)
	r.commands(commandsTOML)
	r.bashFail(r.files, "go test ./...", "Exit code 2", false)
	r.bashOK(r.files, "go test ./...", false)

	db, err := store.Open(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := IngestSpool(context.Background(), db, r.dir); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT session_key, exit_code FROM test_runs ORDER BY ts, id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var codes []int
	for rows.Next() {
		var k string
		var c int
		if err := rows.Scan(&k, &c); err != nil {
			t.Fatal(err)
		}
		if k != r.key() {
			t.Fatalf("session key %q", k)
		}
		codes = append(codes, c)
	}
	if len(codes) != 2 || codes[0] != 2 || codes[1] != 0 {
		t.Fatalf("test_runs exit codes = %v; want [2 0]", codes)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM hook_events`).Scan(&n)
	if n != 0 {
		t.Fatalf("test results must not be counted as hook decisions: %d hook_events rows", n)
	}
}
