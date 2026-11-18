package experiment

import (
	"context"
	"testing"

	"github.com/usetrove/trove/internal/tasks"
)

// Scout and cascade sessions are randomized by the hooks and reported like any other optimizer, from the
// agent's own history (tokens), because they run for subscription users who have no gateway data.
func TestScoutTrialIsReportedFromHistory(t *testing.T) {
	for _, feat := range []string{tasks.FeatureScout, tasks.FeatureCascade} {
		db := openDB(t)
		historySessions(t, db, "h", 120, t0, 0.70)
		if _, err := db.Exec(`UPDATE experiment_assignments SET optimizer = ?`, feat); err != nil {
			t.Fatal(err)
		}
		res, err := Evaluate(context.Background(), db, testCfg(), DefaultPriors())
		if err != nil {
			t.Fatal(err)
		}
		if len(res) != 1 || res[0].Optimizer != feat {
			t.Fatalf("%s: got %+v", feat, res)
		}
		if res[0].Status != Saves || res[0].Headline != MetricTokens {
			t.Errorf("%s: status %q headline %q (%s); the comparison must come from session history", feat, res[0].Status, res[0].Headline, res[0].Reason)
		}
	}
}
