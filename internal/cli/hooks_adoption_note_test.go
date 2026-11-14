package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/setup"
)

// An install from before the trove_run counter existed cannot show whether hook_large_output's hint is followed.
// `trove hooks status` says so and names the fix, and the note goes once the counter is installed.
func TestHooksStatusSaysWhenTheRunCounterIsMissing(t *testing.T) {
	h := newHarness(t, nil)
	dir := apDir(h)
	settings := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(settings, []byte(`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"trove hook pre_tool_large_output"}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[optimizers]\nhook_large_output = \"on\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal([]setup.Change{{ID: "h1", Agent: setup.ClaudeHooks, Path: settings}})
	if err := os.WriteFile(filepath.Join(dir, "changes.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := h.run("hooks", "status"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if !strings.Contains(h.out.String(), "do not count use of trove_run") || !strings.Contains(h.out.String(), "trove hooks install") {
		t.Fatalf("no note about the missing counter:\n%s", h.out)
	}
	if err := os.WriteFile(settings, []byte(`{"hooks":{"PostToolUse":[{"matcher":"mcp__trove__trove_run","hooks":[{"type":"command","command":"trove hook post_tool_run"}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h.reset()
	h.run("hooks", "status")
	if strings.Contains(h.out.String(), "do not count use of trove_run") {
		t.Fatalf("the note must go once the counter is installed:\n%s", h.out)
	}
}
