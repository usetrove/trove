package logs_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/analyze"
	"github.com/usetrove/trove/internal/logs"
	"github.com/usetrove/trove/internal/pricing"
)

func frontierModel(t *testing.T, tbl *pricing.Table) string {
	t.Helper()
	for _, e := range tbl.Entries() {
		if e.Provider == pricing.Anthropic && e.EffectiveFrom != nil && e.EffectiveFrom.Before(time.Now()) {
			if e.Standard.Input.Cmp(pricing.MustDec("5")) >= 0 {
				return e.Model
			}
		}
	}
	t.Skip("no frontier model in the price table")
	return ""
}

func TestAuditFindsOversizedDuplicateAndExpensive(t *testing.T) {
	tbl, _ := pricing.Default()
	model := "claude-sonnet-5-5"
	ts := time.Now().Add(-time.Hour).UnixMilli()
	ev := func(n int, tools ...logs.ToolCall) logs.Event {
		return logs.Event{Source: logs.SourceClaudeCode, SessionKey: "c-1", TS: ts + int64(n), Model: model,
			Input: 100, CacheWrite1h: 40000, CacheRead: 1000, Output: 200, Tools: tools}
	}
	events := []logs.Event{
		ev(1, logs.ToolCall{Name: "Bash", ResultBytes: 150_000, ResultHash: "a"}),
		ev(2, logs.ToolCall{Name: "Read", ResultBytes: 5_000, ResultHash: "b"}),
		ev(3, logs.ToolCall{Name: "Read", ResultBytes: 5_000, ResultHash: "b"}), // same result again
		ev(4),
		ev(5, logs.ToolCall{Name: "Bash", ResultBytes: 150_000, ResultHash: "c"}), // last request: result never sent
	}
	rows := make([]logs.Row, len(events))
	logs.NewAuditor(tbl).Annotate(events, rows)

	findings := func(i int) map[string]analyze.Finding {
		m := map[string]analyze.Finding{}
		if rows[i].Findings == nil {
			return m
		}
		var fs []analyze.Finding
		if err := json.Unmarshal(rows[i].Findings, &fs); err != nil {
			t.Fatal(err)
		}
		for _, f := range fs {
			m[f.Detector] = f
		}
		return m
	}
	// The oversized result from request 0 is billed on request 1.
	o := findings(1)[analyze.DetOversized]
	bpt := float64(analyze.DefaultBytesPerToken)
	wantTokens := int64(150000/bpt) - 10000
	if o.Tokens != wantTokens || o.Label != "estimated" || o.USD == nil || *o.USD <= 0 {
		t.Errorf("oversized on request 1: %+v", o)
	}
	// The repeat of result "b" (request 2) is billed on request 3.
	if d := findings(3)[analyze.DetDuplicate]; d.Tokens != int64(5000/bpt) {
		t.Errorf("duplicate on request 3: %+v", d)
	}
	if len(findings(0)) != 0 || len(findings(2)) != 0 {
		t.Errorf("findings landed on the wrong request: %v %v", findings(0), findings(2))
	}
	if rows[1].ShadowSavings == nil || !strings.Contains(string(rows[1].ShadowSavings), "output_trimmer") {
		t.Errorf("shadow savings missing: %s", rows[1].ShadowSavings)
	}
	for i := range rows {
		if _, ok := findings(i)[analyze.DetOversized]; ok && i == 5 {
			t.Error("a result that was never sent back must not produce a finding")
		}
	}
}

func TestAuditExpensiveModelNeedsSmallRequest(t *testing.T) {
	tbl, _ := pricing.Default()
	model := frontierModel(t, tbl)
	small := logs.Event{Source: logs.SourceClaudeCode, SessionKey: "c-1", TS: time.Now().Add(-time.Hour).UnixMilli(), Model: model, Input: 1000, Output: 100}
	big := small
	big.Input = 90000
	events, rows := []logs.Event{small, big}, make([]logs.Row, 2)
	logs.NewAuditor(tbl).Annotate(events, rows)
	if rows[0].Findings == nil || !strings.Contains(string(rows[0].Findings), analyze.DetExpensive) {
		t.Errorf("small request on a frontier model should be flagged: %s", rows[0].Findings)
	}
	if rows[1].Findings != nil {
		t.Errorf("large request must not be flagged: %s", rows[1].Findings)
	}
}

func TestAuditSkipsUnpricedCacheWrites(t *testing.T) {
	tbl, _ := pricing.Default()
	e := logs.Event{Source: logs.SourceClaudeCode, SessionKey: "c-1", TS: time.Now().Add(-time.Hour).UnixMilli(), Model: frontierModel(t, tbl), Input: 100, Output: 50, CacheWriteNA: 500}
	rows := make([]logs.Row, 1)
	logs.NewAuditor(tbl).Annotate([]logs.Event{e}, rows)
	if rows[0].Findings != nil {
		t.Errorf("a request that cannot be priced exactly must not get a dollar finding: %s", rows[0].Findings)
	}
}
