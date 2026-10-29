package suggest

import (
	"context"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/store"
)

var (
	day  = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	from = day.AddDate(0, 0, -1)
	to   = day.AddDate(0, 0, 1)
)

type row struct {
	model, agent, endpoint, findings string
	streamed                         bool
	status                           int
	cost                             float64
	out                              int64
	tier                             string
}

func seed(t *testing.T, rows []row, copies int) (*store.DB, *pricing.Table) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for i := 0; i < copies; i++ {
		for _, r := range rows {
			if r.endpoint == "" {
				r.endpoint = "/v1/messages"
			}
			if r.status == 0 {
				r.status = 200
			}
			var findings any
			if r.findings != "" {
				findings = r.findings
			}
			if _, err := db.Exec(`INSERT INTO requests (ts, provider, model, agent, endpoint, streamed, status_code, cost_usd, output_tokens, service_tier, findings)
				VALUES (?, 'anthropic', ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				day.UnixMilli(), r.model, r.agent, r.endpoint, r.streamed, r.status, r.cost, r.out, r.tier, findings); err != nil {
				t.Fatal(err)
			}
		}
	}
	tbl, err := pricing.Default()
	if err != nil {
		t.Fatal(err)
	}
	return db, tbl
}

func find(ss []Suggestion, kind string) *Suggestion {
	for i := range ss {
		if ss[i].Kind == kind {
			return &ss[i]
		}
	}
	return nil
}

func TestBatchAPI(t *testing.T) {
	scripted := row{model: "claude-sonnet-5-5", agent: "sdk", cost: 0.02}
	db, tbl := seed(t, []row{
		scripted,
		{model: "claude-sonnet-5-5", agent: "claude_code", cost: 5},                   // interactive: ignored
		{model: "claude-sonnet-5-5", agent: "sdk", cost: 5, streamed: true},           // someone is watching it stream: ignored
		{model: "claude-sonnet-5-5", agent: "sdk", cost: 5, status: 429},              // failed: ignored
		{model: "claude-sonnet-5-5", agent: "sdk", cost: 5, tier: "batch"},            // already batch: ignored
		{model: "claude-sonnet-5-5", agent: "other", cost: 5, endpoint: "/v1/models"}, // not a message
	}, 30)
	ss, err := Generate(context.Background(), db, tbl, from, to)
	if err != nil {
		t.Fatal(err)
	}
	b := find(ss, KindBatchAPI)
	if b == nil || b.Requests != 30 {
		t.Fatalf("batch suggestion wrong: %+v", b)
	}
	// 30 x $0.02 = $0.60; the docs and the price list both say batch is 50% off.
	if b.EstSavingsUSD == nil || math.Abs(*b.EstSavingsUSD-0.30) > 1e-9 || b.Label != "estimated" || b.Confidence != "low" {
		t.Errorf("savings = %v, want 0.30 (50%% of $0.60): %+v", b.EstSavingsUSD, b)
	}
	if !strings.Contains(b.Detail, "50% off") || !strings.Contains(b.Detail, "24") {
		t.Errorf("detail should state the discount and the latency: %s", b.Detail)
	}
}

func TestBatchAPIRequiresEnoughTraffic(t *testing.T) {
	db, tbl := seed(t, []row{{model: "claude-sonnet-5-5", agent: "sdk", cost: 0.02}}, 5)
	ss, _ := Generate(context.Background(), db, tbl, from, to)
	if len(ss) != 0 {
		t.Errorf("5 requests is not enough to suggest anything: %+v", ss)
	}
}

func TestModelChoiceAggregatesFindings(t *testing.T) {
	f := `[{"detector":"expensive_model_small_task","tokens":900,"usd":0.05,"confidence":"low","label":"estimated","detail":{"alternative":"claude-haiku-5-5"}},` +
		`{"detector":"duplicate_tool_result","tokens":10,"usd":9,"confidence":"high","label":"estimated"}]`
	db, tbl := seed(t, []row{{model: "claude-opus-5-5", agent: "claude_code", cost: 0.4, findings: f}}, 25)
	ss, err := Generate(context.Background(), db, tbl, from, to)
	if err != nil {
		t.Fatal(err)
	}
	m := find(ss, KindModelChoice)
	if m == nil || m.Requests != 25 || m.EstSavingsUSD == nil || math.Abs(*m.EstSavingsUSD-1.25) > 1e-9 {
		t.Fatalf("model suggestion wrong: %+v", m)
	}
	if m.Confidence != "low" || !strings.Contains(m.Title, "claude-haiku-5-5") || !strings.Contains(m.Detail, "cannot check") {
		t.Errorf("must say it is a low-confidence suggestion: %+v", m)
	}
}

func TestReasoningEffortNeverInventsASavingsFigure(t *testing.T) {
	tbl, err := pricing.Default()
	if err != nil {
		t.Fatal(err)
	}
	e, _ := tbl.Lookup(pricing.Anthropic, "claude-sonnet-5-5", day)
	if e == nil {
		t.Skip("no price for the test model")
	}
	// Make output about 75% of spend: 1M output tokens per 10 requests.
	outPrice := parse(t, e.Standard.Output.String())
	perReq := 100_000.0
	outCost := perReq * outPrice / 1e6
	db, _ := seed(t, []row{{model: "claude-sonnet-5-5", agent: "claude_code", cost: outCost / 0.75, out: int64(perReq)}}, 30)
	ss, err := Generate(context.Background(), db, tbl, from, to)
	if err != nil {
		t.Fatal(err)
	}
	r := find(ss, KindReasoningEffort)
	if r == nil {
		t.Fatalf("expected an effort suggestion: %+v", ss)
	}
	if r.EstSavingsUSD != nil {
		t.Errorf("Anthropic publishes no savings figure for lower effort, so none may be shown: %v", *r.EstSavingsUSD)
	}
	if !strings.Contains(r.Title, `"medium"`) || !strings.Contains(r.Scenario, "10%") || r.Confidence != "low" {
		t.Errorf("unexpected: %+v", r)
	}
}

func parse(t *testing.T, s string) float64 {
	t.Helper()
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestEffortDefaultsMatchTheDocs(t *testing.T) {
	for model, want := range map[string]string{
		"claude-opus-5-5": "medium", "claude-haiku-5-5": "medium", "claude-sonnet-5-5": "high",
		"claude-opus-4-8-20260101": "high", "claude-sonnet-5": "high",
	} {
		if got, ok := effortDefault(model); !ok || got != want {
			t.Errorf("effortDefault(%q) = %q,%v want %q", model, got, ok, want)
		}
	}
	for _, model := range []string{"claude-haiku-4-5", "claude-3-5-haiku", "gpt-5", "claude-opus-4-9"} {
		if _, ok := effortDefault(model); ok {
			t.Errorf("%q does not support effort in the docs", model)
		}
	}
}

func TestGenerateWithoutPricesStillWorks(t *testing.T) {
	db, _ := seed(t, nil, 0)
	if ss, err := Generate(context.Background(), db, nil, from, to); err != nil || len(ss) != 0 {
		t.Errorf("%v %v", ss, err)
	}
}
