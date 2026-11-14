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
		{Agent: "codex", Tool: "Bash", InputRewrite: Yes, Method: "docs"},        // docs count for Codex input only
		{Agent: "codex", Tool: "Read", InputRewrite: Yes, Method: "docs"},        // ...and this is input, so it counts too
		{Agent: "codex", Tool: "Grep", OutputRewrite: Yes, Method: "docs"},       // but not for output
	}}
	cases := []struct {
		agent, tool, kind string
		want              bool
	}{
		{"claude", "Bash", RewriteInput, true}, {"claude", "Bash", RewriteOutput, true},
		{"claude", "Read", RewriteInput, true}, {"claude", "Read", RewriteOutput, false},
		{"claude", "Grep", RewriteOutput, false}, {"claude", "Glob", RewriteOutput, false},
		{"claude", "Edit", RewriteOutput, false}, {"claude", "NotAProbedTool", RewriteInput, false},
		{"codex", "Bash", RewriteInput, true}, {"codex", "Grep", RewriteOutput, false},
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
		{Agent: "codex", Tool: "Bash", InputRewrite: Yes, Method: "docs"},
	}}
	if !c.SupportsAny("Bash", RewriteInput) {
		t.Error("codex can rewrite Bash input, so some agent can")
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
