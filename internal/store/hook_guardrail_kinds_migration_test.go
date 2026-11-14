package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// Migration 0018 rebuilds hook_events again, to allow extra_read and adopted. A user who already has hook rows,
// including the kinds 0012 added, must keep every one of them with its id.
func TestHookGuardrailKindsMigrationKeepsExistingRows(t *testing.T) {
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
	found := false
	for _, m := range ms {
		if m.name == "0018_hook_guardrail_kinds.sql" {
			found = true
			break
		}
		if _, err := db.ExecContext(ctx, m.sql); err != nil {
			t.Fatalf("%s: %v", m.name, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`, m.version, time.Now().UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	if !found {
		t.Fatal("0018_hook_guardrail_kinds.sql not found")
	}

	// The old table refuses the new kinds, so this really tests the rebuild.
	for _, k := range []string{"extra_read", "adopted"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO hook_events(ts, session_key, hook, kind) VALUES(1, 'c-x', 'h', ?)`, k); err == nil {
			t.Fatalf("the old schema should reject %q", k)
		}
	}
	old := []struct {
		id         int
		ts         int64
		key, hook  string
		kind       string
		bytes, tok int64
	}{
		{7, 1000, "c-aaa", "hook_repeat_read", "blocked", 4096, 1170},
		{9, 2000, "c-aaa", "hook_quiet_commands", "reissued", 0, 0},
		{12, 3000, "c-bbb", "output_trimmer", "fetched_back", 0, 0},
		{15, 4000, "c-bbb", "hook_read_limit", "would_rewrite", 9000, 2500},
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
	for _, k := range []string{"extra_read", "adopted", "rewritten", "fetched_back"} {
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
