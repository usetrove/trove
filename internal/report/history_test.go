package report

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/store"
)

const histFindings = `[{"detector":"oversized_tool_output","tokens":8000,"usd":0.12,"confidence":"low","label":"estimated"}]`

func addHist(t *testing.T, db *store.DB, key string, ts time.Time, session, plan, model string, cost any, findings string) {
	t.Helper()
	var f any
	if findings != "" {
		f = findings
	}
	exec(t, db, `INSERT INTO history_requests(request_key, source, session_key, ts, model, input_tokens, cache_write_1h, cache_read,
		output_tokens, cost_usd, pricing_status, plan, findings) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		key, "claude_code", session, ts.UnixMilli(), model, 100, 2000, 40000, 500, cost, "priced", plan, f)
}

func seedHistory(t *testing.T, db *store.DB, plan string) {
	t.Helper()
	installedDaysAgo(t, db, 60)
	for i := 0; i < 24; i++ {
		var fnd string
		if i%3 == 0 {
			fnd = histFindings
		}
		addHist(t, db, fmt.Sprintf("k%d", i), now.Add(-time.Duration(i+1)*time.Hour), fmt.Sprintf("c-s%d", i%4), plan, "claude-a", 0.5, fnd)
	}
	exec(t, db, `INSERT INTO history_files(file_key, source, size, mtime, offset, parser_version, lines, skipped_lines, updated_at)
		VALUES('f1','claude_code',1,1,1,1,10,3,1)`)
}

func TestHistoryOnlyReports(t *testing.T) {
	for _, plan := range []string{"api_key", "unknown"} {
		t.Run(plan, func(t *testing.T) {
			db := openDB(t)
			seedHistory(t, db, plan)
			r, err := Build(context.Background(), db, Inputs{Config: config.Default(), Now: now, HistoryPlanEvidence: []string{"env:ANTHROPIC_API_KEY"}}, mustPeriod(t, Month))
			if err != nil {
				t.Fatal(err)
			}
			if r.State != StateOK || r.Requests != 24 || r.History == nil || r.History.Requests != 24 {
				t.Fatalf("state=%s requests=%d history=%+v", r.State, r.Requests, r.History)
			}
			if plan == "api_key" && (r.Headline.Spend.Value != 12 || r.Headline.APIEquivalent != nil) {
				t.Errorf("api key history should be spend: %+v", r.Headline)
			}
			if plan == "unknown" && (r.Headline.Spend.Value != 0 || r.Headline.APIEquivalent == nil || r.Headline.APIEquivalent.Value != 12) {
				t.Errorf("unknown plan history should be API-equivalent, not spend: %+v", r.Headline)
			}
			if r.Headline.Waste.Value <= 0 || len(r.Leaks) == 0 || r.Leaks[0].Category != "oversized_tool_output" {
				t.Errorf("waste/leaks missing: %+v %+v", r.Headline.Waste, r.Leaks)
			}
			assertLabeled(t, r)
			golden(t, "history_"+plan+".txt", Render(r, Options{Width: 80}))
			golden(t, "history_"+plan+".json", toJSON(t, r))
		})
	}
}

// A Claude Code session that went through the gateway is also in its own logs.
// It must be counted once, from the gateway.
func TestHistoryAndGatewayDoNotDoubleCount(t *testing.T) {
	db := openDB(t)
	installedDaysAgo(t, db, 60)
	for i := 0; i < 20; i++ { // gateway: session c-shared (split once by an idle gap) and c-gw
		sess := "c-shared"
		if i%2 == 1 {
			sess = "c-shared~1"
		}
		addReq(t, db, req{ts: now.Add(-time.Duration(i+1) * time.Hour), session: sess, agent: "claude_code", model: "claude-a", cost: 1.0})
	}
	for i := 0; i < 10; i++ { // history: the same session, plus one only history saw
		sess := "c-shared"
		if i >= 5 {
			sess = "c-only-history"
		}
		addHist(t, db, fmt.Sprintf("h%d", i), now.Add(-time.Duration(i+1)*time.Hour), sess, "api_key", "claude-a", 1.0, "")
	}
	r, err := Build(context.Background(), db, Inputs{Config: config.Default(), Now: now}, mustPeriod(t, Month))
	if err != nil {
		t.Fatal(err)
	}
	if r.Requests != 25 || r.Headline.Spend.Value != 25 {
		t.Errorf("want 20 gateway + 5 history-only = 25 requests and $25; got %d and %v", r.Requests, r.Headline.Spend.Value)
	}
	if r.History == nil || r.History.ExcludedSessions != 1 || r.History.Requests != 5 {
		t.Errorf("history: %+v", r.History)
	}
}

func TestNoHistoryLeavesReportUnchanged(t *testing.T) {
	db := openDB(t)
	seedNormal(t, db)
	r, err := Build(context.Background(), db, Inputs{Config: testConfig(), Now: now}, mustPeriod(t, Month))
	if err != nil {
		t.Fatal(err)
	}
	if r.History != nil || r.Headline.APIEquivalent != nil {
		t.Errorf("history fields must be absent without history: %+v", r.History)
	}
}

// Codex sessions cannot be matched by key, so history from the first gateway
// Codex request onward is treated as seen by the gateway.
func TestCodexHistoryAfterFirstGatewayRequestIsNotCountedTwice(t *testing.T) {
	db := openDB(t)
	installedDaysAgo(t, db, 60)
	first := now.Add(-5 * time.Hour)
	for i := 0; i < 20; i++ {
		addReq(t, db, req{ts: first.Add(time.Duration(i) * time.Minute), session: "h-gw", agent: "codex", model: "gpt-a", cost: 1.0})
	}
	for i := 0; i < 10; i++ { // 5 before the gateway was used, 5 after
		ts := first.Add(-time.Duration(i+1) * time.Hour)
		if i >= 5 {
			ts = first.Add(time.Duration(i) * time.Minute)
		}
		exec(t, db, `INSERT INTO history_requests(request_key, source, session_key, ts, model, input_tokens, output_tokens, cost_usd, pricing_status, plan)
			VALUES(?, 'codex', 'o-1', ?, 'gpt-a', 100, 10, 1.0, 'priced', 'api_key')`, fmt.Sprintf("cx%d", i), ts.UnixMilli())
	}
	r, err := Build(context.Background(), db, Inputs{Config: config.Default(), Now: now}, mustPeriod(t, Month))
	if err != nil {
		t.Fatal(err)
	}
	if r.Requests != 25 || r.Headline.Spend.Value != 25 {
		t.Errorf("want 20 gateway + 5 earlier history = 25; got %d requests, $%v", r.Requests, r.Headline.Spend.Value)
	}
}
