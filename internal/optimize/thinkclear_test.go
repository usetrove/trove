package optimize

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// longSession is a Claude Code-shaped request (placeholder text only) with turns typed prompts, each answered with
// a thinking block of thinkBytes, a tool call and its result. toolLast ends it on a tool result instead of a new
// prompt.
func longSession(model string, turns, thinkBytes int, toolLast bool) []byte {
	think, _ := json.Marshal(strings.Repeat("PLACEHOLDER thinking. ", thinkBytes/22))
	var b strings.Builder
	b.WriteString(`{"model":"` + model + `","max_tokens":32000,"thinking":{"type":"adaptive"},"messages":[`)
	for i := 0; i < turns; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"role":"user","content":[{"type":"text","text":"PLACEHOLDER prompt"}]},`)
		b.WriteString(`{"role":"assistant","content":[{"type":"thinking","thinking":` + string(think) + `,"signature":"PLACEHOLDER"},{"type":"tool_use","id":"t` + string(rune('a'+i)) + `","name":"Bash","input":{}}]},`)
		b.WriteString(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t` + string(rune('a'+i)) + `","content":"PLACEHOLDER"}]},`)
		b.WriteString(`{"role":"assistant","content":[{"type":"text","text":"PLACEHOLDER reply"}]}`)
	}
	if toolLast {
		b.WriteString(`,{"role":"assistant","content":[{"type":"tool_use","id":"tz","name":"Bash","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"tz","content":"PLACEHOLDER"}]}`)
	} else {
		b.WriteString(`,{"role":"user","content":[{"type":"text","text":"PLACEHOLDER prompt"}]}`)
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

func clearRun(t *testing.T, body []byte) (*Request, Report) {
	t.Helper()
	r, err := ParseRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	out, rep := ThinkingClear{}.Transform(context.Background(), r)
	if rep.Err != "" {
		t.Fatal(rep.Err)
	}
	return out, rep
}

func TestThinkingClearAddsTheStrategyAndEstimatesBothSides(t *testing.T) {
	// 8 typed turns before the new prompt, 60 KB of thinking each: about 140k tokens in all.
	body := longSession("claude-opus-5-5", 8, 60_000, false)
	out, rep := clearRun(t, body)
	if rep.Name != OptimizerThinkingClear || rep.Clear == nil || len(rep.Betas) != 1 || rep.Betas[0] != ContextManagementBeta {
		t.Fatalf("report %+v", rep)
	}
	if err := out.Ensure(); err != nil {
		t.Fatal(err)
	}
	var v struct {
		CM struct {
			Edits []struct {
				Type string `json:"type"`
				Keep struct {
					Type  string `json:"type"`
					Value int    `json:"value"`
				} `json:"keep"`
			} `json:"edits"`
		} `json:"context_management"`
	}
	if err := json.Unmarshal(out.Raw, &v); err != nil || len(v.CM.Edits) != 1 || v.CM.Edits[0].Type != "clear_thinking_20251015" ||
		v.CM.Edits[0].Keep.Type != "thinking_turns" || v.CM.Edits[0].Keep.Value != ThinkingClearKeep {
		t.Fatalf("context_management: %+v err=%v", v, err)
	}
	if !bytes.HasPrefix(out.Raw, body[:len(body)-1]) {
		t.Error("the field must be added at the end, leaving every earlier byte as it was")
	}
	// 9 turns (8 answered, the new prompt in progress); the last 2 keep their thinking, so 7 are cleared.
	c := rep.Clear
	if c.ClearedTurns != 7 || c.Saved < 7*15_000 || c.Lost <= 0 || c.Label != "estimated" {
		t.Fatalf("estimate %+v", c)
	}
	// On a tool-result continuation the cleared set is the same as on the previous request: nothing is lost.
	_, rep = clearRun(t, longSession("claude-opus-5-5", 8, 60_000, true))
	if rep.Clear == nil || rep.Clear.Lost != 0 || rep.Clear.Saved <= 0 {
		t.Fatalf("continuation estimate %+v", rep.Clear)
	}
	again, _ := clearRun(t, body)
	if !bytes.Equal(out.Raw, again.Raw) {
		t.Fatal("not deterministic")
	}
}

func TestThinkingClearLeavesTheseAlone(t *testing.T) {
	long := string(longSession("claude-opus-5-5", 8, 60_000, false))
	cases := map[string]string{
		"short session":           string(longSession("claude-opus-5-5", 8, 2_000, false)),
		"model that keeps none":   strings.Replace(long, `"claude-opus-5-5"`, `"claude-opus-4-1"`, 1),
		"client's own editing":    strings.Replace(long, `"max_tokens":32000`, `"max_tokens":32000,"context_management":{"edits":[]}`, 1),
		"thinking only in recent": string(longSession("claude-opus-5-5", 1, 400_000, false)),
	}
	for name, body := range cases {
		r, err := ParseRequest([]byte(body))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out, rep := ThinkingClear{}.Transform(context.Background(), r)
		if rep.Name != "" || !bytes.Equal(out.Raw, []byte(body)) {
			t.Errorf("%s: changed the request: %+v", name, rep.Clear)
		}
	}
}
