package recommend

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/store"
)

func tuneDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func seedTrims(t *testing.T, db *store.DB, repo string, at time.Time, n, bad int) {
	t.Helper()
	ctx := context.Background()
	if err := db.Features().PutSession(ctx, store.SessionFeaturesRow{SessionKey: "s-" + repo, RepoKey: repo, RecordedAt: at}); err != nil {
		t.Fatal(err)
	}
	add := func(kind string) {
		if _, err := db.ExecContext(ctx, `INSERT INTO hook_events (ts, session_key, hook, kind, bytes, tokens) VALUES (?,?,?,?,0,0)`,
			at.UnixMilli(), "s-"+repo, config.OptimizerOutputTrimmer, kind); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < n; i++ {
		add("rewritten")
	}
	for i := 0; i < bad; i++ {
		add("fetched_back")
	}
}

func trimmerOn() config.Config {
	cfg := config.Default()
	cfg.Optimizers[config.OptimizerOutputTrimmer] = config.StateOn
	return cfg
}

func TestProposeTuningLoosensTheTrimmerWhereAgentsFetchBack(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	cfg := trimmerOn()
	half := cfg.Measure.RefetchRatePercent / 2

	t.Run("fetch-back share at half the guardrail limit proposes loosening", func(t *testing.T) {
		db := tuneDB(t)
		n := 100
		seedTrims(t, db, "r", now.Add(-time.Hour), n, n*half/100+1)
		got, err := ProposeTuning(ctx, db, cfg, "r", now)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 3 {
			t.Fatalf("want 3 proposals, got %d: %+v", len(got), got)
		}
		want := map[string]int{"min_bytes": 9000, "keep_head": 7, "keep_tail": 15}
		for _, p := range got {
			if want[p.Row.Param] != p.Row.Value || p.Rec.Kind != KindTuning || p.Rec.Action != ActionTune ||
				p.Rec.Label != "measured" || p.Rec.Evidence != EvidenceLocal || p.Rec.Confidence != High || p.Rec.Value == nil ||
				int(*p.Rec.Value) != p.Row.Value || p.Row.State != store.TuningSuggested || p.Row.NBefore != n || p.Row.BeforeRate <= 0 {
				t.Errorf("bad proposal %+v", p)
			}
			if p.Rec.Name != optimize.TuningKey(config.OptimizerOutputTrimmer, p.Row.Param) {
				t.Errorf("name %q", p.Rec.Name)
			}
		}
	})
	t.Run("a low share proposes nothing", func(t *testing.T) {
		db := tuneDB(t)
		seedTrims(t, db, "r", now.Add(-time.Hour), 100, 1)
		if got, _ := ProposeTuning(ctx, db, cfg, "r", now); len(got) != 0 {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("too few trimmed outputs to judge", func(t *testing.T) {
		db := tuneDB(t)
		n := cfg.Measure.MinSessions - 1
		seedTrims(t, db, "r", now.Add(-time.Hour), n, n)
		if got, _ := ProposeTuning(ctx, db, cfg, "r", now); len(got) != 0 {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("nothing unless the trimmer is on", func(t *testing.T) {
		db := tuneDB(t)
		seedTrims(t, db, "r", now.Add(-time.Hour), 100, 60)
		off := config.Default()
		if got, _ := ProposeTuning(ctx, db, off, "r", now); len(got) != 0 {
			t.Errorf("trimmer off: %+v", got)
		}
		if got, _ := ProposeTuning(ctx, db, cfg, "", now); len(got) != 0 {
			t.Errorf("no repo: %+v", got)
		}
	})
	t.Run("not while applied, and not again after a revert", func(t *testing.T) {
		db := tuneDB(t)
		seedTrims(t, db, "r", now.Add(-time.Hour), 100, 60)
		props, _ := ProposeTuning(ctx, db, cfg, "r", now)
		var rows []store.TuningRow
		for _, p := range props {
			rows = append(rows, p.Row)
		}
		if err := db.Tuning().Suggest(ctx, rows); err != nil {
			t.Fatal(err)
		}
		if again, _ := ProposeTuning(ctx, db, cfg, "r", now); len(again) != len(props) {
			t.Errorf("a pending suggestion may be re-proposed (same values): %d vs %d", len(again), len(props))
		}
		if _, err := db.Tuning().Apply(ctx, "r", now); err != nil {
			t.Fatal(err)
		}
		if got, _ := ProposeTuning(ctx, db, cfg, "r", now); len(got) != 0 {
			t.Errorf("applied: %+v", got)
		}
		if err := db.Tuning().Revert(ctx, "r", config.OptimizerOutputTrimmer); err != nil {
			t.Fatal(err)
		}
		if got, _ := ProposeTuning(ctx, db, cfg, "r", now); len(got) != 0 {
			t.Errorf("reverted: %+v", got)
		}
	})
}

func TestTuningProposalsReachTheRecommender(t *testing.T) {
	db := tuneDB(t)
	now := time.Now()
	seedTrims(t, db, "r", now.Add(-time.Hour), 100, 60)
	f, err := Gather(context.Background(), db, trimmerOn(), "r", "", now)
	if err != nil {
		t.Fatal(err)
	}
	recs, _ := NewLocal().Recommend(context.Background(), f)
	n := 0
	for _, r := range recs {
		if r.Kind == KindTuning {
			n++
		}
	}
	if n != 3 {
		t.Errorf("want 3 tuning recommendations in the list, got %d: %v", n, names(recs))
	}
}
