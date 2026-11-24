package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
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

// TestHooksVerifyPromptContextStoresTheGate plays the user's session through the real `trove hook probe_context`
// entry and checks the result is stored beside an earlier block result, not over it.
func TestHooksVerifyPromptContextStoresTheGate(t *testing.T) {
	h := newHarness(t, map[string]string{"ANTHROPIC_API_KEY": ""})
	if code := h.run("hooks", "verify", "--prompt-context", "--json"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	var rep struct {
		Armed struct {
			State    string    `json:"state_dir"`
			Settings string    `json:"settings_file"`
			Prompts  [4]string `json:"prompts"`
		} `json:"armed"`
	}
	if err := json.Unmarshal(h.out.Bytes(), &rep); err != nil || rep.Armed.Settings == "" || rep.Armed.Prompts[3] == "" {
		t.Fatalf("%v: %s", err, h.out)
	}
	dir := filepath.Dir(filepath.Dir(rep.Armed.State))
	if err := hooks.SaveCapabilities(dir, hooks.Capabilities{V: hookcaps.Version, Entries: []hooks.Capability{
		{Agent: "claude", Tool: hookcaps.ToolPrompt, Block: hookcaps.Yes, Method: "session", Evidence: "block seen"}}}); err != nil {
		t.Fatal(err)
	}

	transcript := filepath.Join(t.TempDir(), "t.jsonl")
	hook := func(in string) string {
		var out bytes.Buffer
		if !runProbeHook([]string{"probe_context", "--state", rep.Armed.State}, strings.NewReader(in), &out) {
			t.Fatal("probe_context was not routed")
		}
		return out.String()
	}
	hook(`{"hook_event_name":"SessionStart","model":"claude-opus-5-5"}`)
	var lines []string
	user := func(p string) string {
		b, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": p}})
		return string(b)
	}
	reply := func(in, read, write int) string {
		return fmt.Sprintf(`{"type":"assistant","message":{"model":"claude-opus-5-5","content":[],"usage":{"input_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d}}}`, in, read, write)
	}
	for i, p := range rep.Armed.Prompts {
		in, _ := json.Marshal(map[string]any{"hook_event_name": "UserPromptSubmit", "transcript_path": transcript, "prompt": p})
		out := hook(string(in))
		switch i {
		case 0:
			lines = append(lines, user(p), reply(3, 0, 20010))
		case 1:
			var v struct {
				H struct {
					Context string `json:"additionalContext"`
				} `json:"hookSpecificOutput"`
			}
			if json.Unmarshal([]byte(out), &v) != nil || !strings.HasPrefix(v.H.Context, "TROVE_PROBE_CTX_") {
				t.Fatalf("prompt 2 reply: %s", out)
			}
			ctx, _ := json.Marshal(map[string]any{"type": "attachment", "content": v.H.Context})
			lines = append(lines, string(ctx), user(p), reply(3, 20013, 330))
		case 2:
			lines = append(lines, user(p), reply(3, 20346, 20))
			if err := os.WriteFile(transcript, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	h.reset()
	if code := h.run("hooks", "verify", "--prompt-context", "--finish"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	caps := hooks.LoadCapabilities(dir)
	if !caps.Supports("claude", hookcaps.ToolPrompt, hookcaps.PromptContext) {
		t.Fatalf("the context gate was not stored: %+v\n%s", caps, h.out)
	}
	if !caps.Supports("claude", hookcaps.ToolPrompt, hookcaps.PromptBlock) {
		t.Fatalf("the earlier block result was lost: %+v", caps)
	}
	if !strings.Contains(h.out.String(), "may now send its text") {
		t.Errorf("output: %s", h.out)
	}
	if _, err := os.Stat(rep.Armed.Settings); err == nil {
		t.Fatal("the test settings file was left behind")
	}
}

// A later block check keeps the context result in the same UserPromptSubmit entry.
func TestBlockCheckKeepsTheContextResult(t *testing.T) {
	old := hooks.Capabilities{V: hookcaps.Version, Entries: []hooks.Capability{
		{Agent: "claude", Tool: hookcaps.ToolPrompt, Context: hookcaps.Yes, ContextEvidence: "ctx seen", Method: "session"}}}
	got := mergeTools(old, []hooks.Capability{{Agent: "claude", Tool: hookcaps.ToolPrompt, Block: hookcaps.No, Method: "session"}})
	e, _ := got.Find("claude", hookcaps.ToolPrompt)
	if e.Context != hookcaps.Yes || e.ContextEvidence != "ctx seen" || e.Block != hookcaps.No || len(got.Entries) != 1 {
		t.Errorf("merged %+v", got.Entries)
	}
}

func TestPromptChecksRunOneAtATime(t *testing.T) {
	h := newHarness(t, nil)
	if code := h.run("hooks", "verify", "--prompt-block", "--prompt-context"); code == 0 {
		t.Fatal("both checks at once must be refused")
	}
}
