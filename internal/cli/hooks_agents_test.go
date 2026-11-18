package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func cursorHookEntries(t *testing.T, path string) map[string][]string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Version int `json:"version"`
		Hooks   map[string][]struct {
			Command string `json:"command"`
			Matcher string `json:"matcher"`
			Timeout int    `json:"timeout"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(b, &doc); err != nil || doc.Version != 1 {
		t.Fatalf("%s: version=%d err=%v\n%s", path, doc.Version, err, b)
	}
	out := map[string][]string{}
	for ev, list := range doc.Hooks {
		for _, h := range list {
			if h.Timeout <= 0 || h.Timeout > 60 {
				t.Errorf("%s: Cursor timeouts are seconds, got %d", ev, h.Timeout)
			}
			out[ev] = append(out[ev], h.Matcher+"|"+h.Command)
		}
	}
	return out
}

func TestHooksInstallCursorAndGemini(t *testing.T) {
	h, _, home := mcpHarness(t)
	for _, d := range []string{".cursor", ".gemini"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if code := h.run("hooks", "install", "--yes", "--agent", "cursor"); code != 0 {
		t.Fatalf("exit %d: %s\n%s", code, h.err, h.out)
	}
	if _, err := os.Stat(hookClaudeSettings(home)); !os.IsNotExist(err) {
		t.Fatal("--agent cursor touched Claude Code")
	}
	cur := cursorHookEntries(t, filepath.Join(home, ".cursor", "hooks.json"))
	if got := cur["preToolUse"]; len(got) != 1 || !strings.HasPrefix(got[0], "Read|") || !strings.Contains(got[0], " hook pre_tool_repeat_read ") || !strings.HasSuffix(got[0], "--agent cursor") {
		t.Fatalf("preToolUse = %v", got)
	}
	for _, ev := range []string{"postToolUse", "sessionStart", "sessionEnd", "preCompact"} {
		if len(cur[ev]) == 0 {
			t.Errorf("Cursor %s missing: %v", ev, cur)
		}
	}
	for _, list := range cur {
		for _, c := range list {
			if strings.Contains(c, "post_tool_output") || strings.Contains(c, "pre_tool_large_output") {
				t.Errorf("rewriting hooks need --rewrite: %s", c)
			}
		}
	}

	if code := h.run("hooks", "install", "--yes", "--agent", "gemini", "--rewrite"); code != 0 {
		t.Fatalf("exit %d: %s\n%s", code, h.err, h.out)
	}
	gem := filepath.Join(home, ".gemini", "settings.json")
	before := hookCommandsIn(t, gem, "BeforeTool")
	if len(before) != 2 || !strings.HasPrefix(before[0], "read_file|") || !strings.HasSuffix(before[0], "--agent gemini") ||
		!strings.HasPrefix(before[1], "run_shell_command|") {
		t.Fatalf("BeforeTool = %v", before)
	}
	var out bool
	for _, c := range hookCommandsIn(t, gem, "AfterTool") {
		out = out || (strings.Contains(c, "post_tool_output") && strings.HasPrefix(c, "run_shell_command|grep_search|search_file_content|glob|"))
	}
	if !out {
		t.Fatalf("AfterTool = %v", hookCommandsIn(t, gem, "AfterTool"))
	}
	b, _ := os.ReadFile(gem)
	if !strings.Contains(string(b), `"timeout": 5000`) {
		t.Fatalf("Gemini CLI timeouts are milliseconds:\n%s", b)
	}
	for _, ev := range []string{"SessionStart", "SessionEnd", "PreCompress"} {
		if len(hookCommandsIn(t, gem, ev)) != 1 {
			t.Errorf("Gemini %s missing", ev)
		}
	}

	if code := h.run("hooks", "uninstall"); code != 0 {
		t.Fatalf("uninstall exit %d: %s", code, h.err)
	}
	for _, p := range []string{filepath.Join(home, ".cursor", "hooks.json"), gem} {
		if b, err := os.ReadFile(p); err == nil && strings.Contains(string(b), " hook ") {
			t.Errorf("uninstall left Trove's hooks in %s:\n%s", p, b)
		}
	}
}

func TestHooksInstallSkipsCursorAndGeminiWhenAbsent(t *testing.T) {
	h, _, _ := mcpHarness(t)
	if code := h.run("hooks", "install", "--dry-run"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if out := h.out.String(); strings.Contains(out, "\nCursor\n") || strings.Contains(out, "\nGemini CLI\n") || strings.Contains(out, "--agent gemini") || strings.Contains(out, "--agent cursor") {
		t.Fatalf("listed an agent that is not installed:\n%s", out)
	}
}
