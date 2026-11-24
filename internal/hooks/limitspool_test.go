package hooks

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/store"
)

// Session end moves the status line's plan-usage samples into limit_samples.
func TestMaintainIngestsTheStatusLineSpool(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".trove")
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	pct := 12.5
	if err := store.AppendLimitSpool(dir, store.LimitSample{TS: time.Now(), SessionKey: "c-abc", FiveHourPct: &pct}); err != nil {
		t.Fatal(err)
	}
	if err := Maintain(context.Background(), db, dir, config.Default(), func(string) string { return "" }, time.Now()); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM limit_samples WHERE session_key = 'c-abc' AND five_hour_pct = 12.5`).Scan(&n); err != nil || n != 1 {
		t.Errorf("want the spooled sample in limit_samples, found %d (%v)", n, err)
	}
}
