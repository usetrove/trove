package report

import (
	"context"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/regress"
)

func regressResult() *regress.Result {
	a := regress.Alert{Check: regress.Check{Kind: regress.KindVersion, Key: "2.1.89", Metric: regress.MetricThinkTokens,
		Status: regress.StatusAlert, Value: 1712.4, Baseline: 1003.2, MADs: 9.1, Change: 0.7069, N: 300, Sessions: 8, Blocks: 20,
		Label: regress.LabelMeasured}, Cause: regress.CauseClient, Version: "2.1.89", Previous: "2.1.88"}
	a.Sentence, a.Advice = regress.Sentence(a), regress.Advice(a)
	return &regress.Result{ComputedAt: now.UnixMilli(), Alerts: []regress.Alert{a}, Checks: []regress.Check{a.Check,
		{Kind: regress.KindDay, Key: "2026-10-29", Metric: regress.MetricThinkShare, Status: regress.StatusTooLittleData, N: 90, Sessions: 4, Label: regress.LabelMeasured}}}
}

func TestRegressionSectionGolden(t *testing.T) {
	db := openDB(t)
	seedLedgers(t, db)
	r, err := Build(context.Background(), db, Inputs{Config: config.Default(), Now: now, Regressions: regressResult()}, mustPeriod(t, Month))
	if err != nil {
		t.Fatal(err)
	}
	if r.Regressions == nil || r.Regressions.Versions != 1 || r.Regressions.Days != 1 || r.Regressions.TooLittle != 1 {
		t.Fatalf("section = %+v", r.Regressions)
	}
	out := Render(r, Options{Width: 80})
	for _, want := range []string{"Regression watch", "Claude Code 2.1.89: thinking per request up 71%", "DISABLE_AUTOUPDATER", "never pins a version"} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q", want)
		}
	}
	golden(t, "regress.txt", out)
	golden(t, "regress.json", toJSON(t, r))
}

func TestRegressionSectionQuietAndAbsent(t *testing.T) {
	db := openDB(t)
	seedLedgers(t, db)
	quiet := regressResult()
	quiet.Alerts = nil
	r, err := Build(context.Background(), db, Inputs{Config: config.Default(), Now: now, Regressions: quiet}, mustPeriod(t, Month))
	if err != nil {
		t.Fatal(err)
	}
	if out := Render(r, Options{Width: 200}); !strings.Contains(out, "No regression found in 1 version and 1 day") {
		t.Errorf("quiet section:\n%s", out)
	}
	r, err = Build(context.Background(), db, Inputs{Config: config.Default(), Now: now}, mustPeriod(t, Month))
	if err != nil {
		t.Fatal(err)
	}
	if r.Regressions != nil || strings.Contains(Render(r, Options{Width: 80}), "Regression watch") {
		t.Error("section shown without a result")
	}
}
