package measure

import (
	"context"
	"testing"
	"time"
)

// SpentUSD counts the same requests as TotalsBy: successful ones, in the window, at list prices,
// leaving out any that could not be priced and saying how many.
func TestSpentUSDMatchesTheTotalsTheReportShows(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	add := func(ts time.Time, status int, cost any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `INSERT INTO requests(ts, session_key, agent, provider, model, status_code, cost_usd) VALUES(?,?,?,?,?,?,?)`,
			ts.UnixMilli(), "s", "claude_code", "anthropic", "m", status, cost); err != nil {
			t.Fatal(err)
		}
	}
	from := t0
	to := t0.Add(24 * time.Hour)
	add(from, 200, 1.25)
	add(from.Add(time.Hour), 200, 0.75)
	add(from.Add(2*time.Hour), 200, nil)  // unpriced
	add(from.Add(3*time.Hour), 429, 9.99) // not successful: never counted
	add(from.Add(4*time.Hour), 0, 9.99)   // no response
	add(from.Add(-time.Minute), 200, 5.0) // before the window
	add(to, 200, 5.0)                     // the end is exclusive
	w := Window{From: from, To: to}

	usd, unpriced, err := SpentUSD(ctx, db, w)
	if err != nil {
		t.Fatal(err)
	}
	if usd != 2.0 || unpriced != 1 {
		t.Errorf("SpentUSD = %v, %d unpriced; want 2.00 and 1", usd, unpriced)
	}
	rows, err := TotalsBy(ctx, db, w, ByAgent)
	if err != nil {
		t.Fatal(err)
	}
	var total float64
	for _, r := range rows {
		total += r.CostUSD.Value
	}
	if total != usd {
		t.Errorf("the report's total %v and SpentUSD %v disagree", total, usd)
	}
	if usd, un, err := SpentUSD(ctx, db, Window{From: to.Add(time.Hour), To: to.Add(2 * time.Hour)}); err != nil || usd != 0 || un != 0 {
		t.Errorf("an empty window gave %v, %d, %v", usd, un, err)
	}
}
