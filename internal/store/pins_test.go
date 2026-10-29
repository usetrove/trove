package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/optimize"
)

func TestPinsRoundTripAndFirstWins(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	pins := db.Pins()

	if _, ok, err := pins.Load(ctx, "s1"); err != nil || ok {
		t.Fatalf("empty store: ok=%v err=%v", ok, err)
	}
	first := optimize.Pin{
		SessionKey: "s1", States: map[string]string{"noise_normalizer": "on", "cache_breakpoints": "off"},
		CustomAgents: []string{"sdk"}, BytesPerToken: 3.5, PinnedAt: time.UnixMilli(1_700_000_000_000).UTC(),
	}
	if err := pins.Save(ctx, first); err != nil {
		t.Fatal(err)
	}
	changed := first
	changed.States = map[string]string{"noise_normalizer": "off"}
	if err := pins.Save(ctx, changed); err != nil {
		t.Fatal(err)
	}
	got, ok, err := pins.Load(ctx, "s1")
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	if !reflect.DeepEqual(got, first) {
		t.Errorf("pin changed after the session started:\n got %+v\nwant %+v", got, first)
	}
}

func TestOptimizerColumnsRoundTrip(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	diff := 0.0125
	if err := db.insertRequests(ctx, []Request{{
		TS: time.Now(), SessionKey: "s", Agent: "a", TokensRemoved: 420, CostDiffUSD: &diff,
		OptimizationsApplied: []byte(`{"label":"estimated"}`),
	}, {TS: time.Now(), SessionKey: "s", Agent: "a"}}); err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueryContext(ctx, `SELECT tokens_removed, cost_diff_usd FROM requests ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var removed []int64
	var diffs []*float64
	for rows.Next() {
		var n int64
		var d *float64
		if err := rows.Scan(&n, &d); err != nil {
			t.Fatal(err)
		}
		removed, diffs = append(removed, n), append(diffs, d)
	}
	if removed[0] != 420 || diffs[0] == nil || *diffs[0] != diff || removed[1] != 0 || diffs[1] != nil {
		t.Errorf("got %v %v", removed, diffs)
	}
}

func TestPurgeRemovesOldPins(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	for k, at := range map[string]time.Time{"old": now.AddDate(0, 0, -100), "new": now.AddDate(0, 0, -1)} {
		if err := db.Pins().Save(ctx, optimize.Pin{SessionKey: k, States: map[string]string{}, BytesPerToken: 3.5, PinnedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Purge(ctx, 90, now); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := db.Pins().Load(ctx, "old"); ok {
		t.Error("old pin should be purged")
	}
	if _, ok, _ := db.Pins().Load(ctx, "new"); !ok {
		t.Error("recent pin should stay")
	}
}
