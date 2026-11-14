package settings

import (
	"strings"
	"testing"
)

func TestBuiltinCatalogLoads(t *testing.T) {
	c, err := Builtin()
	if err != nil {
		t.Fatalf("embedded catalog must be valid: %v", err)
	}
	if len(c.Files) != 2 {
		t.Fatalf("want 2 harness catalogs, got %d", len(c.Files))
	}
	for _, f := range c.Files {
		for _, s := range f.Settings {
			// Rule 8: nothing is written unless read verbatim.
			if s.Applyable && s.Verified != VerifiedVerbatim {
				t.Errorf("%s is applyable without a verbatim check", s.Key)
			}
		}
	}
	if s, ok := c.Lookup(ClaudeCode, "claudeMdExcludes"); !ok || !s.Applyable {
		t.Error("claudeMdExcludes should be in the Claude Code catalog and applyable")
	}
	// Read as raw text on 2026-11-13, so these may be written now.
	for _, k := range []string{"effortLevel", "ENABLE_TOOL_SEARCH", "MAX_MCP_OUTPUT_TOKENS", "bashOutputMaxChars", "autoCompactWindow", "promptCacheTtl", "subagentPromptCacheTtl", "idleCompaction"} {
		if s, ok := c.Lookup(ClaudeCode, k); !ok || !s.Applyable || s.Verified != VerifiedVerbatim || s.VerifiedOn != "2026-11-13" {
			t.Errorf("%s should be applyable, verbatim and verified on 2026-11-13: %+v", k, s)
		}
	}
	// Still only seen in a summary: these stay suggestion-only until read as raw text.
	for _, k := range []string{"MAX_THINKING_TOKENS", "alwaysLoad", "model", "fallbackModel"} {
		if s, ok := c.Lookup(ClaudeCode, k); !ok || s.Applyable {
			t.Errorf("%s must stay suggestion-only until its entry is read as raw text", k)
		}
	}
	if s, _ := c.Lookup(ClaudeCode, "bashOutputMaxChars"); s.Since != "2.1.261" || *s.Min != 4000 || *s.Max != 128000 {
		t.Errorf("bashOutputMaxChars range or version drifted from the docs: %+v", s)
	}
	if s, _ := c.Lookup(ClaudeCode, "BASH_MAX_OUTPUT_LENGTH"); s.Applyable {
		t.Error("BASH_MAX_OUTPUT_LENGTH is ignored when bashOutputMaxChars is set; Trove writes the key instead")
	}
}

func TestParseRejects(t *testing.T) {
	base := `version = 1
harness = "claude_code"
checked_on = "2026-11-05"
[[setting]]
key = "k"
harness = "claude_code"
target = "settings_env"
scopes = ["user"]
type = "int"
doc_url = "https://example.com/x"
verified_on = "2026-11-05"
verified = "verbatim"
risk = "none"
`
	if _, err := Parse([]byte(base)); err != nil {
		t.Fatalf("base must parse: %v", err)
	}
	tests := []struct {
		name, from, to, want string
	}{
		{"future version", "version = 1", "version = 2", "Upgrade Trove"},
		{"unknown field", "risk = \"none\"", "risk = \"none\"\nsurprise = 1", "could not read"},
		{"http doc", "https://example", "http://example", "doc_url must be https"},
		{"bad date", "verified_on = \"2026-11-05\"", "verified_on = \"yesterday\"", "YYYY-MM-DD"},
		{"extractor applyable", "risk = \"none\"", "risk = \"none\"\napplyable = true\nverified_values = [\"1\"]", ""},
		{"wrong target for harness", "settings_env", "codex_profile", "codex_profile target"},
		{"shell env applyable", "settings_env", "shell_env", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := strings.Replace(base, tc.from, tc.to, 1)
			if tc.name == "extractor applyable" {
				src = strings.Replace(src, `verified = "verbatim"`, `verified = "extractor"`, 1)
			}
			if tc.name == "shell env applyable" {
				src += "applyable = true\n"
			}
			_, err := Parse([]byte(src))
			if err == nil {
				t.Fatal("expected an error")
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q should mention %q", err, tc.want)
			}
		})
	}
}

func TestResolve(t *testing.T) {
	f := File{Harness: ClaudeCode, Settings: []Setting{
		{Key: "old", Harness: ClaudeCode, Applyable: true},
		{Key: "new", Harness: ClaudeCode, Applyable: true, Since: "2.1.288"},
		{Key: "gone", Harness: ClaudeCode, Applyable: true, Until: "2.1.300"},
		{Key: "advisory", Harness: ClaudeCode},
	}}
	tests := []struct {
		installed string
		want      map[string]Status
		usable    []string
	}{
		{"2.1.314 (Claude Code)", map[string]Status{"old": StatusOK, "new": StatusOK, "gone": StatusRemoved, "advisory": StatusOK}, []string{"old", "new"}},
		{"2.1.250", map[string]Status{"old": StatusOK, "new": StatusTooOld, "gone": StatusOK, "advisory": StatusOK}, []string{"old", "gone"}},
		{"", map[string]Status{"old": StatusUnverified, "new": StatusUnverified, "gone": StatusUnverified, "advisory": StatusUnverified}, nil},
		{"garbage", map[string]Status{"old": StatusUnverified, "new": StatusUnverified, "gone": StatusUnverified, "advisory": StatusUnverified}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.installed, func(t *testing.T) {
			var usable []string
			for _, r := range f.Resolve(tc.installed) {
				if r.Status != tc.want[r.Key] {
					t.Errorf("%s: status %s, want %s", r.Key, r.Status, tc.want[r.Key])
				}
				if r.Usable() {
					usable = append(usable, r.Key)
				}
			}
			if strings.Join(usable, ",") != strings.Join(tc.usable, ",") {
				t.Errorf("usable = %v, want %v", usable, tc.usable)
			}
		})
	}
}

func TestVersionLess(t *testing.T) {
	a, _ := ParseVersion("0.134.0")
	b, _ := ParseVersion("0.99.9")
	if !b.Less(a) || a.Less(b) || a.Less(a) {
		t.Error("version ordering is wrong")
	}
}
