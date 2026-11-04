package measure

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
)

func TestNetCostRollsBackOnlyWhenToolCostsMore(t *testing.T) {
	m := config.Default().Measure
	run := func(t *testing.T, onCost, offCost float64) (*NetCost, []Event) {
		db := openDB(t)
		ctx := context.Background()
		add := func(i int, on bool, cost float64) {
			key := fmt.Sprintf("s%d", i)
			pinned := t0.Add(time.Duration(i) * time.Hour)
			state := "off"
			if on {
				state = "on"
			}
			if _, err := db.ExecContext(ctx, `INSERT INTO sessions(session_key, agent, started_at, request_count, total_cost_usd) VALUES(?, 'a', ?, 10, ?)`,
				key, pinned.UnixMilli(), cost); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, `INSERT INTO session_pins(session_key, states, bytes_per_token, pinned_at) VALUES(?,?,3.5,?)`,
				key, `{"ext_rules":"`+state+`"}`, pinned.UnixMilli()); err != nil {
				t.Fatal(err)
			}
		}
		for i := 0; i < 25; i++ {
			add(i, true, onCost+float64(i%5)*0.01)
		}
		for i := 100; i < 125; i++ {
			add(i, false, offCost+float64(i%5)*0.01)
		}
		c, err := NetCostFor(ctx, db, "ext_rules", m)
		if err != nil {
			t.Fatal(err)
		}
		ev, err := EvaluateNetCost(ctx, db, m, t0.Add(1000*time.Hour), []string{"ext_rules"})
		if err != nil {
			t.Fatal(err)
		}
		// A second pass must not record the same rollback again.
		again, _ := EvaluateNetCost(ctx, db, m, t0.Add(1001*time.Hour), []string{"ext_rules"})
		if len(again) != 0 {
			t.Errorf("second evaluation re-recorded: %v", again)
		}
		return c, ev
	}
	t.Run("costlier is rolled back", func(t *testing.T) {
		c, ev := run(t, 1.50, 1.00)
		if c == nil || !c.Costlier || c.Label != Measured || len(ev) != 1 || ev[0].Rule != RuleNetCost {
			t.Fatalf("c=%+v ev=%+v", c, ev)
		}
	})
	t.Run("cheaper is kept", func(t *testing.T) {
		c, ev := run(t, 0.80, 1.00)
		if c == nil || c.Costlier || len(ev) != 0 {
			t.Fatalf("c=%+v ev=%+v", c, ev)
		}
	})
	t.Run("same is kept", func(t *testing.T) {
		if _, ev := run(t, 1.00, 1.00); len(ev) != 0 {
			t.Fatalf("ev=%+v", ev)
		}
	})
}
