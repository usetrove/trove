package measure

import (
	"context"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
)

// The hook optimizers that rewrite are judged by what the agent does next: a
// quieted command run again as first written, and a trimmed output fetched back.
func TestHookGuardrailsForRewritingOptimizers(t *testing.T) {
	cases := []struct {
		name      string
		optimizer string
		rule      string
		acted     string
		bad       string
	}{
		{"quieted commands run again", config.OptimizerHookQuietCommands, RuleHookRerun, "rewritten", "reissued"},
		{"trimmed output fetched back", config.OptimizerOutputTrimmer, RuleHookFetchBack, "rewritten", "fetched_back"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := openDB(t)
			ctx := context.Background()
			now := t0.Add(24 * time.Hour)
			ex := func(q string, a ...any) error { _, err := db.ExecContext(ctx, q, a...); return err }
			m := config.Default().Measure // MinSessions 20, RefetchRatePercent 25

			// 19 actions with a terrible rate: below the minimum sample, so nothing happens.
			for i := 0; i < 19; i++ {
				hookEvent(t, ex, now, "c-s1", c.optimizer, c.acted, 100, 30)
			}
			for i := 0; i < 10; i++ {
				hookEvent(t, ex, now, "c-s1", c.optimizer, c.bad, 0, 0)
			}
			if ev, err := EvaluateHooks(ctx, db, m, now); err != nil || len(ev) != 0 {
				t.Fatalf("19 actions is too few to judge: %+v %v", ev, err)
			}

			// The 20th action reaches the sample: 10/20 is over the 25% limit.
			hookEvent(t, ex, now, "c-s2", c.optimizer, c.acted, 100, 30)
			ev, err := EvaluateHooks(ctx, db, m, now)
			if err != nil || len(ev) != 1 || ev[0].Rule != c.rule || ev[0].Optimizer != c.optimizer || ev[0].SampleOn != 20 {
				t.Fatalf("expected one rollback by %s: %+v %v", c.rule, ev, err)
			}
			if over, _ := Overrides(ctx, db); over[c.optimizer] == "" {
				t.Fatalf("not rolled back: %v", over)
			}
			if again, err := EvaluateHooks(ctx, db, m, now); err != nil || len(again) != 0 {
				t.Fatalf("rolled back twice: %+v %v", again, err)
			}

			// Once the user lifts it, the old counts are not held against it.
			if err := ClearOverride(ctx, db, c.optimizer, now.Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			if ev, err := EvaluateHooks(ctx, db, m, now.Add(2*time.Minute)); err != nil || len(ev) != 0 {
				t.Fatalf("old events tripped it again: %+v %v", ev, err)
			}
		})
	}
}

func TestHookGuardrailsAreIndependentPerOptimizer(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	now := t0.Add(24 * time.Hour)
	ex := func(q string, a ...any) error { _, err := db.ExecContext(ctx, q, a...); return err }
	// The trimmer is healthy; the quiet-command hook is not. Only the second may roll back.
	for i := 0; i < 40; i++ {
		hookEvent(t, ex, now, "c-s", config.OptimizerOutputTrimmer, "rewritten", 5000, 1500)
		hookEvent(t, ex, now, "c-s", config.OptimizerHookQuietCommands, "rewritten", 0, 0)
	}
	for i := 0; i < 20; i++ {
		hookEvent(t, ex, now, "c-s", config.OptimizerHookQuietCommands, "reissued", 0, 0)
	}
	hookEvent(t, ex, now, "c-s", config.OptimizerOutputTrimmer, "fetched_back", 0, 0)
	ev, err := EvaluateHooks(ctx, db, config.Default().Measure, now)
	if err != nil || len(ev) != 1 || ev[0].Optimizer != config.OptimizerHookQuietCommands {
		t.Fatalf("%+v %v", ev, err)
	}

	stats, err := HookTotals(ctx, db, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]HookStats{}
	for _, s := range stats {
		by[s.Hook] = s
	}
	q, tr := by[config.OptimizerHookQuietCommands], by[config.OptimizerOutputTrimmer]
	if q.Rewritten != 40 || q.Reissued != 20 || tr.Rewritten != 40 || tr.FetchedBack != 1 {
		t.Fatalf("quiet %+v trimmer %+v", q, tr)
	}
	if tr.TokensRewritten == nil || tr.TokensRewritten.Label != Estimated || tr.TokensRewritten.Value != 40*1500 {
		t.Fatalf("rewrite savings must be labelled estimated: %+v", tr.TokensRewritten)
	}
}
