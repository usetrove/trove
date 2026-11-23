package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func f64(v float64) *float64 { return &v }
func i64(v int64) *int64     { return &v }
func bptr(v bool) *bool      { return &v }

func fullSample(key string) LimitSample {
	return LimitSample{
		TS: time.UnixMilli(1_700_000_000_000), SessionKey: key,
		FiveHourPct: f64(23.5), FiveHourResetsAt: i64(1_738_425_600_000),
		SevenDayPct: f64(41.2), SevenDayResetsAt: i64(1_738_857_600_000),
		CacheWarm: bptr(true), CacheTTLSeconds: i64(3600), Model: "claude-opus-5-5", CCVersion: "2.1.290",
	}
}

func limitRows(t *testing.T, db *DB, key string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM limit_samples WHERE session_key = ?`, key).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A sample is written only when one of its values differs from the session's latest row.
func TestInsertLimitSampleIfChanged(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		change func(*LimitSample)
		want   bool
	}{
		{"identical values add nothing", func(*LimitSample) {}, false},
		{"five-hour percentage", func(s *LimitSample) { s.FiveHourPct = f64(24) }, true},
		{"five-hour reset", func(s *LimitSample) { s.FiveHourResetsAt = i64(1_738_443_600_000) }, true},
		{"seven-day percentage", func(s *LimitSample) { s.SevenDayPct = f64(41.3) }, true},
		{"seven-day reset", func(s *LimitSample) { s.SevenDayResetsAt = i64(1_739_000_000_000) }, true},
		{"cache went cold", func(s *LimitSample) { s.CacheWarm = bptr(false) }, true},
		{"cache ttl", func(s *LimitSample) { s.CacheTTLSeconds = i64(300) }, true},
		{"model", func(s *LimitSample) { s.Model = "claude-sonnet-5-5" }, true},
		{"version", func(s *LimitSample) { s.CCVersion = "2.1.291" }, true},
		{"a window disappears", func(s *LimitSample) { s.FiveHourPct, s.FiveHourResetsAt = nil, nil }, true},
		{"cache info disappears", func(s *LimitSample) { s.CacheWarm, s.CacheTTLSeconds = nil, nil }, true},
		{"only the time moves", func(s *LimitSample) { s.TS = s.TS.Add(time.Minute) }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := openTemp(t)
			first := fullSample("c-abc")
			if ok, err := db.InsertLimitSampleIfChanged(ctx, first); err != nil || !ok {
				t.Fatalf("first sample: wrote %v, %v", ok, err)
			}
			next := fullSample("c-abc")
			next.TS = first.TS.Add(time.Second)
			tc.change(&next)
			ok, err := db.InsertLimitSampleIfChanged(ctx, next)
			if err != nil || ok != tc.want {
				t.Fatalf("wrote %v (%v), want %v", ok, err, tc.want)
			}
			want := 1
			if tc.want {
				want = 2
			}
			if got := limitRows(t, db, "c-abc"); got != want {
				t.Errorf("%d rows, want %d", got, want)
			}
			last, found, err := db.LastLimitSample(ctx, "c-abc")
			if err != nil || !found || !last.SameValues(next) {
				t.Errorf("latest sample %+v (%v %v) does not match %+v", last, found, err, next)
			}
		})
	}
}

// Going back to an earlier value is a change too: only the latest row is compared.
func TestLimitSampleComparesWithTheLatestRowOnly(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	a, b := fullSample("c-abc"), fullSample("c-abc")
	b.TS, b.FiveHourPct = a.TS.Add(time.Second), f64(30)
	c := a
	c.TS = a.TS.Add(2 * time.Second)
	for i, s := range []LimitSample{a, b, c} {
		if ok, err := db.InsertLimitSampleIfChanged(ctx, s); err != nil || !ok {
			t.Fatalf("sample %d: wrote %v, %v", i, ok, err)
		}
	}
	if got := limitRows(t, db, "c-abc"); got != 3 {
		t.Errorf("%d rows, want 3", got)
	}
}

func TestLimitSamplesKeepSessionsApart(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	for _, key := range []string{"c-one", "c-two", "c-one", "c-two"} {
		if _, err := db.InsertLimitSampleIfChanged(ctx, fullSample(key)); err != nil {
			t.Fatal(err)
		}
	}
	if limitRows(t, db, "c-one") != 1 || limitRows(t, db, "c-two") != 1 {
		t.Errorf("each session should hold exactly one row")
	}
	if _, found, _ := db.LastLimitSample(ctx, "c-none"); found {
		t.Errorf("a session with no rows has no latest sample")
	}
	if _, err := db.InsertLimitSampleIfChanged(ctx, LimitSample{}); err == nil {
		t.Errorf("a sample without a session key must be refused")
	}
}

// An empty sample (every value absent) round-trips as NULLs, not zeros.
func TestLimitSampleAbsentValuesAreNull(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	s := LimitSample{TS: time.UnixMilli(1), SessionKey: "c-abc"}
	if _, err := db.InsertLimitSampleIfChanged(ctx, s); err != nil {
		t.Fatal(err)
	}
	var nonNull int
	if err := db.QueryRow(`SELECT COUNT(*) FROM limit_samples WHERE five_hour_pct IS NOT NULL OR five_hour_resets_at IS NOT NULL
		OR seven_day_pct IS NOT NULL OR seven_day_resets_at IS NOT NULL OR cache_warm IS NOT NULL OR cache_ttl IS NOT NULL
		OR model IS NOT NULL OR cc_version IS NOT NULL`).Scan(&nonNull); err != nil || nonNull != 0 {
		t.Errorf("absent values must be stored as NULL: %d rows (%v)", nonNull, err)
	}
	last, _, _ := db.LastLimitSample(ctx, "c-abc")
	if !last.SameValues(s) {
		t.Errorf("round trip: %+v", last)
	}
}

func TestSampleWriterNeverCreatesAndNeverWaits(t *testing.T) {
	ctx := context.Background()
	if _, err := OpenSampleWriter(filepath.Join(t.TempDir(), "none")); !os.IsNotExist(err) {
		t.Errorf("a missing database must not be created: %v", err)
	}

	dir := filepath.Join(t.TempDir(), "data")
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	w, err := OpenSampleWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	if ok, err := w.InsertLimitSampleIfChanged(ctx, fullSample("c-abc")); err != nil || !ok {
		t.Fatalf("unlocked write: %v %v", ok, err)
	}

	// Another process holds the write lock: the writer fails at once instead of waiting.
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = conn.ExecContext(ctx, `ROLLBACK`) }()
	next := fullSample("c-abc")
	next.FiveHourPct = f64(50)
	start := time.Now()
	_, err = w.InsertLimitSampleIfChanged(ctx, next)
	if err == nil {
		t.Fatal("a write while the database is locked should fail")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "lock") && !strings.Contains(strings.ToLower(err.Error()), "busy") {
		t.Errorf("want a busy/locked error, got %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("the writer waited %v for the lock; it must not wait", d)
	}
}
