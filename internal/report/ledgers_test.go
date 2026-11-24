package report

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/store"
)

// lrow is one seeded history request for the ledger sections.
type lrow struct {
	session, agent string
	at             time.Time
	model, ver     string
	effort, phase  string
	compacted      bool
	fast           bool
	read, write    int64
	out            int64
	thinking       any // int64 or nil
	text           any // int64 or nil (read before sizes were recorded)
	toolBytes      int64
}

var lseq int

func addLedger(t *testing.T, db *store.DB, r lrow) {
	t.Helper()
	lseq++
	key := fmt.Sprintf("lk%03d", lseq)
	side, agent := 0, any(nil)
	if r.agent != "" {
		side, agent = 1, r.agent
	}
	var phase any
	if r.phase != "" {
		phase = r.phase
	}
	exec(t, db, `INSERT INTO history_requests(request_key, source, session_key, ts, model, input_tokens, cache_write_5m,
		cache_read, output_tokens, cost_usd, pricing_status, plan, sidechain, agent_key, agent_version, effort, phase,
		compacted, fast_mode, thinking_tokens, text_bytes)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		key, "claude_code", r.session, r.at.UnixMilli(), r.model, 50, r.write, r.read, r.out, 0.01, "priced", "api_key",
		side, agent, r.ver, r.effort, phase, boolInt(r.compacted), boolInt(r.fast), r.thinking, r.text)
	if r.toolBytes > 0 {
		exec(t, db, `INSERT INTO history_tool_calls(request_key, idx, ts, tool_name, input_bytes) VALUES(?,?,?,?,?)`,
			key, 0, r.at.UnixMilli(), "Edit", r.toolBytes)
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func seedLedgers(t *testing.T, db *store.DB) {
	t.Helper()
	installedDaysAgo(t, db, 60)
	const s55, h55, o5 = "claude-sonnet-5-5", "claude-haiku-5-5", "claude-opus-5"
	at := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	m := func(min int) time.Time { return at.Add(time.Duration(min) * time.Minute) }
	n := func(v int64) any { return v }
	for _, r := range []lrow{
		// Session A, main conversation.
		{session: "c-a", at: m(0), model: s55, ver: "2.1.330", effort: "medium", phase: "prompt", write: 30_000, out: 800, thinking: n(500), text: n(600), toolBytes: 300},
		{session: "c-a", at: m(1), model: s55, ver: "2.1.330", effort: "medium", phase: "tool_result", read: 30_000, write: 2_000, out: 400, thinking: n(100), text: n(0), toolBytes: 700},
		{session: "c-a", at: m(2), model: s55, ver: "2.1.330", effort: "medium", phase: "tool_result", write: 35_000, out: 300, thinking: n(0), text: n(900)},
		{session: "c-a", at: m(20), model: s55, ver: "2.1.330", effort: "high", phase: "prompt", write: 36_000, out: 2_000, thinking: n(1_500), text: n(1_200), toolBytes: 400},
		{session: "c-a", at: m(21), model: h55, ver: "2.1.330", effort: "high", phase: "tool_result", write: 37_000, out: 500, thinking: n(200), text: n(700)},
		{session: "c-a", at: m(22), model: h55, ver: "2.1.330", effort: "high", phase: "prompt", compacted: true, write: 25_000, out: 600, thinking: n(300), text: n(800)},
		{session: "c-a", at: m(23), model: h55, ver: "2.1.331", effort: "high", phase: "tool_result", write: 25_000, out: 100, thinking: n(0), text: n(300)},
		// Session A, a subagent: its first request rebuilds; no thinking count, so thinking is estimated.
		{session: "c-a", agent: "ag1", at: m(5), model: s55, ver: "2.1.330", effort: "medium", phase: "prompt", write: 22_000, out: 1_000, text: n(1_400)},
		{session: "c-a", agent: "ag1", at: m(6), model: s55, ver: "2.1.330", effort: "medium", phase: "tool_result", read: 22_000, write: 1_000, out: 300, text: n(350), toolBytes: 350},
		// Session B starts before the period: its first in-period request compares with the earlier one.
		{session: "c-b", at: time.Date(2026, 9, 30, 23, 59, 0, 0, time.UTC), model: o5, ver: "2.1.329", effort: "medium", phase: "prompt", write: 30_000, out: 100, thinking: n(0), text: n(350)},
		{session: "c-b", at: time.Date(2026, 10, 1, 0, 1, 0, 0, time.UTC), model: o5, ver: "2.1.329", effort: "medium", phase: "tool_result", read: 30_000, write: 1_000, out: 100, thinking: n(0), text: n(350)},
		{session: "c-b", at: time.Date(2026, 10, 1, 0, 2, 0, 0, time.UTC), model: o5, ver: "2.1.329", effort: "high", phase: "prompt", write: 31_000, out: 100, thinking: n(0), text: n(350)},
		{session: "c-b", at: time.Date(2026, 10, 1, 0, 3, 0, 0, time.UTC), model: o5, ver: "2.1.329", effort: "high", phase: "tool_result", fast: true, write: 32_000, out: 100, thinking: n(0), text: n(350)},
		// Session C was read before sizes were recorded: its output cannot be split.
		{session: "c-c", at: m(60), model: s55, write: 5_000, out: 700},
	} {
		addLedger(t, db, r)
	}
}

func TestLedgerSectionsGolden(t *testing.T) {
	db := openDB(t)
	seedLedgers(t, db)
	r, err := Build(context.Background(), db, Inputs{Config: config.Default(), Now: now}, mustPeriod(t, Month))
	if err != nil {
		t.Fatal(err)
	}
	if r.Rebuilds == nil || r.OutputMix == nil {
		t.Fatalf("sections missing: %+v %+v", r.Rebuilds, r.OutputMix)
	}
	causes := map[string]int{}
	for _, c := range r.Rebuilds.ByCause {
		causes[c.Cause] = int(c.Count.Value)
	}
	want := map[string]int{"unexplained": 1, "idle_expired": 1, "model_changed": 1, "compaction": 1, "version_changed": 1,
		"subagent_start": 1, "effort_changed": 1, "fast_mode_changed": 1}
	if fmt.Sprint(causes) != fmt.Sprint(want) {
		t.Errorf("causes: got %v, want %v", causes, want)
	}
	// Session B's start is before the period, so only session A's start is listed.
	if r.Rebuilds.SessionStarts.Value != 1 || r.Rebuilds.Count.Value != 8 {
		t.Errorf("counts: %+v", r.Rebuilds)
	}
	if r.OutputMix.Total.Unsplit.Value != 700 || r.OutputMix.Total.Thinking.Label != "estimated" {
		t.Errorf("output total: %+v", r.OutputMix.Total)
	}
	if n := assertLabeled(t, r); n == 0 {
		t.Fatal("no labelled figures")
	}
	golden(t, "ledgers.txt", Render(r, Options{Width: 80}))
	golden(t, "ledgers.json", toJSON(t, r))
}

// Without Claude Code history there are no ledger sections, and old rows alone show no output split.
func TestLedgerSectionsAbsent(t *testing.T) {
	db := openDB(t)
	seedHistory(t, db, "api_key")
	r, err := Build(context.Background(), db, Inputs{Config: config.Default(), Now: now}, mustPeriod(t, Month))
	if err != nil {
		t.Fatal(err)
	}
	if r.Rebuilds != nil || r.OutputMix != nil {
		t.Errorf("want no ledger sections: %+v %+v", r.Rebuilds, r.OutputMix)
	}
}
