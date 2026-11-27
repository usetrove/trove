package hooks

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/regress"
	"github.com/usetrove/trove/internal/store"
)

// At a session end the regression watch reads the newest history, but only where Trove already reads it, and at
// most every regress.RefreshEvery.
func TestRegressStepReadsHistoryOnlyWhereTroveAlreadyDoes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".trove")
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now()
	cc := t.TempDir()
	writeClaudeHistory(t, cc, 6, now.Add(-2*time.Hour))
	getenv := func(k string) string {
		if k == "CLAUDE_CONFIG_DIR" {
			return cc
		}
		return ""
	}
	ctx := context.Background()

	// History never scanned: no read, but a result is saved.
	regressStep(ctx, db, dir, config.Default(), getenv, now)
	if n := historyRows(t, db); n != 0 {
		t.Fatalf("read %d history rows before Trove ever scanned history", n)
	}
	first := regress.ReadCache(dir)
	if first == nil {
		t.Fatal("no result saved")
	}

	// Once Trove has read history, a stale result brings it up to date.
	if _, err := db.Exec(`INSERT INTO history_files(file_key, source, size, mtime, offset, parser_version, updated_at) VALUES('x', 'claude_code', 0, 0, 0, 0, 0)`); err != nil {
		t.Fatal(err)
	}
	later := now.Add(regress.RefreshEvery + time.Minute)
	regressStep(ctx, db, dir, config.Default(), getenv, later)
	if n := historyRows(t, db); n != 6 {
		t.Fatalf("history rows = %d, want 6", n)
	}
	if r := regress.ReadCache(dir); r == nil || r.ComputedAt != later.UnixMilli() {
		t.Fatalf("result not recomputed: %+v", r)
	}

	// Fresh: nothing is read or recomputed.
	if err := os.RemoveAll(filepath.Join(cc, "projects")); err != nil {
		t.Fatal(err)
	}
	regressStep(ctx, db, dir, config.Default(), getenv, later.Add(time.Minute))
	if r := regress.ReadCache(dir); r.ComputedAt != later.UnixMilli() {
		t.Error("recomputed a fresh result")
	}

	// History off: never read.
	cfg := config.Default()
	cfg.History.Enabled = false
	writeClaudeHistory(t, cc, 9, now.Add(-time.Hour))
	regressStep(ctx, db, dir, cfg, getenv, later.Add(2*regress.RefreshEvery))
	if n := historyRows(t, db); n != 6 {
		t.Errorf("read history with history off: %d rows", n)
	}
}
