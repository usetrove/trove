package measure

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
)

// hook_read_limit is judged by how often a limited read is followed by a read of more of the file. A follow-up
// is what the limit's note asks for, so the limit is looser (twice the shared rate, 50 percent by default).
// The check is sequential, so 20 actions must be well above the limit: 16 of 20 is, 15 is not yet.
func TestReadLimitGuardrailFiresAtItsOwnLimit(t *testing.T) {
	for _, c := range []struct {
		extra    int
		rollback bool
	}{{15, false}, {16, true}} {
		t.Run(fmt.Sprintf("%d of 20", c.extra), func(t *testing.T) {
			db := openDB(t)
			ctx := context.Background()
			now := t0.Add(24 * time.Hour)
			ex := func(q string, a ...any) error { _, err := db.ExecContext(ctx, q, a...); return err }
			for i := 0; i < 20; i++ {
				hookEvent(t, ex, now, "c-s", config.OptimizerHookReadLimit, "rewritten", 5000, 1200)
			}
			for i := 0; i < c.extra; i++ {
				hookEvent(t, ex, now, "c-s", config.OptimizerHookReadLimit, "extra_read", 0, 0)
			}
			ev, err := EvaluateHooks(ctx, db, config.Default().Measure, now)
			if err != nil {
				t.Fatal(err)
			}
			if c.rollback != (len(ev) == 1) {
				t.Fatalf("rollback = %v, want %v: %+v", len(ev) == 1, c.rollback, ev)
			}
			if c.rollback && (ev[0].Rule != RuleHookExtraRead || ev[0].Threshold != 0.5 || ev[0].SampleOn != 20) {
				t.Fatalf("%+v", ev[0])
			}
		})
	}
}

// The output trimmer hides text, so it is judged more strictly than the shared rate: half of it.
// 8 of 20 trips it; at the shared 25 percent limit 20 actions would need 11.
func TestTrimmerGuardrailIsStricterThanTheSharedRate(t *testing.T) {
	for _, c := range []struct {
		back     int
		rollback bool
	}{{7, false}, {8, true}} { // 35 and 40 percent of 20, against a 12 percent limit, judged sequentially
		t.Run(fmt.Sprintf("%d of 20", c.back), func(t *testing.T) {
			db := openDB(t)
			ctx := context.Background()
			now := t0.Add(24 * time.Hour)
			ex := func(q string, a ...any) error { _, err := db.ExecContext(ctx, q, a...); return err }
			for i := 0; i < 20; i++ {
				hookEvent(t, ex, now, "c-s", config.OptimizerOutputTrimmer, "rewritten", 5000, 1500)
			}
			for i := 0; i < c.back; i++ {
				hookEvent(t, ex, now, "c-s", config.OptimizerOutputTrimmer, "fetched_back", 0, 0)
			}
			ev, err := EvaluateHooks(ctx, db, config.Default().Measure, now)
			if err != nil {
				t.Fatal(err)
			}
			if c.rollback != (len(ev) == 1) {
				t.Fatalf("rollback = %v, want %v: %+v", len(ev) == 1, c.rollback, ev)
			}
			if c.rollback && ev[0].Threshold != 0.12 {
				t.Fatalf("threshold = %v", ev[0].Threshold)
			}
		})
	}
}

func TestUnusedAdviceGuardrail(t *testing.T) {
	type tc struct {
		name       string
		sessions   int
		adopted    int
		observable bool
		prefix     string
		rollback   bool
	}
	for _, c := range []tc{
		{"3 of 20 followed is above the floor", 20, 3, true, "c-", false},
		// Against a 90 percent limit, 20 sessions with none followed happen by chance 12% of the time at the floor.
		{"none of 20 followed is not yet beyond chance", 20, 0, true, "c-", false},
		{"1 of 60 followed is not yet beyond chance", 60, 1, true, "c-", false},
		{"none of 60 followed", 60, 0, true, "c-", true},
		{"too few sessions to judge", 19, 0, true, "c-", false},
		{"adoption cannot be observed, so nothing is judged", 60, 0, false, "c-", false},
		{"Codex sessions are not judged at all", 60, 0, true, "x-", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			db := openDB(t)
			ctx := context.Background()
			now := t0.Add(24 * time.Hour)
			ex := func(q string, a ...any) error { _, err := db.ExecContext(ctx, q, a...); return err }
			for i := 0; i < c.sessions; i++ {
				s := fmt.Sprintf("%ss%d", c.prefix, i)
				// Advised many times in one session still counts as one session.
				for j := 0; j < 3; j++ {
					hookEvent(t, ex, now, s, config.OptimizerHookLargeOutput, "advised", 0, 0)
				}
				if i < c.adopted {
					hookEvent(t, ex, now, s, config.OptimizerHookLargeOutput, "adopted", 0, 0)
				}
			}
			// Use of trove_run in a session that was never advised says nothing about the hint.
			hookEvent(t, ex, now, "c-other", config.OptimizerHookLargeOutput, "adopted", 0, 0)
			ev, err := EvaluateHooksWith(ctx, db, config.Default().Measure, now, HookGuardOptions{AdoptionObservable: c.observable})
			if err != nil {
				t.Fatal(err)
			}
			if c.rollback != (len(ev) == 1) {
				t.Fatalf("rollback = %v, want %v: %+v", len(ev) == 1, c.rollback, ev)
			}
			if c.rollback {
				e := ev[0]
				if e.Rule != RuleHookUnusedAdvice || e.Optimizer != config.OptimizerHookLargeOutput || e.SampleOn != c.sessions {
					t.Fatalf("%+v", e)
				}
			}
		})
	}
	t.Run("plain EvaluateHooks never judges adoption", func(t *testing.T) {
		db := openDB(t)
		ctx := context.Background()
		now := t0.Add(24 * time.Hour)
		ex := func(q string, a ...any) error { _, err := db.ExecContext(ctx, q, a...); return err }
		for i := 0; i < 30; i++ {
			hookEvent(t, ex, now, fmt.Sprintf("c-s%d", i), config.OptimizerHookLargeOutput, "advised", 0, 0)
		}
		if ev, err := EvaluateHooks(ctx, db, config.Default().Measure, now); err != nil || len(ev) != 0 {
			t.Fatalf("%+v %v", ev, err)
		}
	})
}

func TestHookTotalsCountsTheNewKinds(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	now := t0.Add(24 * time.Hour)
	ex := func(q string, a ...any) error { _, err := db.ExecContext(ctx, q, a...); return err }
	for i := 0; i < 4; i++ {
		hookEvent(t, ex, now, "c-s", config.OptimizerHookReadLimit, "extra_read", 0, 0)
	}
	hookEvent(t, ex, now, "c-s", config.OptimizerHookLargeOutput, "adopted", 0, 0)
	stats, err := HookTotals(ctx, db, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]HookStats{}
	for _, s := range stats {
		got[s.Hook] = s
	}
	if got[config.OptimizerHookReadLimit].ExtraRead != 4 || got[config.OptimizerHookLargeOutput].Adopted != 1 {
		t.Fatalf("%+v", got)
	}
}
