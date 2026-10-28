package store

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

func openTemp(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestMigrateTwice(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	cur, err := db.CurrentVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	latest, _ := LatestVersion()
	if cur != latest || latest < 1 {
		t.Errorf("current %d, latest %d", cur, latest)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil || n != latest {
		t.Errorf("schema_migrations has %d rows (err %v), want %d", n, err, latest)
	}
}

func TestReopenKeepsData(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "d")
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO settings(key, value) VALUES('a','b')`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	db, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v string
	if err := db.QueryRow(`SELECT value FROM settings WHERE key='a'`).Scan(&v); err != nil || v != "b" {
		t.Errorf("got %q, %v", v, err)
	}
}

func TestNewerDatabaseRefused(t *testing.T) {
	db := openTemp(t)
	if _, err := db.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES(9999, 0)`); err != nil {
		t.Fatal(err)
	}
	err := db.Migrate(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Upgrade Trove") {
		t.Errorf("got %v", err)
	}
}

func TestFreshInstallPermissionsAndWAL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fresh")
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Errorf("journal_mode = %q, %v", mode, err)
	}
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on Windows")
	}
	// Force WAL/SHM files to exist, then check everything.
	if _, err := db.Exec(`INSERT INTO settings(key,value) VALUES('x','y')`); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("data dir mode %o, want 700", fi.Mode().Perm())
	}
	for _, f := range []string{DBFileName, DBFileName + "-wal", DBFileName + "-shm"} {
		fi, err := os.Stat(filepath.Join(dir, f))
		if err != nil {
			continue
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %o, want 600", f, fi.Mode().Perm())
		}
	}
	if p := PermissionProblems(dir); len(p) != 0 {
		t.Errorf("unexpected problems: %v", p)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if p := PermissionProblems(dir); len(p) != 1 || !strings.Contains(p[0], "chmod 0700") {
		t.Errorf("want one chmod hint, got %v", p)
	}
}

// TestSchemaIsMetadataOnly pins the exact columns so any new column is a
// deliberate, reviewed change, and blocks names that suggest content storage.
func TestSchemaIsMetadataOnly(t *testing.T) {
	db := openTemp(t)
	want := map[string][]string{
		"requests": {"id", "ts", "session_key", "agent", "agent_version", "provider", "model", "endpoint", "streamed",
			"status_code", "latency_ms", "ttft_ms", "input_uncached_tokens", "cache_write_tokens", "cache_read_tokens",
			"output_tokens", "reasoning_tokens", "cost_usd", "pricing_version", "segment_token_estimates", "findings",
			"optimizations_applied", "shadow_savings", "error_class"},
		"sessions": {"session_key", "agent", "started_at", "ended_at", "request_count", "total_input_uncached_tokens",
			"total_cache_write_tokens", "total_cache_read_tokens", "total_output_tokens", "total_cost_usd", "outcome_signals"},
		"optimizations": {"name", "tier", "state", "changed_at", "reason"},
		"measurements":  {"id", "optimizer", "window", "metric", "value", "label", "sample_size"},
		"budgets":       {"id", "period", "cap_usd", "starts_at"},
		"settings":      {"key", "value"},
	}
	banned := []string{"prompt", "content", "body", "path", "message", "text", "code", "output_text", "tool"}
	for table, cols := range want {
		rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for rows.Next() {
			var n string
			_ = rows.Scan(&n)
			got = append(got, n)
		}
		_ = rows.Close()
		sort.Strings(got)
		w := append([]string(nil), cols...)
		sort.Strings(w)
		if !reflect.DeepEqual(got, w) {
			t.Errorf("%s columns changed:\n got %v\nwant %v", table, got, w)
		}
		for _, c := range got {
			for _, b := range banned {
				if c == b {
					t.Errorf("%s.%s looks like content storage", table, c)
				}
			}
		}
	}
}

func TestPurge(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	old, recent := now.AddDate(0, 0, -100), now.AddDate(0, 0, -10)
	if err := db.insertRequests(ctx, []Request{
		{TS: old, SessionKey: "old", Agent: "a"},
		{TS: recent, SessionKey: "new", Agent: "a"},
	}); err != nil {
		t.Fatal(err)
	}
	n, err := db.Purge(ctx, 90, now)
	if err != nil || n != 1 {
		t.Fatalf("purged %d, %v", n, err)
	}
	var reqs, sess int
	_ = db.QueryRow(`SELECT COUNT(*) FROM requests`).Scan(&reqs)
	_ = db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sess)
	if reqs != 1 || sess != 1 {
		t.Errorf("requests %d sessions %d, want 1 and 1", reqs, sess)
	}
	if _, err := db.Purge(ctx, 0, now); err == nil {
		t.Error("days=0 must be rejected")
	}
}

func TestWriterStoresRowsAndSessionTotals(t *testing.T) {
	db := openTemp(t)
	w := NewWriter(db, WriterOptions{FlushInterval: 10 * time.Millisecond})
	ttft, cost := int64(120), 0.5
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	w.Enqueue(Request{TS: base, SessionKey: "s1", Agent: "claude-code", Provider: "anthropic", Model: "m", Streamed: true,
		TTFTMS: &ttft, CostUSD: &cost, InputUncachedTokens: 10, OutputTokens: 5, Findings: []byte(`{"a":1}`)})
	w.Enqueue(Request{TS: base.Add(time.Minute), SessionKey: "s1", Agent: "claude-code", InputUncachedTokens: 20, OutputTokens: 1})
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := w.Stats(); s.Written != 2 || s.Dropped != 0 || s.Failed != 0 {
		t.Fatalf("stats %+v", s)
	}
	var count, in, out int64
	var started, ended int64
	if err := db.QueryRow(`SELECT request_count, total_input_uncached_tokens, total_output_tokens, started_at, ended_at FROM sessions WHERE session_key='s1'`).
		Scan(&count, &in, &out, &started, &ended); err != nil {
		t.Fatal(err)
	}
	if count != 2 || in != 30 || out != 6 || started != base.UnixMilli() || ended != base.Add(time.Minute).UnixMilli() {
		t.Errorf("session %d %d %d %d %d", count, in, out, started, ended)
	}
	var nullCost, nullTTFT int
	_ = db.QueryRow(`SELECT COUNT(*) FROM requests WHERE cost_usd IS NULL`).Scan(&nullCost)
	_ = db.QueryRow(`SELECT COUNT(*) FROM requests WHERE ttft_ms IS NULL`).Scan(&nullTTFT)
	if nullCost != 1 || nullTTFT != 1 {
		t.Errorf("unknown cost/ttft must be NULL, got %d and %d", nullCost, nullTTFT)
	}
}

func TestWriterDropsWhenFullAndNeverBlocks(t *testing.T) {
	db := openTemp(t)
	hold := make(chan struct{})
	w := newWriter(db, WriterOptions{BufferSize: 2, BatchSize: 1}, hold)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			w.Enqueue(Request{TS: time.Now(), SessionKey: "s"})
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Enqueue blocked on a full buffer")
	}
	// The loop is parked, so at most BufferSize rows were accepted.
	if d := w.Stats().Dropped; d != 48 {
		t.Errorf("dropped %d, want 48", d)
	}
	close(hold)
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := w.Stats(); s.Written != 2 {
		t.Errorf("written %d, want 2", s.Written)
	}
}

func TestEnqueueAfterCloseDropsWithoutPanic(t *testing.T) {
	db := openTemp(t)
	w := NewWriter(db, WriterOptions{})
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if w.Enqueue(Request{}) {
		t.Error("closed writer accepted a row")
	}
	if w.Stats().Dropped != 1 {
		t.Error("drop not counted")
	}
	if err := w.Close(context.Background()); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestWriterSurvivesDatabaseErrors(t *testing.T) {
	db := openTemp(t)
	w := NewWriter(db, WriterOptions{FlushInterval: 5 * time.Millisecond})
	_ = db.Close() // every flush now fails
	w.Enqueue(Request{TS: time.Now()})
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := w.Stats(); s.Failed != 1 || s.Written != 0 {
		t.Errorf("stats %+v", s)
	}
}

func TestRunRetentionPurgesOnStartAndStops(t *testing.T) {
	db := openTemp(t)
	ctx, cancel := context.WithCancel(context.Background())
	if err := db.insertRequests(ctx, []Request{{TS: time.Now().AddDate(0, 0, -200)}}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		db.RunRetention(ctx, func() int { return 90 }, time.Hour, nil)
		close(done)
	}()
	deadline := time.After(2 * time.Second)
	for {
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM requests`).Scan(&n)
		if n == 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("old row was not purged")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunRetention did not stop")
	}
}
