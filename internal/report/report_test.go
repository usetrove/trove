package report

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/store"
)

func mustPeriod(t *testing.T, name string) Period {
	t.Helper()
	p, err := NewPeriod(name, "", now)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReportGoldens(t *testing.T) {
	budget := func(cfg config.Config) config.Config {
		cfg.Budget.CapUSD = 1000
		return cfg
	}
	tests := []struct {
		name   string
		period string
		seed   func(*testing.T, *store.DB)
		cfg    config.Config
		width  int
	}{
		{"empty", Month, func(*testing.T, *store.DB) {}, config.Default(), 80},
		{"quiet", Today, seedSmallLastMonth, config.Default(), 80},
		{"small", Month, seedSmall, config.Default(), 80},
		{"normal", Month, seedNormal, budget(testConfig()), 80},
		{"normal_narrow", Month, seedNormal, budget(testConfig()), 40},
		{"baseline", Month, func(t *testing.T, db *store.DB) { seedNormal(t, db); installedDaysAgo(t, db, 2) }, testConfig(), 80},
		{"rolled_back", Month, seedRolledBack, testConfig(), 80},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := openDB(t)
			tc.seed(t, db)
			r, err := Build(context.Background(), db, Inputs{Config: tc.cfg, Now: now}, mustPeriod(t, tc.period))
			if err != nil {
				t.Fatal(err)
			}
			golden(t, tc.name+".txt", Render(r, Options{Width: tc.width}))
			if tc.width == 80 {
				golden(t, tc.name+".json", toJSON(t, r))
			}
			assertLabeled(t, r)
		})
	}
}

func seedSmallLastMonth(t *testing.T, db *store.DB) {
	t.Helper()
	installedDaysAgo(t, db, 60)
	addReq(t, db, req{ts: now.AddDate(0, -1, 0), session: "old", agent: "claude_code", model: "claude-a", cost: 2.0})
}

func seedRolledBack(t *testing.T, db *store.DB) {
	t.Helper()
	seedNormal(t, db)
	exec(t, db, `INSERT INTO optimizations(name, tier, state, changed_at, reason) VALUES('noise_normalizer','safe','shadow',?, ?)`,
		now.UnixMilli(), "guardrail: repeated tool calls rose 45% against the comparison sessions")
	exec(t, db, `INSERT INTO guardrail_events(ts, optimizer, rule, observed, threshold, sample_on, sample_ref, reference, reason)
		VALUES(?, 'noise_normalizer', 'retries', 45, 30, 22, 25, 'off_sessions', ?)`,
		now.UnixMilli(), "Trove set noise_normalizer back to shadow because repeated tool calls rose 45% against the comparison sessions. Review it with `trove report`.")
}

// Every number in the report comes from the measurement layer unchanged.
func TestReportMatchesMeasurementLayer(t *testing.T) {
	db := openDB(t)
	seedNormal(t, db)
	ctx := context.Background()
	p := mustPeriod(t, Month)
	cfg := testConfig()
	r, err := Build(ctx, db, Inputs{Config: cfg, Now: now}, p)
	if err != nil {
		t.Fatal(err)
	}

	rows, _ := measure.TotalsBy(ctx, db, p.Window(), measure.ByAgent)
	var spend float64
	var reqs int
	for _, x := range rows {
		spend += x.CostUSD.Value
		reqs += x.Requests
	}
	if diff := r.Headline.Spend.Value - spend; diff > 1e-9 || diff < -1e-9 || r.Requests != reqs || reqs != 30 {
		t.Errorf("headline spend %v (%d requests), measure says %v (%d)", r.Headline.Spend.Value, r.Requests, spend, reqs)
	}
	w, _ := measure.WasteTotalIn(ctx, db, p.Window())
	if r.Headline.Waste != w.USD {
		t.Errorf("waste %+v, measure says %+v", r.Headline.Waste, w.USD)
	}
	sv, _ := measure.SavingsByOptimizer(ctx, db, p.Window(), cfg.Measure)
	if len(sv) != 1 || r.Headline.Savings.Value != sv[0].USD.Value || r.Headline.Savings.N != sv[0].USD.N {
		t.Errorf("savings %+v, measure says %+v", r.Headline.Savings, sv)
	}
	if r.Headline.Spend.Label != measure.Estimated || r.Headline.Tokens.Label != measure.Measured {
		t.Errorf("labels: spend %q, tokens %q", r.Headline.Spend.Label, r.Headline.Tokens.Label)
	}
	if len(r.Leaks) < 3 || len(r.Leaks) > maxLeaks {
		t.Errorf("%d leaks; want 3 to %d", len(r.Leaks), maxLeaks)
	}
}

// Every figure in the JSON carries a label of measured or estimated.
func TestEveryFigureIsLabeled(t *testing.T) {
	db := openDB(t)
	seedNormal(t, db)
	cfg := testConfig()
	cfg.Budget.CapUSD = 1000
	r, err := Build(context.Background(), db, Inputs{Config: cfg, Now: now}, mustPeriod(t, Month))
	if err != nil {
		t.Fatal(err)
	}
	if n := assertLabeled(t, r); n < 10 {
		t.Errorf("found only %d figures; the walk is not reaching the report", n)
	}
}

// assertLabeled fails for any {"value": ...} object in v's JSON that lacks a
// measured or estimated label, and returns how many figures it checked.
func assertLabeled(t *testing.T, v any) int {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var tree any
	_ = json.Unmarshal(b, &tree)
	figures := 0
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch x := v.(type) {
		case map[string]any:
			if _, isFigure := x["value"]; isFigure {
				figures++
				if l, _ := x["label"].(string); l != "measured" && l != "estimated" {
					t.Errorf("%s has a number without a label: %v", path, x)
				}
			}
			for k, c := range x {
				walk(path+"."+k, c)
			}
		case []any:
			for i, c := range x {
				walk(fmt.Sprintf("%s[%d]", path, i), c)
			}
		}
	}
	walk("$", tree)
	return figures
}

func TestReportEmptyStateTellsWhatToDo(t *testing.T) {
	db := openDB(t)
	r, err := Build(context.Background(), db, Inputs{Config: config.Default(), Now: now}, mustPeriod(t, Month))
	if err != nil {
		t.Fatal(err)
	}
	out := Render(r, Options{})
	for _, want := range []string{"trove setup", "trove gateway"} {
		if !strings.Contains(out, want) {
			t.Errorf("empty report does not mention %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "$0") {
		t.Errorf("empty report shows zeros:\n%s", out)
	}
}

func TestReportShowsAppliedFixesWithHonestLabels(t *testing.T) {
	r := newReport(mustPeriod(t, Month), false)
	r.State, r.Requests = StateOK, 100
	usd := 0.02
	r.Fixes = []measure.FixEffect{
		{FixID: "ctx-1", Kind: "context_slim", AppliedAt: now, EstTokens: 900, EstUSD: &usd, NBefore: 3, NAfter: 2, Label: measure.Estimated,
			Text: "not enough data yet"},
		{FixID: "mcp-2", Kind: "mcp_prune", AppliedAt: now, EstTokens: 1200, NBefore: 30, NAfter: 25, Label: measure.Measured, Enough: true,
			Reduced: true, Percent: 18, Text: "supports but does not prove"},
	}
	out := Render(r, Options{})
	for _, want := range []string{
		"Config fixes you applied", "ctx-1", "900 tokens saved", "Measured: not enough data yet",
		"Measured: 18% fewer prompt tokens per request",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// With no fixes the section is absent.
	r.Fixes = nil
	if strings.Contains(Render(r, Options{}), "Config fixes") {
		t.Error("the section must not appear when no fix was applied")
	}
}
