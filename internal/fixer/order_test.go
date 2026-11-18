package fixer

import "testing"

// MCP pruning, the tool-search check and context slimming remove overhead re-sent
// on every request, need no proxy and work on subscriptions, so they come first.
func TestFixedOverheadFixesComeFirst(t *testing.T) {
	fixes := []*Fix{
		{ID: "model-1", Kind: ModelDefault, TokensPerSession: 90000},
		{ID: "chk-cache", Kind: SettingsCheck, TokensPerSession: 50000},
		{ID: "ctx-1", Kind: ContextSlim, TokensPerSession: 800},
		{ID: "cache-1", Kind: CacheBreaker, TokensPerSession: 70000},
		{ID: "chk-tools", Kind: SettingsCheck, fixedOverhead: true},
		{ID: "mcp-1", Kind: MCPPrune, TokensPerSession: 3000},
	}
	sortFixes(fixes)
	want := []string{"mcp-1", "ctx-1", "chk-tools", "model-1", "cache-1", "chk-cache"}
	for i, w := range want {
		if fixes[i].ID != w {
			t.Errorf("fix %d is %s, want %s", i, fixes[i].ID, w)
		}
		if got := fixes[i].FixedOverhead; got != (i < 3) {
			t.Errorf("fix %s fixed_overhead=%v, want %v", w, got, i < 3)
		}
	}
}

func TestToolSearchCheckIsFixedOverhead(t *testing.T) {
	e := &Env{}
	for _, c := range costlySettings {
		f := e.settingsCheckFix(c, "/p/settings.json", []byte(`{"env":{"`+c.name+`":"`+c.badVal+`"}}`), 1)
		if want := c.key == "toolsearch"; f.fixedOverhead != want {
			t.Errorf("%s: fixedOverhead=%v, want %v", c.name, f.fixedOverhead, want)
		}
	}
}
