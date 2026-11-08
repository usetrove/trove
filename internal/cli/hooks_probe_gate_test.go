package cli

import (
	"strings"
	"testing"
)

// A live probe spends money on ANTHROPIC_API_KEY. Only a recognised Console API key may start one.
// Every key below is a fake; none is a real credential.
func TestLiveProbeRefusesEveryKeyThatIsNotAConsoleKey(t *testing.T) {
	refused := map[string]string{
		"an unknown prefix (sk-ant-usr)":        "sk-ant-usr01-FAKEUSRKEYFORTESTS0123456789abcdefghijk",
		"an OAuth token (sk-ant-oat)":           "sk-ant-oat01-FAKEOAUTHKEYFORTESTS0123456789abcdefgh",
		"an admin key (sk-ant-admin)":           "sk-ant-admin01-FAKEADMINKEYFORTESTS0123456789abcdef",
		"a Console prefix that is too short":    "sk-ant-api03-SHORT",
		"a bare word from a test fixture":       "test-value-not-a-key",
		"an OpenAI key placed in the Anthropic": "sk-proj-FAKEOPENAIKEYFORTESTS0123456789abcdefgh",
	}
	for name, key := range refused {
		h := newHarness(t, map[string]string{"ANTHROPIC_API_KEY": key}, "claude")
		code := h.run("hooks", "probe", "--live", "--max-usd", "0.25", "--claude", "/nonexistent/claude")
		if code == 0 {
			t.Errorf("%s: a live probe ran with it", name)
		}
		msg := h.err.String()
		if !strings.Contains(msg, "is not a Claude Console API key") {
			t.Errorf("%s: wrong refusal: %q", name, msg)
		}
		// It must stop before starting the agent: no "cannot run" (that would mean the gate passed).
		if strings.Contains(msg, "cannot run") {
			t.Errorf("%s: the gate let it reach the agent", name)
		}
		// The key's value never appears in output.
		if strings.Contains(h.out.String()+msg, key) {
			t.Errorf("%s: the key value was printed", name)
		}
	}
}

func TestLiveProbeAcceptsAConsoleKeyAndGetsToTheAgent(t *testing.T) {
	const consoleKey = "sk-ant-api03-FAKECONSOLEKEYFORTESTS0123456789abcdefghijk"
	h := newHarness(t, map[string]string{"ANTHROPIC_API_KEY": consoleKey}, "claude")
	code := h.run("hooks", "probe", "--live", "--max-usd", "0.25", "--claude", "/nonexistent/claude")
	if code == 0 {
		t.Fatalf("expected the run to fail at the missing agent binary, got success")
	}
	// Passing the gate is shown by the failure coming from starting the agent, not from the key check.
	if !strings.Contains(h.err.String(), "cannot run") {
		t.Errorf("a Console key should pass the gate and fail only at the agent: %q", h.err)
	}
	if strings.Contains(h.out.String()+h.err.String(), consoleKey) {
		t.Error("the key value was printed")
	}
}

func TestLiveProbeWithNoKeyStillRefusesWithTheEvidenceMessage(t *testing.T) {
	h := newHarness(t, map[string]string{"ANTHROPIC_API_KEY": ""}, "claude")
	if code := h.run("hooks", "probe", "--live", "--max-usd", "0.25", "--claude", "/nonexistent/claude"); code == 0 {
		t.Fatal("a live probe ran without a key")
	}
	if !strings.Contains(h.err.String(), "ANTHROPIC_API_KEY") {
		t.Errorf("the refusal should say which variable is needed: %q", h.err)
	}
}
