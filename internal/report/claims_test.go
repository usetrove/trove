package report

import (
	"context"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/experiment"
)

// The claims table puts a third-party tool and Trove's own optimizer side by side, keeps the one with no effect,
// labels every figure and links the method on every row.
func TestClaimsTableGolden(t *testing.T) {
	db := openDB(t)
	seedNormal(t, db)
	seedHandoff(t, db, "ext_rtk", 80, 0.6, 4)
	seedHandoff(t, db, config.OptimizerHookRepeatRead, 80, 1.0, 0)
	in := Inputs{Config: config.Default(), Now: now, LimitFit: weekFit}
	r, err := Build(context.Background(), db, in, mustPeriod(t, Month))
	if err != nil {
		t.Fatal(err)
	}
	c, err := BuildClaims(context.Background(), db, in, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Rows) != 2 {
		t.Fatalf("want 2 rows, got %+v", c.Rows)
	}
	byName := map[string]ClaimRow{}
	for _, row := range c.Rows {
		byName[row.Name] = row
	}
	rtk, rr := byName["rtk"], byName[config.OptimizerHookRepeatRead]
	if rtk.Maker != "third-party" || rtk.Claim.Label != "claimed" || rtk.Status != experiment.Saves || !strings.Contains(rtk.WeeklyLimit, "lower") {
		t.Errorf("rtk row: %+v", rtk)
	}
	if rr.Maker != "trove" || rr.Status != experiment.NoClearEffect || !strings.Contains(rr.Usage, "no clear effect") {
		t.Errorf("a Trove optimizer with no effect must be kept and say so: %+v", rr)
	}
	md := RenderClaimsMarkdown(c)
	if n := strings.Count(md, "](https://github.com/usetrove/trove/blob/main/docs/benchmark-methodology.md#11-the-claims-table)"); n < 3 {
		t.Errorf("the method is not linked from every row (%d links):\n%s", n, md)
	}
	golden(t, "claims.md", md)
	golden(t, "claims.txt", RenderClaimsText(c, 100))
	golden(t, "claims.json", toRoundedJSON(t, c))
}

func TestClaimsTableEmpty(t *testing.T) {
	db := openDB(t)
	in := Inputs{Config: config.Default(), Now: now}
	c, err := BuildClaims(context.Background(), db, in, Report{})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Rows) != 0 || !strings.Contains(RenderClaimsMarkdown(c), "No randomized session test has run yet.") {
		t.Errorf("%+v", c)
	}
}

// A gateway trial has no plan limit and no test runs; the cells say so rather than show a number.
func TestClaimsCellsForAGatewayTrial(t *testing.T) {
	m := experiment.MetricResult{Metric: experiment.MetricCost, Kind: "relative", Value: -0.1, Low: -0.2, High: 0.02}
	row := ClaimRow{Trial: TrialFigures{Optimizer: "noise_normalizer", Cost: &experiment.Result{Status: experiment.NoClearEffect, NOn: 30, NOff: 31, Cost: &m}, FirstAt: now.UnixMilli(), LastAt: now.UnixMilli()}}
	fillCells(&row)
	if !strings.HasPrefix(row.WeeklyLimit, "not applicable") || !strings.Contains(row.Usage, "API list prices") ||
		!strings.Contains(row.Usage, "no clear effect") || !strings.HasPrefix(row.PassRate, "not compared") || row.NOn != 30 {
		t.Errorf("%+v", row)
	}
	if dateRange(row) != "2026-10-29" {
		t.Errorf("date %q", dateRange(row))
	}
}

func TestMdCellKeepsOneCell(t *testing.T) {
	if got := mdCell("a|b\nc"); got != `a\|b c` {
		t.Errorf("%q", got)
	}
}
