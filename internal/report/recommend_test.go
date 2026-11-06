package report

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/recommend"
	"github.com/usetrove/trove/internal/store"
)

func seedRepoSessions(t *testing.T, db *store.DB) {
	t.Helper()
	ctx := context.Background()
	if err := db.Features().PutRepo(ctx, store.RepoFeaturesRow{RepoKey: "repo1", ComputedAt: now, SizeBucket: "L",
		LangMix: "go:90,python:10", TestFramework: "go_test"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if err := db.Features().PutSession(ctx, store.SessionFeaturesRow{SessionKey: "sf" + string(rune('a'+i)), RepoKey: "repo1",
			RecordedAt: now.Add(-time.Duration(i) * time.Hour), FilesRead: 30, RepeatReads: 3, LargeResults: 4, ToolResults: 15, AvgDupRatio: 0.2}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReportShowsRecommenderSuggestionsWithReasonAndConfidence(t *testing.T) {
	db := openDB(t)
	seedNormal(t, db)
	seedRepoSessions(t, db)
	cfg := testConfig()
	cfg.Budget.CapUSD = 1000
	r, err := Build(context.Background(), db, Inputs{Config: cfg, Now: now, RepoKey: "repo1", Task: "quick-fix"}, mustPeriod(t, Month))
	if err != nil {
		t.Fatal(err)
	}
	var recs []Recommendation
	for _, x := range r.Recommendations {
		if x.Source == "recommender" {
			recs = append(recs, x)
		}
	}
	if len(recs) < 4 {
		t.Fatalf("expected the busy repo and the task to produce suggestions, got %d", len(recs))
	}
	for _, x := range recs {
		if x.Detail == "" || x.Title == "" || x.Name == "" || x.Action == "" || x.Evidence != "none" || x.Label != "estimated" ||
			(x.Confidence != "low" && x.Confidence != "medium" && x.Confidence != "high") || x.EstSavingsUSD != nil || x.DefaultsRelease == "" {
			t.Errorf("incomplete recommender suggestion: %+v", x)
		}
	}
	text := Render(r, Options{Width: 80})
	for _, want := range []string{"Try output_trimmer in shadow mode", "Use the quick-fix settings profile", "(confidence: low)",
		"A rule of thumb with no benchmark or measurement behind it yet", "No savings figure."} {
		if !strings.Contains(text, want) {
			t.Errorf("report is missing %q:\n%s", want, text)
		}
	}
	golden(t, "recommender.txt", text)
	golden(t, "recommender.json", toJSON(t, r))
	assertLabeled(t, r)
}

func TestReportWithoutARepoStillBuildsAndSuggestsOnlyFromTheTask(t *testing.T) {
	db := openDB(t)
	seedNormal(t, db)
	seedRepoSessions(t, db)
	r, err := Build(context.Background(), db, Inputs{Config: testConfig(), Now: now}, mustPeriod(t, Month))
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range r.Recommendations {
		if x.Source == "recommender" {
			t.Errorf("no repo and no task should give no recommender suggestions: %+v", x)
		}
	}
}

type failingProvider struct{}

func (failingProvider) Name() string { return "hosted" }
func (failingProvider) Recommend(context.Context, recommend.Facts) ([]recommend.Recommendation, error) {
	return nil, errors.New("offline")
}

func TestAFailingRecommenderProviderNeverFailsTheReport(t *testing.T) {
	db := openDB(t)
	seedNormal(t, db)
	r, err := Build(context.Background(), db, Inputs{Config: testConfig(), Now: now, Task: "feature",
		Providers: []recommend.Provider{failingProvider{}}}, mustPeriod(t, Month))
	if err != nil {
		t.Fatalf("a provider failure must not fail the report: %v", err)
	}
	for _, x := range r.Recommendations {
		if x.Source == "recommender" {
			t.Errorf("a failed provider should contribute nothing: %+v", x)
		}
	}
}

func TestTuningSuggestionTitleCarriesTheValue(t *testing.T) {
	v := 15.0
	x := FromRecommendation(recommend.Recommendation{Kind: recommend.KindTuning, Name: "output_trimmer.keep_tail", Action: recommend.ActionTune,
		Reason: "r", Confidence: recommend.High, Evidence: recommend.EvidenceLocal, Label: "measured", Value: &v}, "seed-1")
	if x.Title != "Set output_trimmer.keep_tail to 15 for this repo" || x.SuggestedValue == nil || x.DefaultsRelease != "" {
		t.Errorf("got %+v", x)
	}
	if got := recommenderBasis(x); !strings.Contains(got, "your own data (measured)") {
		t.Errorf("basis = %q", got)
	}
}
