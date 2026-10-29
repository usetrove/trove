package store

import (
	"context"
	"testing"
	"time"
)

func TestUsageDailyFollowsRequests(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 29, 12, 0, 0, 0, time.Local)
	cost, diff := 0.50, 0.10
	rows := []Request{
		{TS: at, SessionKey: "s", StatusCode: 200, CostUSD: &cost},
		{TS: at.Add(time.Minute), SessionKey: "s", StatusCode: 200}, // unpriced
		{TS: at.Add(2 * time.Minute), SessionKey: "s", StatusCode: 200, CostUSD: &cost,
			TokensRemoved: 100, CostDiffUSD: &diff, OptimizationsApplied: []byte(`["x"]`)},
		{TS: at.Add(3 * time.Minute), SessionKey: "s", StatusCode: 200, CostUSD: &cost,
			TokensRemoved: 100, CostDiffUSD: &diff}, // shadow only: not a saving
		{TS: at.Add(4 * time.Minute), SessionKey: "s", StatusCode: 500, CostUSD: &cost}, // failed: not counted
	}
	if err := db.insertRequests(ctx, rows); err != nil {
		t.Fatal(err)
	}
	var n, unpriced int
	var spend, saved float64
	err := db.QueryRow(`SELECT requests, unpriced, spend_usd, saved_usd FROM usage_daily WHERE day = ?`, DayKey(at)).
		Scan(&n, &unpriced, &spend, &saved)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 || unpriced != 1 || spend != 1.5 || saved != 0.1 {
		t.Errorf("got requests %d unpriced %d spend %v saved %v", n, unpriced, spend, saved)
	}
}
