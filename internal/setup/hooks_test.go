package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const hookBase = "/usr/local/bin/trove hook "

func hookSpecs() []HookSpec {
	c := func(ev string) string { return hookBase + ev }
	return []HookSpec{
		{Event: "PreToolUse", Matcher: "Read", Command: c("pre_tool_repeat_read"), Timeout: 5},
		{Event: "PostToolUse", Matcher: "Read", Command: c("post_tool_read_record"), Timeout: 5},
		{Event: "SessionStart", Command: c("session_reset"), Timeout: 5},
		{Event: "SessionEnd", Command: c("session_end_receipt"), Timeout: 3},
	}
}

// userSettings has hooks of the user's own: another handler on the same event and
// matcher, a hook on an event Trove does not use, keys Trove has no business with,
// and a group with extra fields.
const userSettings = `{
  "model": "opus",
  "permissions": {"allow": ["Bash(go test:*)"]},
  "hooks": {
    "PreToolUse": [
      {"matcher": "Read", "hooks": [{"type": "command", "command": "/home/me/audit-reads.sh", "timeout": 10}]},
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "/home/me/no-rm.sh"}], "x-note": "keep me"}
    ],
    "Stop": [{"hooks": [{"type": "command", "command": "/home/me/notify.sh"}]}]
  }
}
`

func applyHooks(t *testing.T, dataDir, path string) {
	t.Helper()
	e, err := PlanClaudeHooks(path, hookSpecs(), hookBase)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(dataDir, []*Edit{e}, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s is not valid JSON: %v\n%s", path, err, b)
	}
	return m
}

// commands lists every hook command under an event, in order.
func commands(m map[string]any, event string) []string {
	var out []string
	hooks, _ := m["hooks"].(map[string]any)
	groups, _ := hooks[event].([]any)
	for _, g := range groups {
		hs, _ := g.(map[string]any)["hooks"].([]any)
		for _, h := range hs {
			out = append(out, h.(map[string]any)["command"].(string))
		}
	}
	return out
}

func TestHooksMergeKeepsUserHooks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(userSettings), 0o600); err != nil {
		t.Fatal(err)
	}
	applyHooks(t, filepath.Join(dir, "data"), path)
	m := readJSON(t, path)
	if m["model"] != "opus" || m["permissions"] == nil {
		t.Fatalf("unrelated keys were lost: %v", m)
	}
	pre := commands(m, "PreToolUse")
	if len(pre) != 3 || pre[0] != "/home/me/audit-reads.sh" || pre[1] != "/home/me/no-rm.sh" || pre[2] != hookBase+"pre_tool_repeat_read" {
		t.Fatalf("PreToolUse = %v", pre)
	}
	if got := commands(m, "Stop"); len(got) != 1 || got[0] != "/home/me/notify.sh" {
		t.Fatalf("Stop = %v", got)
	}
	for _, ev := range []string{"PostToolUse", "SessionStart", "SessionEnd"} {
		if got := commands(m, ev); len(got) != 1 || !strings.HasPrefix(got[0], hookBase) {
			t.Fatalf("%s = %v", ev, got)
		}
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), `"x-note": "keep me"`) {
		t.Fatalf("an extra key on the user's group was lost:\n%s", b)
	}
}

func TestHooksInstallTwiceIsANoOp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(userSettings), 0o600); err != nil {
		t.Fatal(err)
	}
	applyHooks(t, filepath.Join(dir, "data"), path)
	e, err := PlanClaudeHooks(path, hookSpecs(), hookBase)
	if err != nil {
		t.Fatal(err)
	}
	if !e.NoOp {
		t.Fatalf("a second install should change nothing:\n%s", e.Show)
	}
}

func TestHooksUndoRestoresTheFileExactly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(userSettings), 0o600); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(dir, "data")
	applyHooks(t, data, path)
	res, err := Undo(data)
	if err != nil || len(res) != 1 || res[0].Action != "restored" {
		t.Fatalf("undo = %+v, %v", res, err)
	}
	if b, _ := os.ReadFile(path); string(b) != userSettings {
		t.Fatalf("file not restored byte for byte:\n%s", b)
	}
}

func TestHooksUndoAfterYouEditedTheFileRemovesOnlyTrovesEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(userSettings), 0o600); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(dir, "data")
	applyHooks(t, data, path)

	// The user adds a hook and a setting afterwards, including one inside an event Trove also uses.
	m := readJSON(t, path)
	m["theme"] = "dark"
	hooks := m["hooks"].(map[string]any)
	hooks["SessionEnd"] = append(hooks["SessionEnd"].([]any), map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "/home/me/bye.sh"}}})
	hooks["UserPromptSubmit"] = []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "/home/me/prompt.sh"}}}}
	b, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := Undo(data)
	if err != nil || len(res) != 1 || res[0].Action != "removed_our_lines" {
		t.Fatalf("undo = %+v, %v", res, err)
	}
	got := readJSON(t, path)
	if got["theme"] != "dark" || got["model"] != "opus" {
		t.Fatalf("user keys lost: %v", got)
	}
	for ev, want := range map[string][]string{
		"PreToolUse":       {"/home/me/audit-reads.sh", "/home/me/no-rm.sh"},
		"Stop":             {"/home/me/notify.sh"},
		"SessionEnd":       {"/home/me/bye.sh"},
		"UserPromptSubmit": {"/home/me/prompt.sh"},
		"PostToolUse":      nil,
		"SessionStart":     nil,
	} {
		if g := commands(got, ev); strings.Join(g, "|") != strings.Join(want, "|") {
			t.Errorf("%s = %v, want %v", ev, g, want)
		}
	}
	for _, g := range []string{"PostToolUse", "SessionStart"} {
		if _, ok := got["hooks"].(map[string]any)[g]; ok {
			t.Errorf("empty event %s was left behind", g)
		}
	}
}

func TestHooksCreateAndRemoveTheFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(dir, "data")
	applyHooks(t, data, path)
	if got := commands(readJSON(t, path), "SessionEnd"); len(got) != 1 {
		t.Fatalf("SessionEnd = %v", got)
	}
	if _, err := Undo(data); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the file Trove created should be removed: %v", err)
	}
}

func TestHooksRefuseWhatTheyCannotParse(t *testing.T) {
	for name, content := range map[string]string{
		"invalid JSON":       `{"hooks": `,
		"hooks is a list":    `{"hooks": []}`,
		"event is an object": `{"hooks": {"PreToolUse": {"matcher": "Read"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if e, err := PlanClaudeHooks(path, hookSpecs(), hookBase); err == nil {
				t.Fatalf("expected an error, got a plan: %+v", e)
			}
			if b, _ := os.ReadFile(path); string(b) != content {
				t.Fatal("the file was modified")
			}
		})
	}
}

func TestCodexHooksUseHooksJSONAndStepAsideForInlineHooks(t *testing.T) {
	dir := t.TempDir()
	specs := []HookSpec{{Event: "SessionEnd", Command: hookBase + "session_end_receipt --agent codex", Timeout: 3}}
	e, err := PlanCodexHooks(dir, specs, hookBase)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(e.Path) != "hooks.json" {
		t.Fatalf("path = %s", e.Path)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("model = \"x\"\n[[hooks.PreToolUse]]\nmatcher = \"^Bash$\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = PlanCodexHooks(dir, specs, hookBase)
	var inline *InlineHooksError
	if err == nil || !asInline(err, &inline) {
		t.Fatalf("expected InlineHooksError, got %v", err)
	}
	// A config.toml without hooks is fine.
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("model = \"x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanCodexHooks(dir, specs, hookBase); err != nil {
		t.Fatal(err)
	}
}

func asInline(err error, target **InlineHooksError) bool {
	e, ok := err.(*InlineHooksError)
	*target = e
	return ok
}

func TestHooksInstalledListsOnlyHooks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	data := filepath.Join(dir, "data")
	applyHooks(t, data, path)
	cs, err := HooksInstalled(data)
	if err != nil || len(cs) != 1 || cs[0].Agent != ClaudeHooks {
		t.Fatalf("%+v, %v", cs, err)
	}
}
