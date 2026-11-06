package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `trove setup` must never point Claude Code at the gateway unless Claude Code will authenticate with an API key:
// otherwise its Claude.ai login token would be sent to the gateway before the gateway refuses it (CLAUDE.md
// rules 3 and 10, docs/compliance.md question 1).

func setupWith(t *testing.T, env map[string]string, settings string) (h *harness, claudePath string) {
	t.Helper()
	h = newHarness(t, env, "claude")
	claudePath, _ = agentHome(t, h)
	if settings != "" {
		if err := os.WriteFile(claudePath, []byte(settings), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return h, claudePath
}

func TestSetupRefusesClaudeCodeWithoutAnAPIKeyAndLeavesItUntouched(t *testing.T) {
	h, claudePath := setupWith(t, map[string]string{"ANTHROPIC_API_KEY": ""}, "")
	before, _ := os.ReadFile(claudePath)
	if code := h.run("setup", "--yes"); code != 0 {
		t.Fatalf("exit %d: %s %s", code, h.out, h.err)
	}
	after, _ := os.ReadFile(claudePath)
	if string(after) != string(before) {
		t.Fatalf("Claude Code's settings were changed without an API key:\n%s", after)
	}
	out := h.out.String()
	for _, want := range []string{"Claude.ai login", "ANTHROPIC_API_KEY", "plugin", "plan = \"api_key\""} {
		if !strings.Contains(out, want) {
			t.Errorf("the refusal must say %q:\n%s", want, out)
		}
	}
}

func TestSetupAcceptsOnlyRealEvidenceOfAnAPIKey(t *testing.T) {
	apiKeyHelper := `{"apiKeyHelper": "/usr/local/bin/get-key"}` + "\n"
	cases := []struct {
		name     string
		env      map[string]string
		settings string
		config   string
		applies  bool
	}{
		{"ANTHROPIC_API_KEY set", map[string]string{"ANTHROPIC_API_KEY": "x"}, "", "", true},
		{"apiKeyHelper in settings", map[string]string{"ANTHROPIC_API_KEY": ""}, apiKeyHelper, "", true},
		{"console login forced", map[string]string{"ANTHROPIC_API_KEY": ""}, `{"forceLoginMethod": "console"}` + "\n", "", true},
		{"the user says so in config.toml", map[string]string{"ANTHROPIC_API_KEY": ""}, "", "[history]\nplan = \"api_key\"\n", true},
		{"nothing at all", map[string]string{"ANTHROPIC_API_KEY": ""}, "", "", false},
		{"a bearer-only auth token is not an API key", map[string]string{"ANTHROPIC_API_KEY": "", "ANTHROPIC_AUTH_TOKEN": "t"}, "", "", false},
		{"Claude.ai login forced", map[string]string{"ANTHROPIC_API_KEY": ""}, `{"forceLoginMethod": "claudeai"}` + "\n", "", false},
		{"Claude.ai login forced even with a key in the environment", map[string]string{"ANTHROPIC_API_KEY": "x"}, `{"forceLoginMethod": "claudeai"}` + "\n", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, claudePath := setupWith(t, c.env, c.settings)
			if c.config != "" {
				dir := h.app.Getenv("TROVE_DATA_DIR")
				_ = os.MkdirAll(dir, 0o700)
				if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(c.config), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if code := h.run("setup", "--yes"); code != 0 {
				t.Fatalf("exit %d: %s %s", code, h.out, h.err)
			}
			got, _ := os.ReadFile(claudePath)
			applied := strings.Contains(string(got), "ANTHROPIC_BASE_URL")
			if applied != c.applies {
				t.Errorf("gateway endpoint written = %v, want %v\nsettings: %s\noutput: %s", applied, c.applies, got, h.out)
			}
		})
	}
}

// Codex CLI is unaffected: Trove's provider entry reads the key from OPENAI_API_KEY, so a ChatGPT login is never used with it.
func TestSetupStillConfiguresCodexWhenClaudeCodeIsRefused(t *testing.T) {
	h := newHarness(t, map[string]string{"ANTHROPIC_API_KEY": ""}, "claude", "codex")
	claudePath, codexPath := agentHome(t, h)
	if code := h.run("setup", "--yes"); code != 0 {
		t.Fatalf("exit %d: %s %s", code, h.out, h.err)
	}
	if got, _ := os.ReadFile(claudePath); strings.Contains(string(got), "ANTHROPIC_BASE_URL") {
		t.Error("Claude Code was pointed at the gateway without an API key")
	}
	if got, _ := os.ReadFile(codexPath); !strings.Contains(string(got), "env_key") {
		t.Errorf("Codex CLI should still be configured (it authenticates with an API key from the environment):\n%s", got)
	}
}

// Re-running setup on a machine that is already set up is not blocked, so `trove undo` and status keep working.
func TestSetupAlreadyConfiguredIsNotARefusal(t *testing.T) {
	h, _ := setupWith(t, map[string]string{"ANTHROPIC_API_KEY": "x"}, "")
	if code := h.run("setup", "--yes"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	h2 := newHarness(t, map[string]string{"TROVE_DATA_DIR": h.app.Getenv("TROVE_DATA_DIR"), "ANTHROPIC_API_KEY": ""}, "claude")
	h2.app.HomeDir = h.app.HomeDir
	if code := h2.run("setup", "--yes"); code != 0 {
		t.Fatalf("exit %d: %s %s", code, h2.out, h2.err)
	}
	if strings.Contains(h2.out.String(), "Claude.ai login") {
		t.Errorf("an already configured setup should say so, not refuse:\n%s", h2.out)
	}
}
