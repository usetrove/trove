package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// Migration 0012 rebuilds hook_events to allow new kinds. A user who already has
// hook rows must keep every one of them, with their ids.
func TestHookRewritesMigrationKeepsExistingRows(t *testing.T) {
	ctx := context.Background()
	db, err := OpenNoMigrate(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	var target *migration
	for i, m := range ms {
		if m.name == "0012_hook_rewrites.sql" {
			target = &ms[i]
			break
		}
		if _, err := db.ExecContext(ctx, m.sql); err != nil {
			t.Fatalf("%s: %v", m.name, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`, m.version, time.Now().UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	if target == nil {
		t.Fatal("0012_hook_rewrites.sql not found")
	}

	// The old table refuses the new kinds, so this really tests the rebuild.
	if _, err := db.ExecContext(ctx, `INSERT INTO hook_events(ts, session_key, hook, kind) VALUES(1, 'c-x', 'h', 'rewritten')`); err == nil {
		t.Fatal("the old schema should reject 'rewritten'")
	}
	old := []struct {
		id         int
		ts         int64
		key, hook  string
		kind       string
		bytes, tok int64
	}{
		{7, 1000, "c-aaa", "hook_repeat_read", "blocked", 4096, 1170},
		{9, 2000, "c-aaa", "hook_repeat_read", "reread_anyway", 4096, 1170},
		{12, 3000, "c-bbb", "hook_large_output", "would_advise", 0, 0},
	}
	for _, r := range old {
		if _, err := db.ExecContext(ctx, `INSERT INTO hook_events(id, ts, session_key, hook, kind, bytes, tokens) VALUES(?,?,?,?,?,?,?)`,
			r.id, r.ts, r.key, r.hook, r.kind, r.bytes, r.tok); err != nil {
			t.Fatal(err)
		}
	}

	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	for _, r := range old {
		var ts, bytes, tok int64
		var key, hook, kind string
		if err := db.QueryRowContext(ctx, `SELECT ts, session_key, hook, kind, bytes, tokens FROM hook_events WHERE id = ?`, r.id).
			Scan(&ts, &key, &hook, &kind, &bytes, &tok); err != nil {
			t.Fatalf("row %d was lost: %v", r.id, err)
		}
		if ts != r.ts || key != r.key || hook != r.hook || kind != r.kind || bytes != r.bytes || tok != r.tok {
			t.Errorf("row %d changed: %d %s %s %s %d %d", r.id, ts, key, hook, kind, bytes, tok)
		}
	}
	// The new kinds are accepted, bad ones still are not, and the indexes came back.
	for _, k := range []string{"rewritten", "would_rewrite", "reissued", "fetched_back"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO hook_events(ts, session_key, hook, kind) VALUES(5, 'c-x', 'h', ?)`, k); err != nil {
			t.Errorf("kind %q rejected after migrating: %v", k, err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO hook_events(ts, session_key, hook, kind) VALUES(5, 'c-x', 'h', 'bogus')`); err == nil {
		t.Error("an unknown kind must still be rejected")
	}
	var idx int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND tbl_name = 'hook_events' AND name IN ('hook_events_session', 'hook_events_hook_ts')`).Scan(&idx); err != nil || idx != 2 {
		t.Errorf("indexes after migrating: %d (err %v)", idx, err)
	}
}
