package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/hookcaps"
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

func TestHooksVerifyPromptBlockStoresTheGate(t *testing.T) {
	h := newHarness(t, map[string]string{"ANTHROPIC_API_KEY": ""})
	if code := h.run("hooks", "verify", "--prompt-block", "--json"); code != 0 {
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
	// Play the user's runs through the real `trove hook probe_prompt` entry: two interactive prompts, then -p.
	for _, entry := range []string{"cli", "cli", "sdk-cli"} {
		t.Setenv(hookprobe.EntrypointEnv, entry)
		var out bytes.Buffer
		if !runProbeHook([]string{"probe_prompt", "--state", rep.Armed.State}, strings.NewReader(`{"session_id":"s","prompt":"x"}`), &out) {
			t.Fatal("probe_prompt was not routed")
		}
	}
	h.reset()
	if code := h.run("hooks", "verify", "--prompt-block", "--finish", "--saw-block", "maybe"); code == 0 {
		t.Fatal("an answer other than yes or no must be refused")
	}
	h.reset()
	if code := h.run("hooks", "verify", "--prompt-block", "--finish", "--saw-block", "yes", "--saw-system", "no"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	dir := filepath.Dir(filepath.Dir(rep.Armed.State))
	caps := hooks.LoadCapabilities(dir)
	if !caps.Supports("claude", hookcaps.ToolPrompt, hookcaps.PromptBlock) {
		t.Fatalf("the gate was not stored: %+v", caps)
	}
	if !strings.Contains(h.out.String(), "may now stop the first prompt") {
		t.Errorf("output: %s", h.out)
	}
	if _, err := os.Stat(rep.Armed.Settings); err == nil {
		t.Fatal("the test settings file was left behind")
	}
}

// A later `trove hooks probe` writes the documented entry again; it must not wipe a result from the user's session.
func TestProbeKeepsAPromptBlockResultFromASession(t *testing.T) {
	old := hooks.Capabilities{V: hookcaps.Version, Entries: []hooks.Capability{
		{Agent: "claude", Tool: hookcaps.ToolPrompt, Block: hookcaps.Yes, Method: "session"},
		{Agent: "claude", Tool: "Bash", InputRewrite: hookcaps.Yes, Method: "live"},
		{Agent: "codex", Tool: "Bash", Method: "docs"},
	}}
	fresh := hooks.Capabilities{Entries: []hooks.Capability{
		{Agent: "claude", Tool: hookcaps.ToolPrompt, Block: hookcaps.Untested, Method: "docs"},
		{Agent: "claude", Tool: "Bash", InputRewrite: hookcaps.No, Method: "live"},
	}}
	got := mergeCapabilities(old, fresh)
	if e, _ := got.Find("claude", hookcaps.ToolPrompt); e.Method != "session" || e.Block != hookcaps.Yes {
		t.Errorf("prompt entry %+v", e)
	}
	if e, _ := got.Find("claude", "Bash"); e.InputRewrite != hookcaps.No {
		t.Errorf("a re-probed tool takes the fresh result: %+v", e)
	}
	if _, ok := got.Find("codex", "Bash"); !ok {
		t.Error("an agent that was not probed keeps its entries")
	}
	n := 0
	for _, e := range got.Entries {
		if e.Tool == hookcaps.ToolPrompt {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d prompt entries", n)
	}
}
