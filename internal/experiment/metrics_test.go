package experiment

import (
	"context"
	"testing"

	"github.com/usetrove/trove/internal/measure"
)

func TestReportsCostPerTurnAndPerUncachedTokenWithRanges(t *testing.T) {
	db := openDB(t)
	simulate(t, db, "opt", 80,
		profile{cost: 1.00, turns: 10, uncached: 100000},
		profile{cost: 0.80, turns: 10, uncached: 60000}, 11)
	r, err := Analyze(context.Background(), db, "opt", testCfg(), Priors{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Cost == nil || r.CostPerTurn == nil || r.CostPerUncached == nil {
		t.Fatalf("want all three cost metrics: %+v", r)
	}
	for _, m := range []*MetricResult{r.Cost, r.CostPerTurn, r.CostPerUncached} {
		if m.Label != measure.Measured || !(m.Low < m.Value && m.Value < m.High) || m.NOn < 20 || m.NOff < 20 {
			t.Errorf("%s: %+v", m.Metric, *m)
		}
	}
	if r.CostPerTurn.Metric != MetricCostPerTurn || r.CostPerUncached.Metric != MetricCostPerUncached {
		t.Errorf("metric names: %q %q", r.CostPerTurn.Metric, r.CostPerUncached.Metric)
	}
	// 0.8 / 1.0 at equal turns is about -20% per turn; 0.8/60k vs 1/100k is about +33% per uncached token.
	if v := r.CostPerTurn.Value; v < -0.3 || v > -0.1 {
		t.Errorf("cost per turn change %.3f; want about -0.2", v)
	}
	if v := r.CostPerUncached.Value; v < 0.2 || v > 0.5 {
		t.Errorf("cost per uncached token change %.3f; want about +0.33", v)
	}
	// Reproducible: the bootstrap is seeded.
	b, _ := Analyze(context.Background(), db, "opt", testCfg(), Priors{})
	if *b.CostPerTurn != *r.CostPerTurn || *b.CostPerUncached != *r.CostPerUncached {
		t.Fatal("not reproducible")
	}
	// Stored as numbers, one row each.
	names := map[string]bool{}
	for _, row := range r.rows() {
		names[row.Metric] = true
	}
	for _, want := range []string{MetricCost, MetricCostPerTurn, MetricCostPerUncached} {
		if !names[want] {
			t.Errorf("rows() has no %s", want)
		}
	}
	// The verdict still comes from cost per session (see docs/plan W1 item 1.1).
	if r.Status != Saves {
		t.Errorf("status %s; want saves", r.Status)
	}
}

func TestSessionsWithNoUncachedTokensAreSkippedNotCountedAsZero(t *testing.T) {
	db := openDB(t)
	simulate(t, db, "opt", 80, profile{cost: 1, turns: 10}, profile{cost: 0.9, turns: 10}, 12)
	r, err := Analyze(context.Background(), db, "opt", testCfg(), Priors{})
	if err != nil {
		t.Fatal(err)
	}
	if r.CostPerUncached != nil {
		t.Fatalf("no session has uncached tokens, so no figure: %+v", *r.CostPerUncached)
	}
	if r.CostPerTurn == nil {
		t.Fatal("cost per turn should still be shown")
	}
}
