package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func spoolPath(dir string) string { return filepath.Join(dir, LimitSpoolDir, limitSpoolName) }

// Spooling a sample marks it as the session's latest values; only that exact set of values is marked.
func TestLimitSpoolMarksTheLatestValues(t *testing.T) {
	dir := t.TempDir()
	a, b, c := fullSample("c-one"), fullSample("c-two"), fullSample("c-one")
	c.TS, c.FiveHourPct = a.TS.Add(time.Second), f64(30)
	if LimitSampleMarked(dir, a) {
		t.Fatal("nothing is marked yet")
	}
	for _, s := range []LimitSample{a, b, c} {
		if err := AppendLimitSpool(dir, s); err != nil {
			t.Fatal(err)
		}
	}
	later := c
	later.TS = c.TS.Add(time.Hour) // the time is not part of the values
	if !LimitSampleMarked(dir, later) || !LimitSampleMarked(dir, b) {
		t.Errorf("the latest values of each session should be marked")
	}
	if LimitSampleMarked(dir, a) {
		t.Errorf("a session's earlier values must no longer be marked, so going back to them is a change")
	}
	noCache := c
	noCache.CacheWarm = nil
	if LimitSampleMarked(dir, noCache) {
		t.Errorf("an absent value differs from a present one")
	}
	entries, _ := os.ReadDir(filepath.Join(dir, LimitSpoolDir, limitSeenDir))
	if len(entries) != 2 {
		t.Errorf("want one marker per session, got %d", len(entries))
	}
	if fi, err := os.Stat(spoolPath(dir)); err != nil || (fi.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/') {
		t.Errorf("the spool must be private (0600): %v %v", fi, err)
	}
	if err := MarkLimitSample(dir, fullSample("../escape")); err == nil {
		t.Errorf("a session key that is not a hash must be refused")
	}
}

func TestIngestSweepsOldMarkers(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	old, fresh := fullSample("c-old"), fullSample("c-new")
	for _, s := range []LimitSample{old, fresh} {
		if err := MarkLimitSample(dir, s); err != nil {
			t.Fatal(err)
		}
	}
	week := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(limitMarker(dir, old), week, week); err != nil {
		t.Fatal(err)
	}
	if _, err := db.IngestLimitSpool(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if LimitSampleMarked(dir, old) || !LimitSampleMarked(dir, fresh) {
		t.Errorf("only markers unchanged for a week should be swept")
	}
}

// The status line never creates the data folder; it only adds its spool folder inside an existing one.
func TestLimitSpoolNeedsTheDataFolder(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "none")
	if err := AppendLimitSpool(missing, fullSample("c-one")); !os.IsNotExist(err) {
		t.Errorf("want a not-exist error, got %v", err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Errorf("the data folder was created")
	}
}

func TestLimitSpoolStopsAtItsCap(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, LimitSpoolDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spoolPath(dir), make([]byte, MaxLimitSpool), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AppendLimitSpool(dir, fullSample("c-one")); !errors.Is(err, ErrLimitSpoolFull) {
		t.Errorf("want ErrLimitSpoolFull, got %v", err)
	}
}

func TestIngestLimitSpool(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "data")
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	base := fullSample("c-one")
	at := func(sec int, pct float64) LimitSample {
		s := base
		s.TS, s.FiveHourPct = base.TS.Add(time.Duration(sec)*time.Second), f64(pct)
		return s
	}
	// A crash left a claimed spool behind; it is older than the live one.
	if err := AppendLimitSpool(dir, at(0, 10)); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(spoolPath(dir), filepath.Join(dir, LimitSpoolDir, "limits.ing.0000000000000000001")); err != nil {
		t.Fatal(err)
	}
	for _, s := range []LimitSample{at(1, 10), at(2, 20), at(3, 20), at(4, 30), fullSample("c-two")} {
		if err := AppendLimitSpool(dir, s); err != nil {
			t.Fatal(err)
		}
	}
	// A torn last line, as a crash mid-write could leave, and a line with free text where a model id goes.
	f, _ := os.OpenFile(spoolPath(dir), os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(`{"t":1700000099000,"s":"c-three","fp":5,"m":"my secret prompt"}` + "\n" + `{"t":17000`)
	_ = f.Close()

	n, err := db.IngestLimitSpool(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	// c-one: 10 (claimed), 10 again (same: skipped), 20, 20 again (skipped), 30. c-two: one. c-three: one.
	if n != 5 || limitRows(t, db, "c-one") != 3 || limitRows(t, db, "c-two") != 1 || limitRows(t, db, "c-three") != 1 {
		t.Errorf("ingested %d; rows c-one %d, c-two %d, c-three %d", n, limitRows(t, db, "c-one"), limitRows(t, db, "c-two"), limitRows(t, db, "c-three"))
	}
	var model any
	if err := db.QueryRow(`SELECT model FROM limit_samples WHERE session_key = 'c-three'`).Scan(&model); err != nil || model != nil {
		t.Errorf("free text in the model field must be dropped, got %v (%v)", model, err)
	}
	left, _ := filepath.Glob(filepath.Join(dir, LimitSpoolDir, "limits.*"))
	if len(left) != 0 {
		t.Errorf("the spool should be gone after ingest: %v", left)
	}

	// Ingesting the same lines again (two ingests raced for one claimed file) stores nothing new.
	for _, s := range []LimitSample{at(1, 10), at(4, 30), at(2, 20)} {
		if err := AppendLimitSpool(dir, s); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := db.IngestLimitSpool(ctx, dir); err != nil || n != 0 || limitRows(t, db, "c-one") != 3 {
		t.Errorf("re-ingest wrote %d (%v); c-one has %d rows", n, err, limitRows(t, db, "c-one"))
	}
	if n, err := db.IngestLimitSpool(ctx, dir); err != nil || n != 0 {
		t.Errorf("nothing to ingest: %d %v", n, err)
	}
}

// The spool holds the table's numbers and nothing else (rule 7).
func TestLimitSpoolLineIsMetadataOnly(t *testing.T) {
	dir := t.TempDir()
	if err := AppendLimitSpool(dir, fullSample("c-one")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(spoolPath(dir))
	want := `{"t":1700000000000,"s":"c-one","fp":23.5,"fr":1738425600000,"sp":41.2,"sr":1738857600000,"w":true,"ttl":3600,"m":"claude-opus-5-5","v":"2.1.290"}`
	if strings.TrimSpace(string(b)) != want {
		t.Errorf("spool line:\n got %s\nwant %s", b, want)
	}
}
