package experiment

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/store"
)

var t0 = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

func openDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func testCfg() config.Experiment {
	c := config.Default().Experiment
	c.MinSessionsPerArm = 20
	return c
}

// profile describes a simulated optimizer's effect on a session.
type profile struct {
	cost, turns, errs float64 // mean per session when the arm is on / off
	abandonOn         float64 // probability of an abandoned session with it on
}

// simulate stores n finished sessions for optimizer, assigning arms with the
// real Assigner, and drawing cost and signals from the profile with seeded
// noise. offP is the off-arm profile and onP the on-arm profile. It returns the
// sessions' arms by key.
func simulate(t *testing.T, db *store.DB, optimizer string, n int, offP, onP profile, seed int64) map[string]string {
	t.Helper()
	ctx := context.Background()
	a := NewAssigner(db, testCfg(), func() time.Time { return t0.Add(time.Minute) }, nil)
	r := rand.New(rand.NewSource(seed))
	arms := map[string]string{}
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("s%03d", i)
		arm := a.Arm(key, optimizer)
		arms[key] = arm
		p, state := offP, config.StateShadow
		if arm == ArmOn {
			p, state = onP, config.StateOn
		}
		cost := p.cost * (1 + 0.1*r.NormFloat64())
		turns := int(p.turns + 2*r.NormFloat64())
		errs := int(p.errs + r.NormFloat64())
		if errs < 0 {
			errs = 0
		}
		sig := measure.SessionSignals{Turns: turns, ToolErrors: errs, HasTools: true, Abandoned: arm == ArmOn && r.Float64() < p.abandonOn,
			On: []string{}, Acted: []string{}, PinnedAt: t0.UnixMilli()}
		sb, _ := json.Marshal(sig)
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
	return arms
}
