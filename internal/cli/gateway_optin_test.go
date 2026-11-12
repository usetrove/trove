package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// readClaudeEnv returns the env block of a Claude Code settings file.
func readClaudeEnv(t *testing.T, path string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("settings not JSON: %v", err)
	}
	return doc.Env
}

// Setup never points Claude Code at the gateway unless the user asks with --gateway.
func TestSetupDoesNotPointClaudeCodeAtTheGatewayUnlessAskedTo(t *testing.T) {
	h, claudePath := setupWith(t, nil, `{"theme":"dark"}`)
	if code := h.run("setup", "--yes"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if _, set := readClaudeEnv(t, claudePath)["ANTHROPIC_BASE_URL"]; set {
		t.Fatal("setup without --gateway pointed Claude Code at the gateway")
	}
	if !strings.Contains(h.out.String(), "trove setup --gateway") {
		t.Errorf("the output should say how to opt in: %q", h.out)
	}

	if code := h.run("setup", "--gateway", "--yes"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	got := readClaudeEnv(t, claudePath)["ANTHROPIC_BASE_URL"]
	if !strings.HasPrefix(got, "http://127.0.0.1:") || !strings.HasSuffix(got, "/anthropic") {
		t.Fatalf("with --gateway, ANTHROPIC_BASE_URL should be the local gateway, got a non-matching value")
	}
}

// `trove gateway stop` puts back the value setup replaced, as undo would.
func TestGatewayStopPutsBackThePreviousBaseURL(t *testing.T) {
	const previous = "https://api.anthropic.com"
	settings := `{"env":{"ANTHROPIC_BASE_URL":"` + previous + `"}}`
	h, claudePath := setupWith(t, nil, settings)
	if code := h.run("setup", "--gateway", "--yes"); code != 0 {
		t.Fatalf("setup exit %d: %s", code, h.err)
	}
	if readClaudeEnv(t, claudePath)["ANTHROPIC_BASE_URL"] == previous {
		t.Fatal("setup did not change the value, so this test proves nothing")
	}
	if code := h.run("gateway", "stop"); code != 0 {
		t.Fatalf("gateway stop exit %d: %s", code, h.err)
	}
	if got := readClaudeEnv(t, claudePath)["ANTHROPIC_BASE_URL"]; got != previous {
		t.Fatalf("after gateway stop the value is not the previous one")
	}
}

// When there was no value before, gateway stop removes the key it added.
func TestGatewayStopRemovesTheKeyWhenThereWasNone(t *testing.T) {
	h, claudePath := setupWith(t, nil, `{"theme":"dark"}`)
	if code := h.run("setup", "--gateway", "--yes"); code != 0 {
		t.Fatalf("setup exit %d: %s", code, h.err)
	}
	if _, set := readClaudeEnv(t, claudePath)["ANTHROPIC_BASE_URL"]; !set {
		t.Fatal("setup did not add the key, so this test proves nothing")
	}
	if code := h.run("gateway", "stop"); code != 0 {
		t.Fatalf("gateway stop exit %d: %s", code, h.err)
	}
	if _, set := readClaudeEnv(t, claudePath)["ANTHROPIC_BASE_URL"]; set {
		t.Fatal("after gateway stop the key Trove added is still there")
	}
}
