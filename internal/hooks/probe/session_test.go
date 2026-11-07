package probe

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/hooks"
)

var sessionNow = func() time.Time { return time.Unix(1_800_000_000, 0) }

// runSession plays the agent: it runs the armed hooks in process for each tool.
func runSession(t *testing.T, state string, tools []string, honourInput map[string]bool) {
	t.Helper()
	getenv := func(k string) string {
		if k == StateEnv {
			return state
		}
		return ""
	}
	for _, tool := range tools {
		ib, _ := json.Marshal(map[string]any{"pattern": "TROVE_PROBE_SRC", "command": "echo TROVE_PROBE_SRC", "file_path": "/p/probe.txt"})
		pre, _ := json.Marshal(map[string]any{"tool_name": tool, "tool_input": json.RawMessage(ib), "session_id": "s"})
		var out bytes.Buffer
		HookMain("pre", bytes.NewReader(pre), &out, getenv)
		ran := json.RawMessage(ib)
		if honourInput[tool] && out.Len() > 0 {
			var o struct {
				H struct {
					U json.RawMessage `json:"updatedInput"`
				} `json:"hookSpecificOutput"`
			}
			_ = json.Unmarshal(out.Bytes(), &o)
			ran = o.H.U
		}
		post, _ := json.Marshal(map[string]any{"tool_name": tool, "tool_input": ran, "tool_response": json.RawMessage(defaultShapes[tool]), "session_id": "s"})
		HookMain("post", bytes.NewReader(post), &bytes.Buffer{}, getenv)
	}
}

func armOpts() ArmOptions {
	return ArmOptions{Version: "2.1.999", Now: time.Now,
		HookCommand: func(mode, state string) (string, []string) {
			return "trove", []string{"hook", "probe_" + mode, "--state", state}
		}}
}

func TestVerifySessionRunsNothingAndRecordsWhatTheUserSessionDid(t *testing.T) {
	state := filepath.Join(t.TempDir(), "verify")
	armed, err := Arm(state, armOpts())
	if err != nil {
		t.Fatal(err)
	}
	if len(armed.Prompts) != len(DefaultTools) || !strings.Contains(armed.Prompts["Bash"], "echo TROVE_PROBE_SRC") {
		t.Fatalf("%+v", armed)
	}
	b, err := os.ReadFile(armed.SettingsPath)
	if err != nil || !strings.Contains(string(b), "PreToolUse") || !strings.Contains(string(b), `"args"`) || !strings.Contains(string(b), "probe_post") {
		t.Fatalf("settings: %s %v", b, err)
	}
	// Bash honours the input rewrite, Read ignores it, Grep and Glob are never called.
	runSession(t, state, []string{"Bash", "Read"}, map[string]bool{"Bash": true})
	caps, err := Finish(state, FinishOptions{Version: "2.1.999", SawOutput: map[string]bool{"Bash": true, "Read": false}, Now: sessionNow})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]hooks.Capability{}
	for _, c := range caps {
		by[c.Tool] = c
		if c.Method != "session" || c.AgentVersion != "2.1.999" {
			t.Fatalf("%+v", c)
		}
	}
	if c := by["Bash"]; c.InputRewrite != hooks.CapYes || c.OutputRewrite != hooks.CapYes || !strings.Contains(c.Evidence, "reported by you") {
		t.Fatalf("Bash: %+v", c)
	}
	if c := by["Read"]; c.InputRewrite != hooks.CapNo || c.OutputRewrite != hooks.CapNo {
		t.Fatalf("Read: %+v", c)
	}
	for _, tool := range []string{"Grep", "Glob"} {
		if c := by[tool]; c.InputRewrite != hooks.CapUntested || !strings.Contains(c.Evidence, "never ran") {
			t.Fatalf("%s: %+v", tool, c)
		}
	}
	c := hooks.Capabilities{Entries: caps}
	if !c.Supports("claude", "Bash", hooks.RewriteInput) || !c.Supports("claude", "Bash", hooks.RewriteOutput) ||
		c.Supports("claude", "Read", hooks.RewriteInput) || c.Supports("claude", "Grep", hooks.RewriteInput) {
		t.Fatalf("Supports disagrees with the session: %+v", c)
	}
}

func TestVerifyOutputStaysUntestedUntilTheUserAnswers(t *testing.T) {
	state := filepath.Join(t.TempDir(), "verify")
	o := armOpts()
	o.Tools = []string{"Bash"}
	if _, err := Arm(state, o); err != nil {
		t.Fatal(err)
	}
	runSession(t, state, []string{"Bash"}, map[string]bool{"Bash": true})
	caps, err := Finish(state, FinishOptions{Now: sessionNow})
	if err != nil {
		t.Fatal(err)
	}
	c := caps[0]
	if c.InputRewrite != hooks.CapYes || c.OutputRewrite != hooks.CapUntested || !strings.Contains(c.Evidence, "you did not say") {
		t.Fatalf("%+v", c)
	}
	if (hooks.Capabilities{Entries: caps}).Supports("claude", "Bash", hooks.RewriteOutput) {
		t.Fatal("an unanswered output check must not allow output rewriting")
	}
}

func TestVerifyHooksGoInertWhenTheSessionExpires(t *testing.T) {
	state := filepath.Join(t.TempDir(), "verify")
	o := armOpts()
	o.Tools = []string{"Bash"}
	o.Now = func() time.Time { return time.Now().Add(-2 * SessionTTL) } // armed two hours ago
	if _, err := Arm(state, o); err != nil {
		t.Fatal(err)
	}
	pre, _ := json.Marshal(map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}})
	var out bytes.Buffer
	HookMain("pre", bytes.NewReader(pre), &out, func(string) string { return state })
	if out.Len() != 0 {
		t.Fatalf("an expired session still rewrote a call: %s", out.String())
	}
	if _, err := os.Stat(filepath.Join(state, "seen.jsonl")); err == nil {
		t.Fatal("an expired session still recorded a call")
	}
}

func TestVerifyRefusesToolNamesThatCouldEscapeTheStateFolder(t *testing.T) {
	o := armOpts()
	o.Tools = []string{"../x"}
	if _, err := Arm(filepath.Join(t.TempDir(), "verify"), o); err == nil {
		t.Fatal("a path-like tool name was accepted")
	}
}

func TestFinishWithoutAnArmedSessionSaysWhatToDo(t *testing.T) {
	if _, err := Finish(filepath.Join(t.TempDir(), "none"), FinishOptions{}); err == nil || !strings.Contains(err.Error(), "trove hooks verify") {
		t.Fatalf("err = %v", err)
	}
}
