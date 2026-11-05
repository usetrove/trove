package recommend

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/store"
)

func TestGatherFromTheLocalDatabase(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	now := time.Now()
	if err := db.Features().PutRepo(ctx, store.RepoFeaturesRow{RepoKey: "r", ComputedAt: now, SizeBucket: "L",
		LangMix: "go:80,python:20", TestFramework: "go_test"}); err != nil {
		t.Fatal(err)
	}
	for i, large := range []int{4, 6, 5, 5} {
		if err := db.Features().PutSession(ctx, store.SessionFeaturesRow{SessionKey: string(rune('a' + i)), RepoKey: "r",
			RecordedAt: now.Add(time.Duration(i) * time.Minute), LargeResults: large, RepeatReads: 3, ToolResults: 12}); err != nil {
			t.Fatal(err)
		}
	}
	f, err := Gather(ctx, db, config.Default(), "r", "feature", now)
	if err != nil {
		t.Fatal(err)
	}
	if f.Repo == nil || f.Repo.Size != "L" || f.Repo.TestFramework != "go_test" || len(f.Repo.LangMix) != 2 {
		t.Errorf("repo = %+v", f.Repo)
	}
	if f.Sessions.Sessions != 4 || f.Sessions.LargeResults != 5 || f.Sessions.RepeatReads != 3 {
		t.Errorf("sessions = %+v", f.Sessions)
	}
	recs, _ := NewLocal().Recommend(ctx, f)
	var got []string
	for _, r := range recs {
		got = append(got, r.Name)
	}
	if len(got) < 3 {
		t.Errorf("a busy repo should produce suggestions, got %v", got)
	}

	// No repo known: only the task profile applies.
	f, err = Gather(ctx, db, config.Default(), "", "quick-fix", now)
	if err != nil || f.Repo != nil || f.Sessions.Sessions != 0 {
		t.Errorf("no repo: %+v err=%v", f, err)
	}
	// A tampered row falls back to unknown instead of flowing through.
	r := repoFromRow(store.RepoFeaturesRow{SizeBucket: "L", TestFramework: "<script>", LangMix: "go:x,,python:5"})
	if r.TestFramework != "unknown" || len(r.LangMix) != 1 {
		t.Errorf("tampered row = %+v", r)
	}
}
