package report

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/measure"
)

func seedTools(t *testing.T, fetches int) (r Report) {
	t.Helper()
	db := openDB(t)
	seedHistory(t, db, "api_key")
	// Claude Code history with Trove and built-in tool calls.
	exec(t, db, `UPDATE history_requests SET tool_names = '{"mcp__trove__trove_read":2,"mcp__trove__trove_search":1,"mcp__trove__trove_run":3,"Read":5,"Grep":2,"Bash":10}' WHERE request_key = 'k0'`)
	// Gateway: two sessions whose last request shows 30 trimmed outputs in all.
	for i, sg := range []string{
		fmt.Sprintf(`{"trimmed":20,"trove_fetches":%d,"trove_calls":%d}`, fetches, fetches),
		`{"trimmed":10,"trove_fetches":0,"trove_calls":0}`,
	} {
		exec(t, db, `INSERT INTO requests(ts, session_key, agent, signals) VALUES(?,?,?,?)`,
			now.Add(-time.Duration(i+1)*time.Hour).UnixMilli(), fmt.Sprintf("c-gw%d", i), "claude_code", sg)
	}
	r, err := Build(context.Background(), db, Inputs{Config: config.Default(), Now: now, Recoverable: true, SchemaTokens: 351}, mustPeriod(t, Month))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestToolsSectionLabelsEveryNumber(t *testing.T) {
	r := seedTools(t, 3)
	if r.Tools == nil || !r.Tools.Registered {
		t.Fatalf("tools section missing: %+v", r.Tools)
	}
	a := r.Tools.Adoption
	if a.Rate == nil || a.Rate.Label != measure.Estimated {
		t.Errorf("adoption from session logs must be estimated: %+v", a.Rate)
	}
	if a.FetchBack == nil || a.FetchBack.Label != measure.Measured || a.FetchBack.Value != 10 || a.TooAggressive {
		t.Errorf("fetch-back from the gateway must be measured: %+v", a.FetchBack)
	}
	if r.Tools.SchemaTokens == nil || r.Tools.SchemaTokens.Label != measure.Estimated {
		t.Errorf("schema size must be estimated: %+v", r.Tools.SchemaTokens)
	}
	text := Render(r, Options{Width: 80})
	for _, want := range []string{
		"Trove tools (MCP)",
		"about 351 tokens to every request (estimated)",
		"(estimated from session logs; 10 calls in 1 session(s))", // 3 of 10 reads and searches
		"Fetch-back (measured): 3 trove_fetch_full calls for 30 trimmed outputs, 10%.",
	} {
		if !strings.Contains(strings.Join(strings.Fields(text), " "), strings.Join(strings.Fields(want), " ")) {
			t.Errorf("report is missing %q:\n%s", want, text)
		}
	}
	golden(t, "tools.txt", Render(r, Options{Width: 80}))
	golden(t, "tools.json", toJSON(t, r))
}

func TestToolsSectionFlagsAggressiveTrimming(t *testing.T) {
	r := seedTools(t, 15) // 15 of 30 = 50%
	if !r.Tools.Adoption.TooAggressive {
		t.Fatalf("%+v", r.Tools.Adoption)
	}
	text := strings.Join(strings.Fields(Render(r, Options{Width: 80})), " ")
	if !strings.Contains(text, "trimming may be too aggressive") || !strings.Contains(text, `output_trimmer = "shadow"`) {
		t.Errorf("missing advice:\n%s", text)
	}
}

func TestNoToolsSectionWhenNotRegisteredAndUnused(t *testing.T) {
	db := openDB(t)
	seedHistory(t, db, "api_key")
	r, err := Build(context.Background(), db, Inputs{Config: config.Default(), Now: now}, mustPeriod(t, Month))
	if err != nil {
		t.Fatal(err)
	}
	if r.Tools != nil || strings.Contains(Render(r, Options{Width: 80}), "Trove tools") {
		t.Error("the section should not appear when there is nothing to say")
	}
}
