package experiment

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/measure"
)

func TestBlockArmBalancedAndDeterministic(t *testing.T) {
	on := 0
	for seq := 0; seq < 40; seq++ {
		a := BlockArm(7, "opt", "", seq)
		if a != BlockArm(7, "opt", "", seq) {
			t.Fatal("same inputs gave different arms")
		}
		if a == ArmOn {
			on++
		}
		// Balanced at every block boundary.
		if (seq+1)%blockSize == 0 && on != (seq+1)/2 {
			t.Fatalf("after %d sessions, %d are on; want %d", seq+1, on, (seq+1)/2)
		}
	}
	// Different strata or seeds draw different orders somewhere.
	diff := false
	for seq := 0; seq < 40; seq++ {
		if BlockArm(7, "opt", "", seq) != BlockArm(8, "opt", "", seq) || BlockArm(7, "opt", "a", seq) != BlockArm(7, "opt", "b", seq) {
			diff = true
		}
	}
	if !diff {
		t.Fatal("seed and stratum have no effect on assignment")
	}
}

func TestAssignerIsStickyAndStratified(t *testing.T) {
	db := openDB(t)
	strat := func(k string) string {
		if strings.HasPrefix(k, "a") {
			return "repo:a"
		}
		return "repo:b"
	}
	as := NewAssigner(db, testCfg(), nil, strat)
	first := map[string]string{}
	for i := 0; i < 8; i++ {
		for _, p := range []string{"a", "b"} {
			k := p + string(rune('0'+i))
			first[k] = as.Arm(k, "opt")
		}
	}
	for k, v := range first {
		if as.Arm(k, "opt") != v {
			t.Fatalf("session %s changed arm after assignment", k)
		}
	}
	for _, p := range []string{"a", "b"} {
		on := 0
		for k, v := range first {
			if strings.HasPrefix(k, p) && v == ArmOn {
				on++
			}
		}
		if on != 4 {
			t.Fatalf("stratum %s has %d of 8 sessions on; want 4", p, on)
		}
	}
}

func TestNotEnoughDataShowsNoNumber(t *testing.T) {
	db := openDB(t)
	simulate(t, db, "opt", 20, profile{cost: 1, turns: 10}, profile{cost: 0.8, turns: 10}, 1)
	r, err := Analyze(context.Background(), db, "opt", testCfg(), Priors{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != NotEnoughData || r.Cost != nil || !strings.Contains(r.Reason, measure.TextNotEnough) {
		t.Fatalf("with 10 per arm: %+v", r)
	}
}

// The acceptance case: an optimizer that shrinks tool output but makes the agent
// take extra turns. Its shadow estimate would look good; the bill says otherwise.
func TestExtraTurnsOptimizerShowsMeasuredCostIncreaseAndIsRolledBack(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	simulate(t, db, "shrinker", 80,
		profile{cost: 1.00, turns: 10, errs: 1},
		profile{cost: 1.25, turns: 16, errs: 1}, 42)

	r, err := Analyze(ctx, db, "shrinker", testCfg(), Priors{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != CostsMore {
		t.Fatalf("status %s (%s); want costs_more", r.Status, r.Reason)
	}
	c := r.Cost
	if c.Label != measure.Measured || c.Low <= 0 || c.Value < 0.15 || c.Value > 0.35 || c.NOn < 20 || c.NOff < 20 {
		t.Fatalf("cost figure %+v", *c)
	}

	results, events, err := Step(ctx, db, testCfg(), Priors{}, t0.Add(100*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Rule != RuleExperimentCost || results[0].Status != RolledBack {
		t.Fatalf("events %+v results %+v", events, results)
	}
	if !strings.Contains(events[0].Reason, "shadow mode cannot see") {
		t.Fatalf("reason must say why shadow missed it: %s", events[0].Reason)
	}
	rolled, err := measure.Overrides(ctx, db)
	if err != nil || rolled["shrinker"] == "" {
		t.Fatalf("overrides %v err %v", rolled, err)
	}
	// The gate now lowers it to shadow for every new session.
	g := measure.NewGate(db, config.Default().Measure, func() time.Time { return t0.Add(1000 * time.Hour) })
	if got := g.State("new-session", "shrinker", config.StateOn, time.Time{}); got != config.StateShadow {
		t.Fatalf("gate state after rollback: %s", got)
	}
	// A second pass does not roll back again, and still reports it rolled back.
	_, events, err = Step(ctx, db, testCfg(), Priors{}, t0.Add(101*time.Hour))
	if err != nil || len(events) != 0 {
		t.Fatalf("second pass: %v %v", events, err)
	}
	all, err := Evaluate(ctx, db, testCfg(), Priors{})
	if err != nil || all[0].Status != RolledBack {
		t.Fatalf("evaluate after rollback: %+v %v", all, err)
	}
}

func TestRealSavingsAreMeasuredAndKept(t *testing.T) {
	db := openDB(t)
	simulate(t, db, "good", 80, profile{cost: 1.00, turns: 10, errs: 1}, profile{cost: 0.80, turns: 10, errs: 1}, 5)
	_, events, err := Step(context.Background(), db, testCfg(), Priors{}, t0.Add(100*time.Hour))
	if err != nil || len(events) != 0 {
		t.Fatalf("events %v err %v", events, err)
	}
	r, _ := Analyze(context.Background(), db, "good", testCfg(), Priors{})
	if r.Status != Saves || r.Cost.High >= 0 || r.Cost.Value > -0.1 || r.Cost.Value < -0.3 {
		t.Fatalf("%+v %+v", r, r.Cost)
	}
	if r.Suggest {
		t.Fatal("must not suggest turning on without benchmark evidence")
	}
}

func TestWorseOutcomesRollBackEvenWhenCostFalls(t *testing.T) {
	db := openDB(t)
	simulate(t, db, "risky", 120, profile{cost: 1.00, turns: 10}, profile{cost: 0.90, turns: 10, abandonOn: 0.45}, 9)
	results, events, err := Step(context.Background(), db, testCfg(), Priors{}, t0.Add(200*time.Hour))
	if err != nil || len(events) != 1 || events[0].Rule != RuleExperimentOutcome {
		t.Fatalf("events %+v err %v results %+v", events, err, results)
	}
}

func TestResultsReproduceFromStoredMetadata(t *testing.T) {
	db := openDB(t)
	simulate(t, db, "opt", 60, profile{cost: 1, turns: 10, errs: 1}, profile{cost: 0.95, turns: 10, errs: 1}, 3)
	a, _ := Analyze(context.Background(), db, "opt", testCfg(), Priors{})
	b, _ := Analyze(context.Background(), db, "opt", testCfg(), Priors{})
	if a.InputsHash != b.InputsHash || *a.Cost != *b.Cost {
		t.Fatalf("not reproducible:\n%+v\n%+v", *a.Cost, *b.Cost)
	}
	// A different seed changes assignment/bootstrap inputs, so the hash differs.
	x := testCfg()
	x.Seed++
	c, _ := Analyze(context.Background(), db, "opt", x, Priors{})
	if c.InputsHash == a.InputsHash {
		t.Fatal("inputs hash ignores the seed")
	}
	// Storing twice with identical inputs adds no duplicate rows.
	for i := 0; i < 2; i++ {
		if err := db.Experiments().Put(context.Background(), t0, a.rows()); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM experiment_results`).Scan(&n)
	if n != len(a.rows()) {
		t.Fatalf("%d stored rows; want %d", n, len(a.rows()))
	}
}

func TestSessionsHeldAtShadowAreExcluded(t *testing.T) {
	db := openDB(t)
	arms := simulate(t, db, "opt", 40, profile{cost: 1, turns: 10}, profile{cost: 1, turns: 10}, 1)
	// A session assigned "on" that the baseline held at shadow was never treated.
	for k, a := range arms {
		if a == ArmOn {
			if _, err := db.Exec(`UPDATE session_pins SET states = '{"opt":"shadow"}' WHERE session_key = ?`, k); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	r, _ := Analyze(context.Background(), db, "opt", testCfg(), Priors{})
	if r.Excluded != 1 || r.NOn+r.NOff != 39 {
		t.Fatalf("excluded %d, kept %d", r.Excluded, r.NOn+r.NOff)
	}
}

func TestClearedRollbackIgnoresOlderSessions(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	simulate(t, db, "shrinker", 80, profile{cost: 1, turns: 10}, profile{cost: 1.3, turns: 16}, 42)
	if _, _, err := Step(ctx, db, testCfg(), Priors{}, t0.Add(100*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := measure.ClearOverride(ctx, db, "shrinker", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	_, events, err := Step(ctx, db, testCfg(), Priors{}, time.Now())
	if err != nil || len(events) != 0 {
		t.Fatalf("old sessions re-triggered a rollback: %v %v", events, err)
	}
}

func TestPriorShrinksNoisyEstimateAndGatesPromotion(t *testing.T) {
	p := Prior{CostChange: -0.10, CostCILow: -0.14, CostCIHigh: -0.06, SuccessCILow: -0.02, Pairs: 40, Source: "bench"}
	if ok, _ := p.Supports(); !ok {
		t.Fatal("prior should clear the ship rule")
	}
	noisy := combine(0.20, -0.30, 0.70, p) // wide data: should land near the prior
	sharp := combine(0.20, 0.18, 0.22, p)  // narrow data: should stay near the data
	if noisy.Value > 0 || sharp.Value < 0.15 {
		t.Fatalf("noisy %+v sharp %+v", noisy, sharp)
	}
	for _, bad := range []Prior{
		{CostCIHigh: -0.06, SuccessCILow: -0.02, Pairs: 10},
		{CostCIHigh: -0.02, SuccessCILow: -0.02, Pairs: 40},
		{CostCIHigh: -0.06, SuccessCILow: -0.09, Pairs: 40},
	} {
		if ok, why := bad.Supports(); ok || why == "" {
			t.Fatalf("%+v should not support promotion", bad)
		}
	}
	pr, err := LoadPriors([]byte("version = 1\n[optimizers.x]\ncost_change=-0.1\ncost_ci_low=-0.14\ncost_ci_high=-0.06\npairs=40\n"))
	if err != nil || pr.Optimizers["x"].Pairs != 40 {
		t.Fatalf("%+v %v", pr, err)
	}
	if len(DefaultPriors().Optimizers) != 0 {
		t.Fatal("the shipped priors file must stay empty until real benchmark results exist")
	}
}

func TestHarnessSavings(t *testing.T) {
	for _, c := range []struct{ size, trimmed, cap, want int64 }{
		{50000, 2000, 0, 48000},    // agent sends everything: full saving
		{50000, 2000, 10000, 8000}, // agent already cut it to 10k: only 8k was ever sent
		{50000, 12000, 10000, 0},   // trimmed is still over the cap: nothing saved
		{3000, 2000, 10000, 1000},  // under the cap, truncation never applied
		{1000, 5000, 0, 0},         // never negative
	} {
		if got := HarnessSavings(c.size, c.trimmed, c.cap); got != c.want {
			t.Errorf("HarnessSavings(%d,%d,%d) = %d; want %d", c.size, c.trimmed, c.cap, got, c.want)
		}
	}
}
