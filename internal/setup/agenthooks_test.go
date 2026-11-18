package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// cursorUserHooks follows the example in cursor.com/docs/agent/hooks (read 2026-11-17):
// version 1, and each event holds a flat list of {command, type, timeout, matcher}.
const cursorUserHooks = `{
  "version": 1,
  "hooks": {
    "beforeShellExecution": [{"command": "./audit.sh", "timeout": 30, "failClosed": true}],
    "preToolUse": [{"command": "./mine.sh", "matcher": "Read"}]
  },
  "x-extra": "keep me"
}
`

func cursorCommands(m map[string]any, event string) []string {
	var out []string
	hooks, _ := m["hooks"].(map[string]any)
	list, _ := hooks[event].([]any)
	for _, h := range list {
		out = append(out, h.(map[string]any)["command"].(string))
	}
	return out
}

func TestCursorHooksMergeAndUndo(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.json")
	if err := os.WriteFile(path, []byte(cursorUserHooks), 0o600); err != nil {
		t.Fatal(err)
	}
	specs := []HookSpec{
		{Event: "preToolUse", Matcher: "Read", Command: hookBase + "pre_tool_repeat_read --agent cursor", Timeout: 5},
		{Event: "sessionEnd", Command: hookBase + "session_end_receipt --agent cursor", Timeout: 3},
	}
	e, err := PlanCursorHooks(path, specs, hookBase)
	if err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(dir, "data")
	if _, err := Apply(data, []*Edit{e}, time.Now()); err != nil {
		t.Fatal(err)
	}
	m := readJSON(t, path)
	if m["version"] != float64(1) || m["x-extra"] != "keep me" {
		t.Fatalf("unrelated keys were lost: %v", m)
	}
	if got := cursorCommands(m, "preToolUse"); len(got) != 2 || got[0] != "./mine.sh" || !strings.HasPrefix(got[1], hookBase) {
		t.Fatalf("preToolUse = %v", got)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), `"failClosed": true`) || !strings.Contains(string(b), `"matcher": "Read"`) {
		t.Fatalf("user entries changed:\n%s", b)
	}
	if again, _ := PlanCursorHooks(path, specs, hookBase); !again.NoOp {
		t.Fatalf("a second install should change nothing:\n%s", again.Show)
	}
	// Edit the file after install so undo has to strip rather than restore.
	b, _ := os.ReadFile(path)
	_ = os.WriteFile(path, []byte(strings.Replace(string(b), `"keep me"`, `"edited"`, 1)), 0o600)
	cs, err := HooksInstalled(data)
	if err != nil || len(cs) != 1 || cs[0].Agent != CursorHooks {
		t.Fatalf("installed = %+v, %v", cs, err)
	}
	if _, err := UndoSelected(data, []string{cs[0].ID}); err != nil {
		t.Fatal(err)
	}
	m = readJSON(t, path)
	if got := cursorCommands(m, "preToolUse"); len(got) != 1 || got[0] != "./mine.sh" {
		t.Fatalf("after undo preToolUse = %v", got)
	}
	if got := cursorCommands(m, "sessionEnd"); len(got) != 0 {
		t.Fatalf("after undo sessionEnd = %v", got)
	}
	if m["version"] != float64(1) || m["x-extra"] != "edited" || len(cursorCommands(m, "beforeShellExecution")) != 1 {
		t.Fatalf("undo touched the user's entries: %v", m)
	}
}

func TestCursorHooksNewFileGetsAVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	e, err := PlanCursorHooks(path, []HookSpec{{Event: "sessionEnd", Command: hookBase + "x", Timeout: 3}}, hookBase)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(e.updated), `"version": 1`) {
		t.Fatalf("hooks.json needs version 1:\n%s", e.updated)
	}
}

func TestGeminiHooksUseTheGroupLayout(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	e, err := PlanGeminiHooks(path, []HookSpec{{Event: "BeforeTool", Matcher: "read_file", Command: hookBase + "pre_tool_repeat_read --agent gemini", Timeout: 5000}}, hookBase)
	if err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(dir, "data")
	if _, err := Apply(data, []*Edit{e}, time.Now()); err != nil {
		t.Fatal(err)
	}
	m := readJSON(t, path)
	if got := commands(m, "BeforeTool"); len(got) != 1 || m["theme"] != "dark" {
		t.Fatalf("%v", m)
	}
	cs, _ := HooksInstalled(data)
	if len(cs) != 1 || cs[0].Agent != GeminiHooks {
		t.Fatalf("installed = %+v", cs)
	}
}
