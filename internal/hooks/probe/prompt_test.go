package probe

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/hookcaps"
	"github.com/usetrove/trove/internal/hooks"
)

func armPrompt(t *testing.T) (string, ArmedPrompt) {
	t.Helper()
	state := filepath.Join(t.TempDir(), "verify-prompt")
	a, err := ArmPrompt(state, ArmPromptOptions{Version: "2.1.400",
		HookCommand: func(st string) (string, []string) { return "trove", []string{"hook", "probe_prompt", "--state", st} }})
	if err != nil {
		t.Fatal(err)
	}
	return state, a
}

// call runs the test hook as Claude Code would, with the given entrypoint word.
func call(t *testing.T, state, entry string) (decision, reason, system string) {
	t.Helper()
	var out bytes.Buffer
	in := `{"session_id":"s","hook_event_name":"UserPromptSubmit","prompt":"MY-PRIVATE-PROMPT"}`
	PromptHookMain(strings.NewReader(in), &out, func(k string) string {
		switch k {
		case StateEnv:
			return state
		case EntrypointEnv:
			return entry
		}
		return ""
	})
	var v struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
		System   string `json:"systemMessage"`
	}
	if out.Len() > 0 {
		if err := json.Unmarshal(out.Bytes(), &v); err != nil {
			t.Fatalf("not JSON: %q", out.String())
		}
	}
	return v.Decision, v.Reason, v.System
}

func TestPromptCheckArmsOneUserPromptSubmitHook(t *testing.T) {
	state, a := armPrompt(t)
	b, err := os.ReadFile(a.SettingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string   `json:"command"`
				Args    []string `json:"args"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(b, &s); err != nil || len(s.Hooks) != 1 || len(s.Hooks["UserPromptSubmit"]) != 1 {
		t.Fatalf("settings %s (%v)", b, err)
	}
	h := s.Hooks["UserPromptSubmit"][0].Hooks[0]
	if h.Command != "trove" || strings.Join(h.Args, " ") != "hook probe_prompt --state "+state {
		t.Errorf("hook %+v", h)
	}
	if !strings.HasPrefix(a.BlockMarker, "TROVE_PROBE_BLOCK_") || !strings.HasPrefix(a.SystemMarker, "TROVE_PROBE_SYS_") {
		t.Errorf("markers %q %q", a.BlockMarker, a.SystemMarker)
	}
}

func TestPromptHookStopsTheFirstPromptOnlyAndStoresNoPrompt(t *testing.T) {
	state, a := armPrompt(t)
	d, r, s := call(t, state, "cli")
	if d != "block" || !strings.HasPrefix(r, a.BlockMarker) || s != "" {
		t.Fatalf("first call: %q %q %q", d, r, s)
	}
	d, _, s = call(t, state, "cli")
	if d != "" || s != a.SystemMarker {
		t.Fatalf("second call: %q %q", d, s)
	}
	d, _, s = call(t, state, "sdk-cli")
	if d != "" || s != a.SystemMarker {
		t.Fatalf("print-mode call: %q %q", d, s)
	}
	_ = filepath.Walk(state, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			if b, _ := os.ReadFile(p); bytes.Contains(b, []byte("MY-PRIVATE-PROMPT")) {
				t.Errorf("%s holds the prompt", filepath.Base(p))
			}
		}
		return nil
	})
	if got := readPromptCalls(state); len(got) != 3 || got[0].Entry != "cli" || got[2].Entry != "sdk-cli" {
		t.Errorf("calls %+v", got)
	}
}

func TestPromptHookKeepsOddEntrypointValuesOut(t *testing.T) {
	state, _ := armPrompt(t)
	call(t, state, "cli; rm -rf /")
	if got := readPromptCalls(state); len(got) != 1 || got[0].Entry != "other" {
		t.Errorf("calls %+v", got)
	}
}

func TestPromptHookDoesNothingWithoutAPreparedCheck(t *testing.T) {
	if d, r, s := call(t, t.TempDir(), "cli"); d != "" || r != "" || s != "" {
		t.Errorf("unprepared state: %q %q %q", d, r, s)
	}
	if d, _, _ := call(t, "", "cli"); d != "" {
		t.Error("no state: must do nothing")
	}
}

func TestFinishPromptVerdicts(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name       string
		entries    []string
		sawBlock   *bool
		sawSystem  *bool
		want       string
		wantSystem string
		evidence   string
	}{
		{"distinguishable and seen", []string{"cli", "cli", "sdk-cli"}, &yes, &no, hookcaps.Yes, hookcaps.No, "CLAUDE_CODE_ENTRYPOINT=sdk-cli under -p"},
		{"print mode looks interactive", []string{"cli", "cli", "cli"}, &yes, nil, hookcaps.No, hookcaps.Untested, "cannot tell them apart"},
		{"print mode says nothing", []string{"cli", "cli", ""}, &yes, nil, hookcaps.No, hookcaps.Untested, "cannot tell them apart"},
		{"interactive is not cli", []string{"", "", "sdk-cli"}, &yes, nil, hookcaps.No, hookcaps.Untested, "not \"cli\""},
		{"block not seen", []string{"cli", "cli", "sdk-cli"}, &no, nil, hookcaps.No, hookcaps.Untested, "not stopped"},
		{"no answer", []string{"cli", "cli", "sdk-cli"}, nil, &yes, hookcaps.Untested, hookcaps.Yes, "did not say"},
		{"print run missing", []string{"cli", "cli"}, &yes, nil, hookcaps.Untested, hookcaps.Untested, "ran 2 times"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			state, _ := armPrompt(t)
			for _, e := range c.entries {
				call(t, state, e)
			}
			res, err := FinishPrompt(state, FinishPromptOptions{SawBlock: c.sawBlock, SawSystem: c.sawSystem})
			if err != nil {
				t.Fatal(err)
			}
			got := res.Capability
			if got.Block != c.want || res.SystemToModel != c.wantSystem || !strings.Contains(got.Evidence, c.evidence) {
				t.Errorf("block %q system %q evidence %q", got.Block, res.SystemToModel, got.Evidence)
			}
			if got.Tool != hookcaps.ToolPrompt || got.Method != "session" || got.AgentVersion != "2.1.400" {
				t.Errorf("entry %+v", got)
			}
			caps := hooks.Capabilities{V: hookcaps.Version, Entries: []hooks.Capability{got}}
			if caps.Supports("claude", hookcaps.ToolPrompt, hookcaps.PromptBlock) != (c.want == hookcaps.Yes) {
				t.Error("Supports disagrees with the verdict")
			}
		})
	}
	if _, err := FinishPrompt(filepath.Join(t.TempDir(), "none"), FinishPromptOptions{}); err == nil || !strings.Contains(err.Error(), "--prompt-block") {
		t.Errorf("no prepared check: %v", err)
	}
}
