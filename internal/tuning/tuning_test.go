package tuning

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/store"
)

func openDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// seed records n trimmed outputs of which bad were fetched back, in one session
// of the repo, at time at.
func seed(t *testing.T, db *store.DB, repo, session string, at time.Time, n, bad int) {
	t.Helper()
	ctx := context.Background()
	if err := db.Features().PutSession(ctx, store.SessionFeaturesRow{SessionKey: session, RepoKey: repo, RecordedAt: at}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		kind := "rewritten"
		if _, err := db.ExecContext(ctx, `INSERT INTO hook_events (ts, session_key, hook, kind, bytes, tokens) VALUES (?,?,?,?,0,0)`,
			at.UnixMilli(), session, config.OptimizerOutputTrimmer, kind); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < bad; i++ {
		if _, err := db.ExecContext(ctx, `INSERT INTO hook_events (ts, session_key, hook, kind, bytes, tokens) VALUES (?,?,?,?,0,0)`,
			at.UnixMilli(), session, config.OptimizerOutputTrimmer, "fetched_back"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRatesAreScopedToTheRepoAndWindow(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	now := time.Now()
	seed(t, db, "r1", "s1", now.Add(-time.Hour), 10, 3)
	seed(t, db, "r2", "s2", now.Add(-time.Hour), 50, 50)
	seed(t, db, "r1", "s3", now.Add(-200*24*time.Hour), 99, 99)
	acted, bad, err := Rates(ctx, db, "r1", config.OptimizerOutputTrimmer, now.Add(-Window), now)
	if err != nil || acted != 10 || bad != 3 {
		t.Errorf("r1: acted=%d bad=%d err=%v; want 10 and 3 (other repos and old events excluded)", acted, bad, err)
	}
	if a, b, _ := Rates(ctx, db, "nobody", config.OptimizerOutputTrimmer, now.Add(-Window), now); a != 0 || b != 0 {
		t.Errorf("unknown repo: %d %d", a, b)
	}
}

func row(repo, param string, v int, state string, at time.Time, before float64) store.TuningRow {
	return store.TuningRow{RepoKey: repo, Optimizer: config.OptimizerOutputTrimmer, Param: param, Value: v, State: state,
		TunedAt: at, Source: "local", BeforeRate: before, NBefore: 40}
}

func TestSuggestApplyAndAppliedClamp(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	now := time.Now()
	tu := db.Tuning()
	if err := tu.Suggest(ctx, []store.TuningRow{row("r1", optimize.ParamKeepTail, 15, "", now, 0.3), row("r1", optimize.ParamMinBytes, 1<<30, "", now, 0.3),
		row("r1", "no_such_param", 5, "", now, 0)}); err != nil {
		t.Fatal(err)
	}
	if got, _ := Applied(ctx, db); len(got) != 0 {
		t.Fatalf("a suggestion must not be in effect: %v", got)
	}
	n, err := tu.Apply(ctx, "r1", now)
	if err != nil || n != 3 {
		t.Fatalf("apply: n=%d err=%v", n, err)
	}
	got, _ := Applied(ctx, db)
	want := optimize.Tuning{
		optimize.TuningKey(config.OptimizerOutputTrimmer, optimize.ParamKeepTail): 15,
		optimize.TuningKey(config.OptimizerOutputTrimmer, optimize.ParamMinBytes): 50000, // clamped
	}
	if fmt.Sprint(got["r1"]) != fmt.Sprint(want) {
		t.Errorf("applied = %v, want %v (out-of-range clamped, unknown parameter dropped)", got["r1"], want)
	}
	// A new suggestion never replaces a value in effect.
	if err := tu.Suggest(ctx, []store.TuningRow{row("r1", optimize.ParamKeepTail, 30, "", now, 0.5)}); err != nil {
		t.Fatal(err)
	}
	if got, _ := Applied(ctx, db); got["r1"][optimize.TuningKey(config.OptimizerOutputTrimmer, optimize.ParamKeepTail)] != 15 {
		t.Error("a suggestion replaced an applied value")
	}
	if err := tu.Reset(ctx, "r1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := Applied(ctx, db); len(got) != 0 {
		t.Errorf("reset must clear it: %v", got)
	}
}

func TestReviewRevertsTuningThatDidNotHelp(t *testing.T) {
	m := config.Default().Measure
	now := time.Now()
	applied := now.Add(-48 * time.Hour)
	tests := []struct {
		name       string
		before     float64
		n, bad     int
		wantRevert bool
	}{
		{"improved", 0.40, 20, 2, false},
		{"unchanged", 0.20, 20, 4, true},
		{"worse", 0.20, 20, 8, true},
		{"too few outputs to judge", 0.20, m.MinSessions - 1, m.MinSessions - 1, false},
		{"improved but still over the guardrail limit", 0.90, 20, 20, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := openDB(t)
			ctx := context.Background()
			if err := db.Tuning().Suggest(ctx, []store.TuningRow{row("r", optimize.ParamKeepTail, 15, "", applied, tc.before)}); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Tuning().Apply(ctx, "r", applied); err != nil {
				t.Fatal(err)
			}
			seed(t, db, "r", "s", now.Add(-time.Hour), tc.n, tc.bad)
			rev, err := Review(ctx, db, m, now)
			if err != nil {
				t.Fatal(err)
			}
			if (len(rev) == 1) != tc.wantRevert {
				t.Fatalf("reverted = %+v, want revert=%v", rev, tc.wantRevert)
			}
			got, _ := Applied(ctx, db)
			if tc.wantRevert && len(got) != 0 {
				t.Errorf("a reverted tuning must no longer be in effect: %v", got)
			}
			if !tc.wantRevert && len(got) == 0 {
				t.Error("a tuning that helped must stay")
			}
			if again, _ := Review(ctx, db, m, now); len(again) != 0 {
				t.Errorf("review must be safe to repeat: %+v", again)
			}
		})
	}
}
