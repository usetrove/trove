package experiment

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/store"
)

// drift returns successive session costs (no optimizer effect) around a level that wanders slowly (an AR(1)
// on its log), like a user moving between cheap and expensive work, so a session's cost is predictable from
// the sessions shortly before it.
func drift(r *rand.Rand) func() float64 {
	logLevel := 0.0
	return func() float64 {
		logLevel = 0.97*logLevel + 0.2*r.NormFloat64()
		return math.Exp(logLevel) * (1 + 0.1*r.NormFloat64())
	}
}

// simulateDrift stores n finished sessions with drifting costs (see drift). With the optimizer on, cost is
// multiplied by onFactor.
func simulateDrift(t *testing.T, db *store.DB, optimizer string, n int, onFactor float64, seed int64) {
	t.Helper()
	ctx := context.Background()
	a := NewAssigner(db, testCfg(), func() time.Time { return t0.Add(time.Minute) }, nil)
	next := drift(rand.New(rand.NewSource(seed)))
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("d%03d", i)
		arm := a.Arm(key, optimizer)
		cost, state := next(), config.StateShadow
		if arm == ArmOn {
			cost, state = cost*onFactor, config.StateOn
		}
		sb, _ := json.Marshal(measure.SessionSignals{Turns: 10, HasTools: true, On: []string{}, Acted: []string{}, PinnedAt: t0.UnixMilli()})
		start := t0.Add(time.Duration(i) * time.Hour)
		if _, err := db.ExecContext(ctx, `INSERT INTO sessions(session_key, agent, started_at, ended_at, request_count, total_cost_usd, outcome_signals)
			VALUES(?, 'claude_code', ?, ?, 10, ?, ?)`, key, start.UnixMilli(), start.Add(10*time.Minute).UnixMilli(), cost, string(sb)); err != nil {
			t.Fatal(err)
		}
		pj, _ := json.Marshal(map[string]string{optimizer: state})
		if _, err := db.ExecContext(ctx, `INSERT INTO session_pins(session_key, states, custom_agents, bytes_per_token, pinned_at)
			VALUES(?, ?, '[]', 3.5, ?)`, key, string(pj), start.UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
}

// Done-when for plan item 1.2: replaying the same stored sessions, the covariate-adjusted range is narrower
// than the unadjusted one, and the estimate still lands on the true effect.
func TestPriorCostAdjustmentNarrowsTheRangeOnTheSameSessions(t *testing.T) {
	db := openDB(t)
	simulateDrift(t, db, "opt", 120, 0.85, 21)
	r, err := Analyze(context.Background(), db, "opt", testCfg(), Priors{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Cost == nil {
		t.Fatalf("no cost figure: %+v", r)
	}
	c := *r.Cost
	if !c.Adjusted || c.Covariate != CovariatePriorCost {
		t.Fatalf("want the prior-cost adjustment used on drifting costs: %+v", c)
	}
	adj, raw := c.High-c.Low, c.UnadjustedHigh-c.UnadjustedLow
	t.Logf("adjusted %+.3f [%+.3f, %+.3f] width %.3f; unadjusted %+.3f [%+.3f, %+.3f] width %.3f",
		c.Value, c.Low, c.High, adj, c.UnadjustedValue, c.UnadjustedLow, c.UnadjustedHigh, raw)
	if adj >= raw {
		t.Fatalf("adjusted range %.3f to %.3f (width %.3f) is not narrower than unadjusted %.3f to %.3f (width %.3f)",
			c.Low, c.High, adj, c.UnadjustedLow, c.UnadjustedHigh, raw)
	}
	if !(c.Low < -0.15 && -0.15 < c.High) {
		t.Errorf("adjusted range %.3f to %.3f misses the true -15%%", c.Low, c.High)
	}
	if !(c.UnadjustedLow <= c.UnadjustedValue && c.UnadjustedValue <= c.UnadjustedHigh) {
		t.Errorf("unadjusted figure kept inconsistently: %+v", c)
	}
	// Reproducible: the bootstrap and the fit are seeded and deterministic.
	b, _ := Analyze(context.Background(), db, "opt", testCfg(), Priors{})
	if *b.Cost != c || b.InputsHash != r.InputsHash {
		t.Fatal("not reproducible")
	}
}

func TestNoAdjustmentWhenPriorCostDoesNotPredictCost(t *testing.T) {
	db := openDB(t)
	simulate(t, db, "opt", 80, profile{cost: 1, turns: 10}, profile{cost: 0.8, turns: 10}, 11) // independent costs
	r, err := Analyze(context.Background(), db, "opt", testCfg(), Priors{})
	if err != nil {
		t.Fatal(err)
	}
	c := *r.Cost
	if c.Adjusted || c.Covariate != "" {
		t.Fatalf("adjusted on noise: %+v", c)
	}
	if c.Value != c.UnadjustedValue || c.Low != c.UnadjustedLow || c.High != c.UnadjustedHigh {
		t.Fatalf("unadjusted fields must equal the reported ones when not adjusted: %+v", c)
	}
}

// The covariate of a session comes only from off-arm sessions in earlier randomisation blocks of the same
// stratum, at most priorWindow of them, so it cannot depend on the session's own arm.
func TestPriorCovariateUsesEarlierBlocksOffArmOnly(t *testing.T) {
	mk := func(seq int, arm, stratum string, cost float64) sess {
		return sess{key: fmt.Sprintf("%s%02d", stratum, seq), seq: seq, arm: arm, stratum: stratum, cost: cost}
	}
	all := []sess{
		mk(0, ArmOff, "a", 1), mk(1, ArmOn, "a", 100), mk(2, ArmOff, "a", 3), mk(3, ArmOn, "a", 100),
		mk(4, ArmOn, "a", 100), mk(5, ArmOff, "a", 50), mk(6, ArmOff, "a", 60), mk(7, ArmOn, "a", 100),
		mk(8, ArmOff, "a", 7),
		mk(4, ArmOff, "b", 1000), // another stratum never leaks in
	}
	x, has := priorCovariate(all, func(s sess) float64 { return s.cost }, nil)
	want := map[int]struct {
		v   float64
		has bool
	}{
		0: {0, false}, 1: {0, false}, 2: {0, false}, 3: {0, false}, // block 0 has nothing before it
		4: {2, true}, 5: {2, true}, 6: {2, true}, 7: {2, true}, // block 1 sees block 0's off sessions: 1 and 3
		8: {(1 + 3 + 50 + 60) / 4.0, true}, // block 2 sees both earlier blocks' off sessions, not its own
	}
	for i, s := range all[:9] {
		w := want[s.seq]
		if has[i] != w.has || (w.has && x[i] != w.v) {
			t.Errorf("seq %d: got %v,%v want %v,%v", s.seq, x[i], has[i], w.v, w.has)
		}
	}
	if has[9] {
		t.Errorf("stratum b has no earlier block, got %v", x[9])
	}

	// The window keeps only the last priorWindow off sessions.
	var long []sess
	for i := 0; i < 40; i++ {
		long = append(long, mk(i, BlockArm(1, "o", "", i), "", float64(i)))
	}
	x, has = priorCovariate(long, func(s sess) float64 { return s.cost }, nil)
	last := len(long) - 1
	var offs []float64
	for _, s := range long {
		if s.arm == ArmOff && s.seq/blockSize < long[last].seq/blockSize {
			offs = append(offs, s.cost)
		}
	}
	offs = offs[len(offs)-priorWindow:]
	if !has[last] || x[last] != meanOf(offs) {
		t.Errorf("window: got %v want %v", x[last], meanOf(offs))
	}
}

// With no effect at all, the adjusted 95% range should cover zero about 95% of the time: the adjustment
// narrows the range without breaking it.
func TestAdjustedRangeKeepsItsCoverageUnderNoEffect(t *testing.T) {
	if testing.Short() {
		t.Skip("simulation")
	}
	const reps = 120
	covered, used := 0, 0
	for rep := 0; rep < reps; rep++ {
		r := rand.New(rand.NewSource(int64(1000 + rep)))
		var all []sess
		next := drift(r)
		for i := 0; i < 96; i++ {
			all = append(all, sess{key: fmt.Sprintf("k%03d", i), seq: i, arm: BlockArm(int64(rep), "o", "", i), cost: next()})
		}
		pick := func(s sess) float64 { return s.cost }
		on, off, _, _ := split(all, pick, nil)
		keys, w := strata(values(on), values(off), 10)
		if !cupedUsable(on, off, keys) {
			continue
		}
		used++
		_, lo, hi, ok := bootstrapCUPED(on, off, keys, w, true, int64(rep))
		if ok && lo <= 0 && 0 <= hi {
			covered++
		}
	}
	if used < reps*9/10 {
		t.Fatalf("adjustment used in only %d of %d replicates", used, reps)
	}
	if rate := float64(covered) / float64(used); rate < 0.88 {
		t.Fatalf("coverage %.2f; want about 0.95", rate)
	}
}
