package fixer

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/store"
)

// gateway gives the fixture a database whose requests carry the analyzer's
// per-server tool-definition estimates. calibrated=false rows must be ignored.
func (f *fx) gateway(rows []map[string]int64, calibrated bool) {
	f.t.Helper()
	db, err := store.Open(f.env.DataDir)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = db.Close() })
	for i, r := range rows {
		var parts []string
		for k, v := range r {
			parts = append(parts, fmt.Sprintf("%q:%d", k, v))
		}
		est := fmt.Sprintf(`{"label":"estimated","calibrated":%v,"total_tokens":50000,"categories":{},"tool_servers":{%s}}`, calibrated, strings.Join(parts, ","))
		if _, err := db.Exec(`INSERT INTO requests (ts, session_key, provider, model, segment_token_estimates) VALUES (?, ?, 'anthropic', 'claude-opus-5-5', ?)`,
			now.Add(-time.Duration(i+1)*time.Hour).UnixMilli(), fmt.Sprintf("g%d", i), est); err != nil {
			f.t.Fatal(err)
		}
	}
	f.env.DB = db.DB
}

// Every fixed-overhead fix shows the tokens it re-sends on every request and
// how many requests a session makes, each with an honest label.
func TestFixedOverheadFixesShowResentTokens(t *testing.T) {
	f := newFx(t)
	f.write(".mcp.json", mcpJSON)
	f.write("README.md", readme)
	f.write("CLAUDE.md", claudeMD())
	f.writeUser(`{"env":{"ENABLE_TOOL_SEARCH":"false"}}`)
	f.sessions(6, []string{"Read", "mcp__github__create_issue"}, f.idle()) // 2 requests per session
	f.gateway([]map[string]int64{{"notion": 1200, "github": 800}, {"notion": 1000, "github": 600}}, true)

	all := map[string]*Fix{}
	for _, k := range []string{MCPPrune, ContextSlim, SettingsCheck} {
		for _, x := range f.detect(k) {
			all[x.Kind] = x
		}
	}
	tests := []struct {
		kind      string
		perReq    int64
		label     string
		evidence  string
		sessTotal int64
	}{
		{MCPPrune, 1100, "measured", "1100 tokens of tool definitions re-sent on every request", 2200},
		{SettingsCheck, 1800, "measured", "1800 tokens of MCP tool definitions re-sent on every request", 0},
		{ContextSlim, -1, "estimated", "re-sent on every request", -1},
	}
	for _, tc := range tests {
		x := all[tc.kind]
		if x == nil {
			t.Fatalf("no %s fix", tc.kind)
		}
		if !x.FixedOverhead {
			t.Errorf("%s: not marked fixed overhead", tc.kind)
		}
		if tc.perReq >= 0 && x.ResentTokensPerRequest != tc.perReq {
			t.Errorf("%s: re-sent per request = %d, want %d", tc.kind, x.ResentTokensPerRequest, tc.perReq)
		}
		if x.ResentTokensPerRequest <= 0 {
			t.Errorf("%s: no re-sent figure", tc.kind)
		}
		if x.ResentLabel != tc.label {
			t.Errorf("%s: label %q, want %q", tc.kind, x.ResentLabel, tc.label)
		}
		if x.RequestsPerSession != 2 || x.RequestsLabel != "measured" {
			t.Errorf("%s: requests per session %v (%q), want 2 measured from history", tc.kind, x.RequestsPerSession, x.RequestsLabel)
		}
		if !strings.Contains(strings.Join(x.Evidence, "\n"), tc.evidence) {
			t.Errorf("%s: evidence %q lacks %q", tc.kind, x.Evidence, tc.evidence)
		}
		if tc.sessTotal >= 0 && x.TokensPerSession != tc.sessTotal {
			t.Errorf("%s: per session %d, want %d", tc.kind, x.TokensPerSession, tc.sessTotal)
		}
	}
}

// Uncalibrated estimates are not provider-backed, so they are no measurement.
func TestResentTokensNeedCalibratedGatewayData(t *testing.T) {
	f := newFx(t)
	f.write(".mcp.json", mcpJSON)
	f.sessions(6, []string{"Read", "mcp__github__create_issue"}, f.idle())
	f.gateway([]map[string]int64{{"notion": 1200}}, false)
	x := f.detect(MCPPrune)[0]
	if x.ResentTokensPerRequest != 0 || x.ResentLabel != "" || x.TokensPerSession != 0 {
		t.Fatalf("uncalibrated data must give no figure: %+v", x)
	}
	if x.RequestsPerSession != 2 {
		t.Errorf("requests per session still comes from history: %v", x.RequestsPerSession)
	}
}
