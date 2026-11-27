package probe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/hookcaps"
	"github.com/usetrove/trove/internal/hooks"
)

// The transcripts below are written in the shape Claude Code's transcripts are observed to have (docs/logs.md), with
// made-up content: a user line per prompt, an assistant line with usage per reply, and the hook's text on a line of
// its own between them. Turn sizes: 20,000 tokens of system prompt and tools, plus a little per turn.

func userLine(text string) string {
	b, _ := json.Marshal(map[string]any{"type": "user", "isSidechain": false, "message": map[string]any{"role": "user", "content": text}})
	return string(b)
}

func replyLine(input, read, write int64) string {
	b, _ := json.Marshal(map[string]any{"type": "assistant", "isSidechain": false, "message": map[string]any{
		"model": "claude-opus-5-5", "content": []any{map[string]any{"type": "text", "text": "OK"}},
		"usage": map[string]any{"input_tokens": input, "cache_read_input_tokens": read, "cache_creation_input_tokens": write}}})
	return string(b)
}

func markerLine(m string) string {
	b, _ := json.Marshal(map[string]any{"type": "attachment", "attachment": map[string]any{"type": "hook_additional_context", "content": []string{m}}})
	return string(b)
}

const testMarker = "TROVE_PROBE_CTX_abc123"

// goodTranscript: each turn reads the whole previous prompt from the cache. Turn 1's prompt is 20,013 tokens; turn 2
// (with the test text, about 280 tokens with its prompt) reads 20,013 and writes 280; turn 3 reads 20,296. The fourth
// prompt's line is there, as it may be when its hook runs; its reply is not yet.
func goodTranscript() []string {
	return []string{
		userLine(ContextPrompts[0]), replyLine(3, 0, 20010),
		markerLine(testMarker), userLine(ContextPrompts[1]), replyLine(3, 20013, 280),
		userLine(ContextPrompts[2]), replyLine(3, 20296, 20),
		userLine(ContextPrompts[3]),
	}
}

func analyse(lines []string) ContextFindings {
	var bs [][]byte
	for _, l := range lines {
		bs = append(bs, []byte(l))
	}
	f := ContextFindings{Calls: 4}
	analyseContextLines(&f, bs, testMarker)
	f.Analysed = true
	return f
}

func TestContextVerdicts(t *testing.T) {
	good := goodTranscript()
	replace := func(i int, l string) []string {
		out := append([]string{}, good...)
		out[i] = l
		return out
	}
	move := func(from, to int) []string {
		out := append([]string{}, good...)
		l := out[from]
		out = append(out[:from], out[from+1:]...)
		return append(out[:to], append([]string{l}, out[to:]...)...)
	}
	cases := []struct {
		name  string
		lines []string
		want  string
	}{
		{"lands after the prefix and stays", good, hooks.CapYes},
		{"marker after the user line, still before the request", move(2, 3), hooks.CapYes},
		// Text placed early (in the system prompt, say) means turn 2 reads only the tools from the cache.
		{"breaks the prefix", replace(4, replyLine(3, 12000, 8293)), hooks.CapNo},
		{"a few tokens short is rounding", replace(4, replyLine(10, 20003, 283)), hooks.CapYes},
		// Text dropped on later turns means turn 3 reads only up to before turn 2.
		{"does not stay", replace(6, replyLine(3, 20013, 303)), hooks.CapNo},
		{"turn 4 not in the transcript yet", good[:7], hooks.CapYes},
		{"marker after the new turn's request", move(2, 4), hooks.CapNo},
		{"marker before the previous turn's request", move(2, 1), hooks.CapNo},
		{"no marker", append(good[:2:2], good[3:]...), hooks.CapUntested},
		{"a prompt missing", append(good[:5:5], good[6:]...), hooks.CapUntested},
		{"no usage for turn 3", replace(6, userLine("tool result")), hooks.CapUntested},
		{"turn 4's reply is not turn 3's", append(replace(6, userLine("tool result")), replyLine(3, 20296, 20)), hooks.CapUntested},
	}
	for _, c := range cases {
		f := analyse(c.lines)
		got, ev := decideContext(f)
		if got != c.want {
			t.Errorf("%s: %s (%s), want %s; findings %+v", c.name, got, ev, c.want, f)
		}
		if ev == "" {
			t.Errorf("%s: no evidence", c.name)
		}
	}
}

func TestContextVerdictNeedsAllFourPrompts(t *testing.T) {
	for _, f := range []ContextFindings{{Calls: 0}, {Calls: 3}, {Calls: 4}} {
		if got, _ := decideContext(f); got != hooks.CapUntested {
			t.Errorf("%+v: %s", f, got)
		}
	}
}

// TestContextCheckEndToEnd drives the real test hook: SessionStart, four prompts, the transcript read on the fourth.
func TestContextCheckEndToEnd(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "verify-context")
	armed, err := ArmContext(state, ArmContextOptions{Version: "2.1.400",
		HookCommand: func(st string) (string, []string) { return "trove", []string{"hook", "probe_context", "--state", st} }})
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]map[string]any
	b, _ := os.ReadFile(armed.SettingsPath)
	if json.Unmarshal(b, &settings) != nil || settings["hooks"]["SessionStart"] == nil || settings["hooks"]["UserPromptSubmit"] == nil {
		t.Fatalf("settings: %s", b)
	}
	var s contextSession
	sb, _ := os.ReadFile(filepath.Join(state, "context-session.json"))
	_ = json.Unmarshal(sb, &s)
	marker := contextMarker(s.Nonce)

	transcript := filepath.Join(root, "SECRET-PATH", "t.jsonl")
	if err := os.MkdirAll(filepath.Dir(transcript), 0o700); err != nil {
		t.Fatal(err)
	}
	getenv := func(k string) string {
		if k == StateEnv {
			return state
		}
		return ""
	}
	call := func(in map[string]any) string {
		in["transcript_path"] = transcript
		in["prompt"] = "SECRET-PROMPT-TEXT"
		j, _ := json.Marshal(in)
		var out bytes.Buffer
		if code := ContextHookMain(bytes.NewReader(j), &out, getenv); code != 0 {
			t.Fatalf("exit %d", code)
		}
		return out.String()
	}
	call(map[string]any{"hook_event_name": "SessionStart", "model": "claude-opus-5-5"})
	for i := 1; i <= 4; i++ {
		if i == 4 {
			lines := goodTranscript()
			lines[2] = markerLine(marker)
			if err := os.WriteFile(transcript, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		out := call(map[string]any{"hook_event_name": "UserPromptSubmit"})
		if (i == 2) != strings.Contains(out, marker) {
			t.Fatalf("prompt %d: reply %q", i, out)
		}
		if i == 2 && !strings.Contains(out, `"hookEventName":"UserPromptSubmit"`) {
			t.Fatalf("reply does not name its event: %s", out)
		}
	}
	fb, _ := os.ReadFile(filepath.Join(state, "context-findings.json"))
	for _, secret := range []string{"SECRET-PROMPT", "SECRET-PATH", "OK"} {
		if bytes.Contains(fb, []byte(secret)) {
			t.Errorf("the findings hold %q: %s", secret, fb)
		}
	}

	prior := hooks.Capability{Agent: "claude", Tool: hookcaps.ToolPrompt, Block: hookcaps.Yes, Method: "session", Evidence: "block seen"}
	res, err := FinishContext(state, FinishContextOptions{Prior: &prior, Now: func() time.Time { return time.Unix(0, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	c := res.Capability
	if c.Context != hookcaps.Yes || c.Block != hookcaps.Yes || c.Evidence != "block seen" || c.Method != "session" {
		t.Fatalf("capability %+v", c)
	}
	if !res.Findings.ModelAllowed || !res.Findings.ModelSeen {
		t.Errorf("model findings %+v", res.Findings)
	}
	caps := hookcaps.Capabilities{V: hookcaps.Version, Entries: []hookcaps.Capability{c}}
	if !caps.Supports("claude", hookcaps.ToolPrompt, hookcaps.PromptContext) {
		t.Error("a passed check does not enable the steer")
	}
}

func TestContextHookIgnoresAnExpiredCheck(t *testing.T) {
	state := filepath.Join(t.TempDir(), "s")
	if _, err := ArmContext(state, ArmContextOptions{Now: func() time.Time { return time.Now().Add(-2 * SessionTTL) },
		HookCommand: func(string) (string, []string) { return "x", nil }}); err != nil {
		t.Fatal(err)
	}
	getenv := func(k string) string { return map[string]string{StateEnv: state}[k] }
	for i := 0; i < 3; i++ {
		var out bytes.Buffer
		ContextHookMain(strings.NewReader(`{"hook_event_name":"UserPromptSubmit"}`), &out, getenv)
		if out.Len() != 0 {
			t.Fatalf("an expired check still adds text: %s", out.String())
		}
	}
	if f := readContextFindings(state); f.Calls != 0 {
		t.Errorf("an expired check still counts: %+v", f)
	}
}

func TestFinishContextWithoutArming(t *testing.T) {
	if _, err := FinishContext(filepath.Join(t.TempDir(), "none"), FinishContextOptions{}); err == nil || !strings.Contains(err.Error(), "--prompt-context") {
		t.Errorf("err = %v", err)
	}
}

func ExampleContextPrompts() {
	for _, p := range ContextPrompts {
		fmt.Println(len(p) < 80)
	}
	// Output:
	// true
	// true
	// true
	// true
}
