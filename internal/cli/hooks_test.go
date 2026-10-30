package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const userClaudeSettings = `{
  "model": "opus",
  "hooks": {
    "PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "/home/me/no-rm.sh"}]}],
    "SessionEnd": [{"hooks": [{"type": "command", "command": "/home/me/bye.sh"}]}]
  }
}
`

func hookClaudeSettings(home string) string { return filepath.Join(home, ".claude", "settings.json") }
func codexHooksFile(home string) string     { return filepath.Join(home, ".codex", "hooks.json") }

func hookCommandsIn(t *testing.T, path, event string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("%s: %v\n%s", path, err, b)
	}
	var out []string
	for _, g := range doc.Hooks[event] {
		for _, h := range g.Hooks {
			out = append(out, g.Matcher+"|"+h.Command)
		}
	}
	return out
}

func TestHooksInstallDryRunChangesNothing(t *testing.T) {
	h, _, home := mcpHarness(t)
	if code := h.run("hooks", "install", "--dry-run"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	out := h.out.String()
	for _, want := range []string{"Claude Code", "Codex CLI", "PreToolUse", "SessionEnd", "Dry run"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	for _, p := range []string{hookClaudeSettings(home), codexHooksFile(home)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("dry run wrote %s", p)
		}
	}
}

func TestHooksInstallAsksFirstAndNeedsConsent(t *testing.T) {
	h, _, home := mcpHarness(t)
	if code := h.run("hooks", "install"); code != 0 { // no stdin: the confirmation reads "no"
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if !strings.Contains(h.out.String(), "No changes made") {
		t.Errorf("output: %s", h.out)
	}
	if _, err := os.Stat(hookClaudeSettings(home)); !os.IsNotExist(err) {
		t.Error("wrote without consent")
	}
	h2, _, home2 := mcpHarness(t)
	if code := h2.run("--json", "hooks", "install"); code != 0 { // --json without --yes applies nothing
		t.Fatalf("exit %d: %s", code, h2.err)
	}
	if _, err := os.Stat(hookClaudeSettings(home2)); !os.IsNotExist(err) {
		t.Error("--json alone must not apply")
	}
}

func TestHooksInstallAndUninstallKeepUserHooks(t *testing.T) {
	h, _, home := mcpHarness(t)
	if err := os.WriteFile(hookClaudeSettings(home), []byte(userClaudeSettings), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := h.run("hooks", "install", "--yes"); code != 0 {
		t.Fatalf("exit %d: %s\n%s", code, h.err, h.out)
	}

	pre := hookCommandsIn(t, hookClaudeSettings(home), "PreToolUse")
	if len(pre) != 2 || pre[0] != "Bash|/home/me/no-rm.sh" || !strings.HasPrefix(pre[1], "Read|") || !strings.Contains(pre[1], "hook pre_tool_repeat_read") {
		t.Fatalf("PreToolUse = %v", pre)
	}
	end := hookCommandsIn(t, hookClaudeSettings(home), "SessionEnd")
	if len(end) != 2 || end[0] != "|/home/me/bye.sh" || !strings.Contains(end[1], "hook session_end_receipt") {
		t.Fatalf("SessionEnd = %v", end)
	}
	for _, ev := range []string{"PostToolUse", "SessionStart", "PreCompact"} {
		if got := hookCommandsIn(t, hookClaudeSettings(home), ev); len(got) != 1 {
			t.Errorf("%s = %v", ev, got)
		}
	}
	cx := hookCommandsIn(t, codexHooksFile(home), "SessionEnd")
	if len(cx) != 1 || !strings.Contains(cx[0], "--agent codex") {
		t.Fatalf("codex SessionEnd = %v", cx)
	}
	if got := hookCommandsIn(t, codexHooksFile(home), "PreToolUse"); len(got) != 0 {
		t.Fatalf("codex has no file-read tool to hook, and large-output is off: %v", got)
	}
	if backups, _ := filepath.Glob(filepath.Join(home, ".trove", "backups", "*settings.json.bak")); len(backups) != 1 {
		t.Errorf("expected a backup of the user's settings, found %v", backups)
	}
	if _, err := os.Stat(filepath.Join(home, ".trove", "hooks", "config.json")); err != nil {
		t.Errorf("the hook snapshot was not written: %v", err)
	}
	if !strings.Contains(h.out.String(), "hook_repeat_read") || !strings.Contains(h.out.String(), "/hooks") {
		t.Errorf("the next steps should mention the shadow setting and Codex's /hooks review:\n%s", h.out)
	}

	// Installing again changes nothing.
	h.out.Reset()
	if code := h.run("hooks", "install", "--yes"); code != 0 || !strings.Contains(h.out.String(), "Already installed") {
		t.Fatalf("exit %d: %s", code, h.out)
	}

	// status sees the files.
	h.out.Reset()
	if code := h.run("--json", "hooks", "status"); code != 0 {
		t.Fatalf("status exit %d: %s", code, h.err)
	}
	var st hooksStatusReport
	if err := json.Unmarshal(h.out.Bytes(), &st); err != nil || len(st.Installed) != 2 || st.Optimizers["hook_repeat_read"] != "off" {
		t.Fatalf("status = %+v (%v)\n%s", st, err, h.out)
	}

	h.out.Reset()
	if code := h.run("hooks", "uninstall"); code != 0 {
		t.Fatalf("uninstall exit %d: %s", code, h.err)
	}
	b, _ := os.ReadFile(hookClaudeSettings(home))
	if string(b) != userClaudeSettings {
		t.Fatalf("the user's settings were not restored:\n%s", b)
	}
	if _, err := os.Stat(codexHooksFile(home)); !os.IsNotExist(err) {
		t.Error("the Codex hooks.json Trove created should be gone")
	}
	h.out.Reset()
	if code := h.run("hooks", "uninstall"); code != 0 || !strings.Contains(h.out.String(), "Nothing to remove") {
		t.Fatalf("second uninstall: %d %s", code, h.out)
	}
}

func TestHooksUndoRemovesThemToo(t *testing.T) {
	h, _, home := mcpHarness(t, "claude")
	if err := os.WriteFile(hookClaudeSettings(home), []byte(userClaudeSettings), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := h.run("hooks", "install", "--yes", "--agent", "claude"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if code := h.run("undo", "--all"); code != 0 {
		t.Fatalf("undo exit %d: %s", code, h.err)
	}
	if b, _ := os.ReadFile(hookClaudeSettings(home)); string(b) != userClaudeSettings {
		t.Fatalf("not restored:\n%s", b)
	}
}

func TestHooksLargeOutputIsOptIn(t *testing.T) {
	h, _, home := mcpHarness(t)
	if code := h.run("hooks", "install", "--yes", "--large-output"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if got := hookCommandsIn(t, hookClaudeSettings(home), "PreToolUse"); len(got) != 2 || !strings.HasPrefix(got[1], "Bash|") {
		t.Fatalf("Claude PreToolUse = %v", got)
	}
	if got := hookCommandsIn(t, codexHooksFile(home), "PreToolUse"); len(got) != 1 || !strings.Contains(got[0], "pre_tool_large_output") {
		t.Fatalf("Codex PreToolUse = %v", got)
	}
}

func TestHooksSkipCodexWithInlineHooks(t *testing.T) {
	h, _, home := mcpHarness(t)
	if err := os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte("[[hooks.Stop]]\n[[hooks.Stop.hooks]]\ntype = \"command\"\ncommand = \"x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := h.run("hooks", "install", "--yes"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if !strings.Contains(h.out.String(), "already defines [hooks] inline") {
		t.Errorf("the skip should be explained:\n%s", h.out)
	}
	if _, err := os.Stat(codexHooksFile(home)); !os.IsNotExist(err) {
		t.Error("created hooks.json next to inline hooks")
	}
	if got := hookCommandsIn(t, hookClaudeSettings(home), "SessionEnd"); len(got) != 1 {
		t.Errorf("Claude Code should still be installed: %v", got)
	}
}

func TestHooksRefuseBadSettingsWithoutTouchingThem(t *testing.T) {
	h, _, home := mcpHarness(t, "claude")
	if err := os.WriteFile(hookClaudeSettings(home), []byte(`{"hooks": [`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := h.run("hooks", "install", "--yes", "--agent", "claude"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if !strings.Contains(h.out.String(), "not valid JSON") {
		t.Errorf("output: %s", h.out)
	}
	if b, _ := os.ReadFile(hookClaudeSettings(home)); string(b) != `{"hooks": [` {
		t.Errorf("file was changed: %s", b)
	}
}

func TestHooksProjectScopeUsesLocalSettings(t *testing.T) {
	h, _, home := mcpHarness(t, "claude")
	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if code := h.run("hooks", "install", "--yes", "--agent", "claude", "--scope", "project", "--project", proj); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if got := hookCommandsIn(t, filepath.Join(proj, ".claude", "settings.local.json"), "SessionEnd"); len(got) != 1 {
		t.Fatalf("SessionEnd = %v", got)
	}
	if _, err := os.Stat(hookClaudeSettings(home)); !os.IsNotExist(err) {
		t.Error("project scope must not touch user settings")
	}
}

func TestHooksRejectBadFlags(t *testing.T) {
	h, _, _ := mcpHarness(t)
	for _, args := range [][]string{{"hooks", "install", "--agent", "vim"}, {"hooks", "install", "--scope", "galaxy"}} {
		if code := h.run(args...); code == 0 {
			t.Errorf("%v should fail", args)
		}
	}
}

// The hidden `trove hook` command is the same entry point the agents run.
func TestHookCommandAlwaysExitsZeroAndStaysQuiet(t *testing.T) {
	h, _, _ := mcpHarness(t)
	h.app.In = strings.NewReader(`{"session_id":"s1","hook_event_name":"SessionStart","source":"startup"}`)
	if code := h.run("hook", "session_reset"); code != 0 || h.out.Len() != 0 {
		t.Fatalf("exit %d out=%q err=%q", code, h.out, h.err)
	}
	h.app.In = strings.NewReader("garbage")
	if code := h.run("hook", "pre_tool_repeat_read"); code != 0 || h.out.Len() != 0 {
		t.Fatalf("exit %d out=%q", code, h.out)
	}
	if code := h.run("hook", "no_such_event"); code != 0 {
		t.Fatalf("exit %d", code)
	}
}
