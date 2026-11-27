package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// Migration 0024 adds nullable columns to history_requests and the history_tool_calls table. Rows
// written before it must survive with the new columns NULL (unknown), not 0.
func TestHistoryDetailMigrationKeepsRowsUnknown(t *testing.T) {
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
	found := false
	for _, m := range ms {
		if m.name == "0024_history_detail.sql" {
			found = true
			break
		}
		if _, err := db.ExecContext(ctx, m.sql); err != nil {
			t.Fatalf("%s: %v", m.name, err)
		}
	}
	if !found {
		t.Fatal("0024_history_detail.sql not found")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO history_requests (request_key, source, session_key, ts, output_tokens)
		VALUES ('rk', 'claude_code', 'c-x', 1000, 77)`); err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.name == "0024_history_detail.sql" {
			if _, err := db.ExecContext(ctx, m.sql); err != nil {
				t.Fatalf("%s: %v", m.name, err)
			}
		}
	}
	var out int64
	var thinking sql.NullInt64
	var ver, entry, eff, turn sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT output_tokens, thinking_tokens, agent_version, entrypoint, effort, turn_effort
		FROM history_requests WHERE request_key = 'rk'`).Scan(&out, &thinking, &ver, &entry, &eff, &turn); err != nil {
		t.Fatal(err)
	}
	if out != 77 || thinking.Valid || ver.Valid || entry.Valid || eff.Valid || turn.Valid {
		t.Errorf("old row should keep its numbers and read unknown for new columns: %d %v %v %v %v %v", out, thinking, ver, entry, eff, turn)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO history_tool_calls (request_key, idx, ts, tool_name, input_bytes)
		VALUES ('rk', 0, 1000, 'Read', 43)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO history_tool_calls (request_key, idx, ts, tool_name) VALUES ('rk', 0, 1000, 'Bash')`); err == nil {
		t.Error("(request_key, idx) should be unique")
	}
}
