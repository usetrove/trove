package measure

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/store"
)

func TestClassifyTool(t *testing.T) {
	tests := []struct{ name, want string }{
		{"mcp__trove__trove_read", ToolTroveRead},
		{"mcp__trove__trove_search", ToolTroveSearch},
		{"mcp__trove__trove_run", ToolTroveRun},
		{"mcp__trove__trove_fetch_full", ToolTroveFetch},
		{"trove.trove_read", ToolTroveRead},
		{"Read", ToolBuiltinRead},
		{"Grep", ToolBuiltinFind},
		{"Glob", ToolBuiltinFind},
		{"Bash", ToolBuiltinBash},
		{"Edit", ToolOther},
		{"mcp__other__trove_read", ToolOther}, // another server's tool with a similar name
		{"mcp__github__search", ToolOther},
		{"trove_read_extra", ToolOther},
		{"", ToolOther},
	}
	for _, tc := range tests {
		if got := ClassifyTool(tc.name); got != tc.want {
			t.Errorf("ClassifyTool(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestExtractCountsTroveToolsAndTrimMarkers(t *testing.T) {
	marker := "[Trove: 120 lines trimmed; full output sha256:0123456789abcdef]"
	body := `{"messages":[
	 {"role":"assistant","content":[{"type":"tool_use","id":"a","name":"mcp__trove__trove_run","input":{"name":"test"}}]},
	 {"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"FAIL x\n` + marker + `\nok"}]},
	 {"role":"assistant","content":[{"type":"tool_use","id":"b","name":"mcp__trove__trove_fetch_full","input":{"output_id":"0123456789abcdef"}}]},
	 {"role":"user","content":[{"type":"tool_result","tool_use_id":"b","content":[{"type":"text","text":"full text"}]}]},
	 {"role":"assistant","content":[{"type":"tool_use","id":"c","name":"Bash","input":{"command":"go build"}}]},
	 {"role":"user","content":[{"type":"tool_result","tool_use_id":"c","content":[{"type":"text","text":"x ` + marker + `"}]}]}
	]}`
	s, ok := Extract([]byte(body))
	if !ok {
		t.Fatal("not read")
	}
	if s.TroveCalls != 2 || s.TroveFetches != 1 || s.Trimmed != 2 {
		t.Errorf("%+v", s)
	}
	// Names and ids never reach the stored JSON.
	out := string(s.JSON())
	for _, leak := range []string{"mcp__", "0123456789abcdef", "go build"} {
		if strings.Contains(out, leak) {
			t.Errorf("signals JSON leaks %q: %s", leak, out)
		}
	}
}

func addHistory(t *testing.T, db *store.DB, n int, source, session string, ts time.Time, tools map[string]int) {
	t.Helper()
	b, _ := json.Marshal(tools)
	if _, err := db.ExecContext(context.Background(), `INSERT INTO history_requests(request_key, source, session_key, ts, tool_names)
		VALUES(?,?,?,?,?)`, fmt.Sprintf("k%d-%s", n, session), source, session, ts.UnixMilli(), string(b)); err != nil {
		t.Fatal(err)
	}
}

func addGateway(t *testing.T, db *store.DB, session string, ts time.Time, s Signals) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `INSERT INTO requests(ts, session_key, signals) VALUES(?,?,?)`,
		ts.UnixMilli(), session, string(s.JSON())); err != nil {
		t.Fatal(err)
	}
}

func TestAdoptionRateFromHistoryIsEstimated(t *testing.T) {
	db := openDB(t)
	w := Window{From: t0, To: t0.Add(24 * time.Hour)}
	addHistory(t, db, 1, "claude_code", "c-1", t0.Add(time.Hour), map[string]int{
		"mcp__trove__trove_read": 3, "mcp__trove__trove_search": 1, "Read": 4, "Grep": 2, "Glob": 1, "Bash": 9, "mcp__trove__trove_run": 2,
	})
	addHistory(t, db, 2, "claude_code", "c-2", t0.Add(2*time.Hour), map[string]int{"Read": 1})
	addHistory(t, db, 3, "codex", "x-1", t0.Add(2*time.Hour), map[string]int{"trove.trove_read": 5, "shell": 7})
	addHistory(t, db, 4, "claude_code", "c-3", t0.Add(48*time.Hour), map[string]int{"Read": 100}) // outside the window

	a, err := AdoptionFor(context.Background(), db, w, config.Default().Measure)
	if err != nil {
		t.Fatal(err)
	}
	if a.TroveRead != 3 || a.TroveSearch != 1 || a.TroveRun != 2 || a.BuiltinRead != 5 || a.BuiltinFind != 3 || a.BuiltinBash != 9 {
		t.Errorf("counts: %+v", a)
	}
	if a.CodexTroveCalls != 5 || a.ClaudeSessions != 2 {
		t.Errorf("codex/sessions: %+v", a)
	}
	// 4 Trove reads+searches of (4 + 8 built-in reads/searches) = 33.3%; Bash is not in the rate.
	if a.Rate == nil || a.Rate.Label != Estimated || fmt.Sprintf("%.1f", a.Rate.Value) != "33.3" || a.Rate.N != 12 {
		t.Errorf("rate: %+v", a.Rate)
	}
	if !a.Any() {
		t.Error("Any should be true")
	}
}

func TestFetchBackRateIsMeasuredAndNeedsASample(t *testing.T) {
	db := openDB(t)
	w := Window{From: t0, To: t0.Add(24 * time.Hour)}
	m := config.Default().Measure // re-fetch limit 25%

	// Earlier requests of a session are ignored: its last request carries the whole history.
	addGateway(t, db, "c-1", t0.Add(time.Hour), Signals{Trimmed: 3, TroveFetches: 3})
	addGateway(t, db, "c-1", t0.Add(2*time.Hour), Signals{Trimmed: 10, TroveFetches: 1})
	a, _ := AdoptionFor(context.Background(), db, w, m)
	if a.Trimmed != 10 || a.Fetches != 1 || a.FetchBack != nil || a.TooAggressive {
		t.Fatalf("below the sample size no rate may be shown: %+v", a)
	}

	addGateway(t, db, "c-2", t0.Add(3*time.Hour), Signals{Trimmed: 10, TroveFetches: 2})
	a, _ = AdoptionFor(context.Background(), db, w, m)
	if a.FetchBack == nil || a.FetchBack.Label != Measured || a.FetchBack.Value != 15 || a.FetchBack.N != 20 || a.TooAggressive {
		t.Fatalf("3 fetches of 20 trimmed should be 15%%, fine: %+v", a.FetchBack)
	}

	addGateway(t, db, "c-3", t0.Add(4*time.Hour), Signals{Trimmed: 10, TroveFetches: 9})
	a, _ = AdoptionFor(context.Background(), db, w, m)
	if a.FetchBack == nil || !a.TooAggressive || a.GatewaySessions != 3 {
		t.Fatalf("12 of 30 = 40%% should flag aggressive trimming: %+v", a)
	}
}

func TestAdoptionEmpty(t *testing.T) {
	db := openDB(t)
	a, err := AdoptionFor(context.Background(), db, Window{From: t0, To: t0.Add(time.Hour)}, config.Default().Measure)
	if err != nil || a.Any() || a.Rate != nil || a.FetchBack != nil {
		t.Fatalf("%+v %v", a, err)
	}
}
