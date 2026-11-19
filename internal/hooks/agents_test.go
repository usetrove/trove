package hooks

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
)

// The Cursor and Gemini CLI inputs below are written from each agent's hooks
// documentation (cursor.com/docs/agent/hooks; geminicli.com/docs/hooks/reference/,
// both read 2026-11-17), not captured from a live agent. Cursor's per-tool
// tool_input keys are not documented, so file_path and command are assumptions a
// live probe has to confirm (docs/hooks-verification.md).

func cursorInput(event, tool, filePath string) string {
	b, _ := json.Marshal(map[string]any{
		"conversation_id": "conv-1", "generation_id": "gen-1", "hook_event_name": event,
		"tool_name": tool, "tool_input": map[string]any{"file_path": filePath}, "cwd": "/w",
		"model": "some-model", "cursor_version": "9.9", "workspace_roots": []string{"/w"},
		"tool_output": "file text",
	})
	return string(b)
}

func geminiInput(event, tool string, input map[string]any, resp any) string {
	m := map[string]any{"session_id": "gem-1", "transcript_path": "/t.json", "cwd": "/w", "hook_event_name": event,
		"timestamp": "2026-11-17T10:00:00Z", "tool_name": tool, "tool_input": input}
	if resp != nil {
		m["tool_response"] = resp
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func TestParseCursorInput(t *testing.T) {
	in, err := parseFor(AgentCursor, strings.NewReader(cursorInput("preToolUse", "Shell", "/w/a.go")))
	if err != nil {
		t.Fatal(err)
	}
	if in.SessionID != "conv-1" || in.Prompt() != "gen-1" || in.Event != "PreToolUse" || in.ToolName != "Bash" || in.ToolInput.FilePath != "/w/a.go" {
		t.Fatalf("%+v", in)
	}
	in, _ = parseFor(AgentCursor, strings.NewReader(cursorInput("postToolUse", "Read", "/w/a.go")))
	if in.Event != "PostToolUse" || in.ToolName != "Read" || string(in.ToolResponse) != `"file text"` {
		t.Fatalf("%+v", in)
	}
}

func TestParseGeminiInput(t *testing.T) {
	in, err := parseFor(AgentGemini, strings.NewReader(geminiInput("BeforeTool", "read_file",
		map[string]any{"file_path": "/w/a.go", "start_line": 10, "end_line": 19}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if in.SessionID != "gem-1" || in.Event != "PreToolUse" || in.ToolName != "Read" || in.ToolInput.FilePath != "/w/a.go" {
		t.Fatalf("%+v", in)
	}
	var tc map[string]any
	_ = json.Unmarshal(toolCall(in).Input, &tc)
	if tc["offset"] != float64(10) || tc["limit"] != float64(10) {
		t.Fatalf("a line range must reach the engine as offset/limit: %v", tc)
	}
	in, _ = parseFor(AgentGemini, strings.NewReader(geminiInput("AfterTool", "run_shell_command",
		map[string]any{"command": "go test ./..."}, map[string]any{"llmContent": "ok\n", "returnDisplay": "ok"})))
	if in.Event != "PostToolUse" || in.ToolName != "Bash" || in.ToolInput.Command != "go test ./..." || string(in.ToolResponse) != `"ok\n"` {
		t.Fatalf("%+v %s", in, in.ToolResponse)
	}
	for native, want := range map[string]string{"grep_search": "Grep", "search_file_content": "Grep", "glob": "Glob", "replace": "Edit", "write_file": "Write", "web_fetch": "web_fetch"} {
		in, _ = parseFor(AgentGemini, strings.NewReader(geminiInput("AfterTool", native, map[string]any{}, nil)))
		if in.ToolName != want {
			t.Errorf("%s -> %q, want %q", native, in.ToolName, want)
		}
	}
	in, _ = parseFor(AgentGemini, strings.NewReader(geminiInput("PreCompress", "", nil, nil)))
	if in.Event != "PreCompact" {
		t.Errorf("PreCompress -> %q", in.Event)
	}
}

func TestReplyTranslation(t *testing.T) {
	deny := denyJSON("unchanged since prompt #1")
	ctx := contextJSON("try trove_run")
	upIn := updatedInputJSON(json.RawMessage(`{"command":"pytest -q"}`), "quieted")
	upOut := updatedOutputJSON(json.RawMessage(`"short"`))
	cases := []struct {
		agent string
		in    []byte
		want  string
	}{
		{AgentClaude, deny, string(deny)},
		{AgentCodex, deny, string(deny)},
		{AgentCursor, deny, `{"agent_message":"unchanged since prompt #1","permission":"deny"}`},
		{AgentCursor, ctx, ``}, // preToolUse documents no context field
		{AgentCursor, upIn, `{"updated_input":{"command":"pytest -q"}}`},
		{AgentCursor, upOut, ``}, // output is replaceable for MCP tools only
		{AgentGemini, deny, `{"decision":"deny","reason":"unchanged since prompt #1"}`},
		{AgentGemini, ctx, ``},
		{AgentGemini, upIn, `{"hookSpecificOutput":{"tool_input":{"command":"pytest -q"}}}`},
		{AgentGemini, upOut, `{"decision":"deny","reason":"short"}`},
		{AgentGemini, updatedOutputJSON(json.RawMessage(`{"stdout":"x"}`)), ``}, // not the shape Gemini's input was turned into
	}
	for _, c := range cases {
		if got := string(reply(c.agent, c.in)); got != c.want {
			t.Errorf("%s %s:\n got %s\nwant %s", c.agent, c.in, got, c.want)
		}
	}
	// No translated reply ever approves a call: the user's own permission flow still applies.
	for _, agent := range []string{AgentCursor, AgentGemini} {
		for _, in := range [][]byte{deny, ctx, upIn, upOut} {
			if s := string(reply(agent, in)); strings.Contains(s, `"allow"`) || strings.Contains(s, `"continue"`) {
				t.Errorf("%s reply approves or stops: %s", agent, s)
			}
		}
	}
}

func TestRewritesStayShadowUntilExpressibleAndProbed(t *testing.T) {
	cases := []struct {
		agent, tool, name string
		probed            bool
		want              string
	}{
		{AgentGemini, "Bash", config.OptimizerHookQuietCommands, false, stateShadow}, // docs only
		{AgentGemini, "Bash", config.OptimizerHookQuietCommands, true, stateOn},
		{AgentGemini, "Read", config.OptimizerHookReadLimit, true, stateShadow}, // range keys differ; not expressible
		{AgentGemini, "Bash", config.OptimizerNoiseNormalizer, true, stateOn},
		{AgentGemini, "Read", config.OptimizerHookReadOutline, true, stateShadow},
		{AgentCursor, "Bash", config.OptimizerHookQuietCommands, true, stateOn},
		{AgentCursor, "Bash", config.OptimizerNoiseNormalizer, true, stateShadow}, // no output replacement
		{AgentCursor, "Read", config.OptimizerHookRepeatRead, false, stateOn},     // a deny, not a rewrite
	}
	for _, c := range cases {
		r := newRig(t, map[string]string{c.name: "on"})
		if c.probed {
			if err := SaveCapabilities(r.dir, Capabilities{Entries: []Capability{{Agent: c.agent, Tool: c.tool, InputRewrite: CapYes, OutputRewrite: CapYes, Method: "live"}}}); err != nil {
				t.Fatal(err)
			}
		}
		snap, _ := LoadSnapshot(r.dir)
		h := &handler{dir: r.dir, agent: c.agent, getenv: r.env, now: func() time.Time { return r.now }, snap: snap}
		if got := h.stateFor(c.name, "on", c.tool); got != c.want {
			t.Errorf("%s %s %s probed=%v: %s, want %s", c.agent, c.tool, c.name, c.probed, got, c.want)
		}
	}
}

func TestGeminiRepeatReadIsDeniedInItsOwnShape(t *testing.T) {
	noCallClock(t) // the test is about the decision, not the machine's speed
	r := newRig(t, on)
	p := r.file("a.go", "package a\n")
	start := geminiInput("SessionStart", "", nil, nil)
	start = strings.Replace(start, `"tool_name":""`, `"source":"startup"`, 1)
	if out, _ := r.run(EventReset, start, "--agent", AgentGemini); out != "" {
		t.Fatalf("start: %q", out)
	}
	read := func(prompt string) string {
		in := map[string]any{"file_path": p}
		out, _ := r.run(EventPreRead, geminiInput("BeforeTool", "read_file", in, nil), "--agent", AgentGemini)
		if out == "" {
			r.run(EventPostRead, geminiInput("AfterTool", "read_file", in, map[string]any{"llmContent": "package a\n"}), "--agent", AgentGemini)
		}
		return out
	}
	if out := read("p1"); out != "" {
		t.Fatalf("first read must go through: %q", out)
	}
	var v struct{ Decision, Reason string }
	out := read("p2")
	if err := json.Unmarshal([]byte(out), &v); err != nil || v.Decision != "deny" || !strings.Contains(v.Reason, "unchanged") {
		t.Fatalf("out = %q", out)
	}
	for _, e := range r.events() {
		if strings.HasPrefix(e.Key, "c-") || e.Key == r.key() {
			t.Fatalf("Gemini CLI events must not share Claude Code's session key: %+v", e)
		}
	}
}

func TestUnknownAgentFallsBackToClaudeShapes(t *testing.T) {
	if got := string(reply("someone-else", denyJSON("x"))); got != string(denyJSON("x")) {
		t.Fatal(got)
	}
}
