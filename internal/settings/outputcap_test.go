package settings

import (
	"strings"
	"testing"
)

func TestBuiltinOutputCaps(t *testing.T) {
	c, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		unit  string
		limit int64
	}{
		"Bash": {CapChars, 30000},
		"mcp":  {CapChars, 50000},
		"Read": {CapTokens, 25000},
	}
	f, _ := c.File(ClaudeCode)
	if len(f.OutputCaps) != len(want) {
		t.Fatalf("want %d output caps, got %d", len(want), len(f.OutputCaps))
	}
	for tool, w := range want {
		oc, ok := c.OutputCap(ClaudeCode, tool)
		if !ok {
			t.Errorf("no output cap for %s", tool)
			continue
		}
		if oc.Unit != w.unit || oc.Limit != w.limit {
			t.Errorf("%s: got %d %s, want %d %s", tool, oc.Limit, oc.Unit, w.limit, w.unit)
		}
		// Each cap names the agent version it describes and when it was read.
		if _, err := ParseVersion(oc.DocsVersion); err != nil {
			t.Errorf("%s: docs_version: %v", tool, err)
		}
		if !dateRE.MatchString(oc.VerifiedOn) || !strings.HasPrefix(oc.DocURL, "https://") {
			t.Errorf("%s: needs verified_on and an https doc_url: %+v", tool, oc)
		}
		// The setting that raises or lowers the cap must exist in the catalog. MCP's 50,000-character limit
		// has none: only the MCP server itself can raise it (anthropic/maxResultSizeChars).
		if tool == "mcp" && oc.SettingKey == "" {
			continue
		}
		if _, ok := c.Lookup(ClaudeCode, oc.SettingKey); !ok {
			t.Errorf("%s: setting_key %q is not in the catalog", tool, oc.SettingKey)
		}
	}
	if _, ok := c.OutputCap(Codex, "Bash"); ok {
		t.Error("Codex has no verified output caps; none may be reported")
	}
}

func TestOutputCapRejects(t *testing.T) {
	base := `version = 1
harness = "claude_code"
checked_on = "2026-11-05"
[[output_cap]]
tool = "Bash"
unit = "chars"
limit = 30000
setting_key = "bashOutputMaxChars"
docs_version = "2.1.314"
doc_url = "https://example.com/x"
verified_on = "2026-11-13"
`
	if _, err := Parse([]byte(base)); err != nil {
		t.Fatalf("base cap must parse: %v", err)
	}
	for name, mut := range map[string][2]string{
		"unit":     {`unit = "chars"`, `unit = "lines"`},
		"limit":    {`limit = 30000`, `limit = 0`},
		"version":  {`docs_version = "2.1.314"`, `docs_version = "latest"`},
		"date":     {`verified_on = "2026-11-13"`, `verified_on = "yesterday"`},
		"https":    {`doc_url = "https://example.com/x"`, `doc_url = "http://example.com/x"`},
		"tool":     {`tool = "Bash"`, `tool = ""`},
		"no-unit":  {`unit = "chars"`, ``},
		"dup-tool": {`tool = "Bash"`, "tool = \"Bash\"\n[[output_cap]]\ntool = \"Bash\"\nunit = \"chars\"\nlimit = 1\ndocs_version = \"2.1.314\"\ndoc_url = \"https://example.com/x\"\nverified_on = \"2026-11-13\""},
	} {
		if _, err := Parse([]byte(strings.Replace(base, mut[0], mut[1], 1))); err == nil {
			t.Errorf("%s: a bad output cap must be rejected", name)
		}
	}
}
