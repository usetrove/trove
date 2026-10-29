package measure

import (
	"context"
	"testing"
	"time"
)

func TestWasteTotalCountsOnlyTheLargestFindingPerRequest(t *testing.T) {
	db := openDB(t)
	w := Window{From: t0.Add(-time.Hour), To: t0.Add(time.Hour)}
	// Two overlapping findings: the total must not be their sum.
	addRequest(t, db, "s", t0, "findings", `[{"detector":"duplicate_tool_result","tokens":100,"usd":0.4},{"detector":"noisy_output","tokens":300,"usd":0.1}]`)
	// An unpriced finding.
	addRequest(t, db, "s", t0.Add(time.Minute), "findings", `[{"detector":"cache_miss","tokens":50,"usd":null}]`)
	// Nothing found.
	addRequest(t, db, "s", t0.Add(2*time.Minute), "findings", `[]`)

	got, err := WasteTotalIn(context.Background(), db, w)
	if err != nil {
		t.Fatal(err)
	}
	if got.USD.Value != 0.4 || got.USD.Label != Estimated || got.USD.N != 2 || got.USD.Unpriced != 1 {
		t.Errorf("usd = %+v", got.USD)
	}
	if got.Tokens.Value != 350 || got.Tokens.Label != Estimated {
		t.Errorf("tokens = %+v", got.Tokens)
	}
}
