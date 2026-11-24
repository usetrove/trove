package hookcaps

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(Path(dir)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(dir), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadTreatsAnythingUnreadableAsNothingSupported(t *testing.T) {
	dir := t.TempDir()
	if c := Load(dir); len(c.Entries) != 0 || c.Supports("claude", "Bash", RewriteOutput) {
		t.Fatalf("missing file: %+v", c)
	}
	_ = os.MkdirAll(filepath.Dir(Path(dir)), 0o755)
	_ = os.WriteFile(Path(dir), []byte("{not json"), 0o600)
	if c := Load(dir); len(c.Entries) != 0 {
		t.Errorf("corrupt file: %+v", c)
	}
	write(t, dir, Capabilities{V: Version + 1, Entries: []Capability{{Agent: "claude", Tool: "Bash", OutputRewrite: Yes, Method: "live"}}})
	if c := Load(dir); c.Supports("claude", "Bash", RewriteOutput) {
		t.Error("a record from a newer version must not be trusted")
	}
	write(t, dir, Capabilities{V: Version, Entries: []Capability{{Agent: "claude", Tool: "Bash", OutputRewrite: Yes, Method: "live"}}})
	if c := Load(dir); !c.Supports("claude", "Bash", RewriteOutput) {
		t.Error("a valid record was not read")
	}
}

func TestSupportsNeedsAProbeThatSawItWork(t *testing.T) {
	c := Capabilities{V: Version, Entries: []Capability{
		{Agent: "claude", Tool: "Bash", InputRewrite: Yes, OutputRewrite: Yes, Method: "live"},
		{Agent: "claude", Tool: "Read", InputRewrite: Yes, Method: "session"},
		{Agent: "claude", Tool: "Grep", OutputRewrite: Yes, Method: "docs"},      // docs alone never counts for Claude Code
		{Agent: "claude", Tool: "Glob", OutputRewrite: No, Method: "live"},       // seen NOT to work
		{Agent: "claude", Tool: "Edit", OutputRewrite: Untested, Method: "live"}, // not tested
		{Agent: "codex", Tool: "Bash", InputRewrite: Yes, Method: "docs"},        // docs alone no longer count for Codex either
		{Agent: "codex", Tool: "Grep", OutputRewrite: Yes, Method: "docs"},       // nor for output
		{Agent: "codex", Tool: "Read", InputRewrite: Yes, Method: "session"},     // a verified session does
		{Agent: "cursor", Tool: "Bash", InputRewrite: Yes, Method: "docs"},       // docs never count for Cursor
		{Agent: "cursor", Tool: "Read", InputRewrite: Yes, Method: "live"},       // a live probe does
		{Agent: "gemini", Tool: "Bash", OutputRewrite: Yes, Method: "docs"},      // docs never count for Gemini CLI
		{Agent: "gemini", Tool: "Grep", OutputRewrite: Yes, Method: "session"},   // a verified session does
	}}
	cases := []struct {
		agent, tool, kind string
		want              bool
	}{
		{"claude", "Bash", RewriteInput, true}, {"claude", "Bash", RewriteOutput, true},
		{"claude", "Read", RewriteInput, true}, {"claude", "Read", RewriteOutput, false},
		{"claude", "Grep", RewriteOutput, false}, {"claude", "Glob", RewriteOutput, false},
		{"claude", "Edit", RewriteOutput, false}, {"claude", "NotAProbedTool", RewriteInput, false},
		{"codex", "Bash", RewriteInput, false}, {"codex", "Grep", RewriteOutput, false}, {"codex", "Read", RewriteInput, true},
		{"cursor", "Bash", RewriteInput, false}, {"cursor", "Read", RewriteInput, true},
		{"gemini", "Bash", RewriteOutput, false}, {"gemini", "Grep", RewriteOutput, true},
		{"someone-else", "Bash", RewriteInput, false}, {"claude", "Bash", "sideways", false},
	}
	for _, x := range cases {
		if got := c.Supports(x.agent, x.tool, x.kind); got != x.want {
			t.Errorf("Supports(%s, %s, %s) = %v, want %v", x.agent, x.tool, x.kind, got, x.want)
		}
	}
}

func TestSupportsAnyAsksWhetherAnyProbedAgentCanDoIt(t *testing.T) {
	c := Capabilities{V: Version, Entries: []Capability{
		{Agent: "claude", Tool: "Bash", OutputRewrite: No, Method: "live"},
		{Agent: "codex", Tool: "Bash", InputRewrite: Yes, Method: "live"},
	}}
	if !c.SupportsAny("Bash", RewriteInput) {
		t.Error("codex was probed rewriting Bash input, so some agent can")
	}
	if c.SupportsAny("Bash", RewriteOutput) {
		t.Error("no agent can rewrite Bash output")
	}
	if (Capabilities{}).SupportsAny("Bash", RewriteInput) {
		t.Error("an empty record supports nothing")
	}
}

func TestFind(t *testing.T) {
	c := Capabilities{Entries: []Capability{{Agent: "claude", Tool: "Bash", AgentVersion: "2.1.319"}}}
	if e, ok := c.Find("claude", "Bash"); !ok || e.AgentVersion != "2.1.319" {
		t.Errorf("%+v %v", e, ok)
	}
	if _, ok := c.Find("claude", "Read"); ok {
		t.Error("found an entry that is not there")
	}
}

func TestPromptBlockNeedsALiveOrSessionCheck(t *testing.T) {
	cases := []struct {
		name string
		e    Capability
		want bool
	}{
		{"documented only", Capability{Agent: "claude", Tool: ToolPrompt, Block: Yes, Method: "docs"}, false},
		{"seen in a live check", Capability{Agent: "claude", Tool: ToolPrompt, Block: Yes, Method: "live"}, true},
		{"seen in the user's session", Capability{Agent: "claude", Tool: ToolPrompt, Block: Yes, Method: "session"}, true},
		{"checked and not usable", Capability{Agent: "claude", Tool: ToolPrompt, Block: No, Method: "live"}, false},
		{"not checked", Capability{Agent: "claude", Tool: ToolPrompt, Block: Untested, Method: "live"}, false},
		{"a rewrite entry says nothing about blocking", Capability{Agent: "claude", Tool: "Bash", InputRewrite: Yes, OutputRewrite: Yes, Method: "live"}, false},
	}
	for _, c := range cases {
		caps := Capabilities{V: Version, Entries: []Capability{c.e}}
		if got := caps.Supports("claude", ToolPrompt, PromptBlock); got != c.want {
			t.Errorf("%s: Supports = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestBlockFieldRoundTrips(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, Capabilities{V: Version, Entries: []Capability{{Agent: "claude", Tool: ToolPrompt, Block: Yes, Method: "session"}}})
	if c := Load(dir); !c.Supports("claude", ToolPrompt, PromptBlock) {
		t.Errorf("the block result was lost on the way through the file: %+v", c)
	}
	b, err := json.Marshal(Capability{Agent: "claude", Tool: "Bash"})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"agent":"claude","agent_version":"","tool":"Bash","input_rewrite":"","output_rewrite":"","method":"","evidence":"","probed_at":""}` {
		t.Errorf("an entry without a block result must not grow a field: %s", b)
	}
}
