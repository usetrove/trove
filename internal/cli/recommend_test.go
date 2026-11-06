package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/features"
	"github.com/usetrove/trove/internal/store"
)

func TestRecommendShowsReasonsConfidenceAndNeverChangesAnything(t *testing.T) {
	h := newHarness(t, nil)
	dir := h.app.Getenv("TROVE_DATA_DIR")
	repo := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	salt, _ := db.Features().Salt(ctx)
	key := features.RepoKey(repo, salt)
	now := time.Now()
	if err := db.Features().PutRepo(ctx, store.RepoFeaturesRow{RepoKey: key, ComputedAt: now, SizeBucket: "L", LangMix: "go:100", TestFramework: "go_test"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := db.Features().PutSession(ctx, store.SessionFeaturesRow{SessionKey: string(rune('a' + i)), RepoKey: key, RecordedAt: now,
			FilesRead: 30, RepeatReads: 3, LargeResults: 4, ToolResults: 12}); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()

	if code := h.run("recommend", "--repo", repo, "--task", "quick-fix", "--json"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	var out recommendOut
	if err := json.Unmarshal(h.out.Bytes(), &out); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, h.out)
	}
	if out.Repo != key || out.Defaults.Release == "" || out.Defaults.Version != 1 || len(out.Recommendations) < 5 {
		t.Fatalf("out = %+v", out)
	}
	for _, r := range out.Recommendations {
		if r.Detail == "" || r.Confidence == "" || r.Label == "" || r.Source != "recommender" {
			t.Errorf("incomplete: %+v", r)
		}
	}
	if strings.Contains(h.out.String(), repo) {
		t.Errorf("the repo's folder must not be printed: %s", h.out)
	}

	h.out.Reset()
	if code := h.run("recommend", "--repo", repo); code != 0 {
		t.Fatal(h.err.String())
	}
	for _, want := range []string{"Try output_trimmer in shadow mode", "confidence: low", "Suggestions only; Trove has changed nothing."} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, h.out)
		}
	}
}

func TestRecommendHandlesNoDataAndBadTask(t *testing.T) {
	h := newHarness(t, nil)
	if code := h.run("recommend", "--repo", t.TempDir()); code != 0 || !strings.Contains(h.out.String(), "No suggestions yet") {
		t.Errorf("no data: exit %d %q", code, h.out)
	}
	if h.app.Getenv("TROVE_DATA_DIR") == "" {
		t.Fatal("test needs a data dir")
	}
	h.out.Reset()
	if code := h.run("recommend", "--task", "quick-fix", "--json"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	var out recommendOut
	if err := json.Unmarshal(h.out.Bytes(), &out); err != nil || len(out.Recommendations) != 1 || out.Recommendations[0].Name != "quick-fix" {
		t.Errorf("with no database a named task still gives its profile suggestion: %v %+v", err, out)
	}
	h.err.Reset()
	if code := h.run("recommend", "--task", "poetry"); code == 0 || !strings.Contains(h.err.String(), "quick-fix") {
		t.Errorf("a bad task should list the valid ones: exit %d %q", code, h.err)
	}
}
