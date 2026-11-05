package tasks

import (
	"strings"
	"testing"
)

func cat(t *testing.T) *Catalog {
	t.Helper()
	c, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestScoutIsReadOnlyAndSmall(t *testing.T) {
	fs, err := ScoutFiles(cat(t), Options{})
	if err != nil {
		t.Fatal(err)
	}
	s := fs[0]
	if s.Path != ".claude/agents/trove-scout.md" || !strings.Contains(s.Content, "tools: Read, Grep, Glob\n") {
		t.Fatalf("unexpected scout file:\n%s", s.Content)
	}
	for _, w := range []string{"Bash", "Edit", "Write"} {
		if strings.Contains(strings.SplitN(s.Content, "---", 3)[1], w) {
			t.Errorf("scout frontmatter mentions %s", w)
		}
	}
	if !strings.Contains(s.Content, "model: haiku\n") {
		t.Error("scout should default to haiku")
	}
	// Rule 13: no provider brand in any name Trove creates.
	for _, f := range append(fs, mustCascade(t)...) {
		for _, brand := range []string{"claude", "anthropic", "codex", "openai"} {
			if strings.Contains(strings.ToLower(f.Path[strings.LastIndex(f.Path, "/")+1:]), brand) && f.Role != RoleSnippet {
				t.Errorf("%s carries the brand %q", f.Path, brand)
			}
		}
	}
}

func mustCascade(t *testing.T) []File {
	t.Helper()
	fs, err := CascadeFiles(cat(t), Options{TestCmd: "go test ./..."})
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func TestRenderingIsDeterministic(t *testing.T) {
	o := Options{TestCmd: "go test ./..."}
	a, _ := CascadeFiles(cat(t), o)
	b, _ := CascadeFiles(cat(t), o)
	for i := range a {
		if a[i].Content != b[i].Content || a[i].Path != b[i].Path {
			t.Fatalf("file %d differs between runs", i)
		}
	}
}

func TestCascadeTextEscalatesOnlyOnFailureAndSaysSo(t *testing.T) {
	fs, err := CascadeFiles(cat(t), Options{TestCmd: "pytest -q", WorkerModel: "haiku", EscalationModel: "opus"})
	if err != nil {
		t.Fatal(err)
	}
	var cmd, esc File
	for _, f := range fs {
		switch f.Role {
		case RoleCascade:
			cmd = f
		case RoleEscalate:
			esc = f
		}
	}
	for _, want := range []string{"`pytest -q`", "Do not trust the subagent's report", "Never escalate without saying so", "which model finished", "$ARGUMENTS", "at most 10 lines"} {
		if !strings.Contains(cmd.Content, want) {
			t.Errorf("command missing %q", want)
		}
	}
	if !strings.Contains(esc.Content, "model: opus\n") || strings.Contains(esc.Content, "maxTurns") {
		t.Errorf("escalation agent wrong:\n%s", esc.Content)
	}
}

func TestValidation(t *testing.T) {
	c := cat(t)
	for name, o := range map[string]Options{
		"no test cmd":      {},
		"newline in cmd":   {TestCmd: "a\nb"},
		"backtick in cmd":  {TestCmd: "a `b`"},
		"unknown model":    {TestCmd: "x", WorkerModel: "gpt-4"},
		"bad escalation":   {TestCmd: "x", EscalationModel: "best"},
		"too many turns":   {TestCmd: "x", MaxTurns: 500},
		"negative turns":   {TestCmd: "x", MaxTurns: -1},
		"over-long cmd":    {TestCmd: strings.Repeat("a", 201)},
		"unbranded prefix": {TestCmd: "x", ScoutModel: "claude-"},
	} {
		if _, err := CascadeFiles(c, o); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if _, err := CascadeFiles(c, Options{TestCmd: "go test ./...", WorkerModel: "claude-sonnet-5-5"}); err != nil {
		t.Errorf("full model ID should be accepted: %v", err)
	}
	if _, err := ScoutFiles(c, Options{}); err != nil {
		t.Errorf("scout needs no test command: %v", err)
	}
}

func TestOverheadIsDeclared(t *testing.T) {
	fs, _ := ScoutFiles(cat(t), Options{})
	o := OverheadOf(fs)
	if o.PerRequestTokens <= 0 || o.PerUseTokens <= 0 {
		t.Fatalf("overhead must be declared and non-zero: %+v", o)
	}
}

func TestCatalogGate(t *testing.T) {
	c := cat(t)
	if f, ok := c.Field("model"); !ok || f.Verified != "verbatim" {
		t.Fatal("model field must be verified verbatim")
	}
	if len(c.ReadOnlyTools) != 3 {
		t.Fatalf("read-only tools = %v", c.ReadOnlyTools)
	}
}
