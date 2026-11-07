package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/hooks"
	hookprobe "github.com/usetrove/trove/internal/hooks/probe"
)

func TestLiveProbeIsRefusedOnASubscriptionAndStartsNothing(t *testing.T) {
	h := newHarness(t, map[string]string{"ANTHROPIC_API_KEY": ""}, "claude")
	if code := h.run("hooks", "probe", "--live", "--max-usd", "0.25"); code == 0 {
		t.Fatalf("a live probe ran without any sign of an API key: %s", h.out)
	}
	msg := h.err.String()
	for _, want := range []string{"API key", "Claude.ai", "Nothing was run", "trove hooks verify"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal should mention %q: %s", want, msg)
		}
	}
}

func TestHooksVerifyPreparesASessionThenStoresWhatItRecorded(t *testing.T) {
	h := newHarness(t, map[string]string{"ANTHROPIC_API_KEY": ""})
	if code := h.run("hooks", "verify", "--tools", "Bash", "--json"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	var rep struct {
		Armed struct {
			State    string `json:"state_dir"`
			Settings string `json:"settings_file"`
		} `json:"armed"`
	}
	if err := json.Unmarshal(h.out.Bytes(), &rep); err != nil || rep.Armed.Settings == "" {
		t.Fatalf("%v: %s", err, h.out)
	}
	if _, err := os.Stat(rep.Armed.Settings); err != nil {
		t.Fatal(err)
	}
	// Play the user's session: the agent runs the test hooks, Bash honours the input rewrite.
	getenv := func(k string) string {
		if k == hookprobe.StateEnv {
			return rep.Armed.State
		}
		return ""
	}
	pre, _ := json.Marshal(map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "echo TROVE_PROBE_SRC"}, "session_id": "s"})
	var out bytes.Buffer
	hookprobe.HookMain("pre", bytes.NewReader(pre), &out, getenv)
	var o struct {
		H struct {
			U json.RawMessage `json:"updatedInput"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &o); err != nil {
		t.Fatalf("the test hook sent no rewrite: %v %q", err, out.String())
	}
	post, _ := json.Marshal(map[string]any{"tool_name": "Bash", "tool_input": o.H.U, "tool_response": map[string]any{"stdout": "x"}, "session_id": "s"})
	hookprobe.HookMain("post", bytes.NewReader(post), &bytes.Buffer{}, getenv)

	h.reset()
	if code := h.run("hooks", "verify", "--finish", "--saw-output", "Bash"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	dir := filepath.Dir(filepath.Dir(rep.Armed.State))
	caps := hooks.LoadCapabilities(dir)
	if !caps.Supports("claude", "Bash", hooks.RewriteInput) || !caps.Supports("claude", "Bash", hooks.RewriteOutput) {
		t.Fatalf("the session result was not stored: %+v", caps)
	}
	if e, _ := caps.Find("claude", "Bash"); e.Method != "session" {
		t.Fatalf("method = %q", e.Method)
	}
	if _, err := os.Stat(rep.Armed.Settings); err == nil {
		t.Fatal("the test settings file was left behind")
	}
}
