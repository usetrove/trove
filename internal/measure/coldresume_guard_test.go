package measure

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
)

// The cold-resume guard is rolled back when users send the stopped prompt again most of the time: stopping it did
// not help them. A clear, or leaving, counts in its favour.
func TestColdResumeOverrideGuardrail(t *testing.T) {
	cases := []struct {
		name               string
		blocked, overrides int
		want               bool
	}{
		{"too few blocks to judge", 19, 19, false},
		{"about half sent again", 40, 22, false},
		{"most sent again", 20, 17, true},
		{"nearly all sent again", 40, 38, true},
		{"most cleared", 30, 5, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := openDB(t)
			ctx := context.Background()
			now := t0.Add(24 * time.Hour)
			ex := func(q string, a ...any) error { _, err := db.ExecContext(ctx, q, a...); return err }
			for i := 0; i < c.blocked; i++ {
				hookEvent(t, ex, now, "c-s", config.OptimizerHookColdResume, "blocked", 0, 200000)
				switch {
				case i < c.overrides:
					hookEvent(t, ex, now, "c-s", config.OptimizerHookColdResume, "overridden", 0, 200000)
				default:
					hookEvent(t, ex, now, "c-s", config.OptimizerHookColdResume, "cleared", 0, 200000)
				}
			}
			ev, err := EvaluateHooks(ctx, db, config.Default().Measure, now)
			if err != nil {
				t.Fatal(err)
			}
			if got := len(ev) == 1; got != c.want {
				t.Fatalf("rolled back = %v, want %v: %+v", got, c.want, ev)
			}
			if !c.want {
				return
			}
			e := ev[0]
			if e.Rule != RuleColdResumeOverride || e.Optimizer != config.OptimizerHookColdResume || e.Threshold != 0.5 {
				t.Errorf("event %+v", e)
			}
			if !strings.Contains(e.Reason, "because you sent the prompt again after") || !strings.Contains(e.Reason, "limit 50%") {
				t.Errorf("reason: %s", e.Reason)
			}
			if over, _ := Overrides(ctx, db); over[config.OptimizerHookColdResume] == "" {
				t.Errorf("not rolled back: %v", over)
			}
		})
	}
}
