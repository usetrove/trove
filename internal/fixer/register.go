package fixer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/settings"
	"github.com/usetrove/trove/internal/setup"
	"github.com/usetrove/trove/internal/store"
)

func sha(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func relIn(base, p string) (string, error) {
	rel, err := filepath.Rel(base, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", filepath.ErrBadPattern
	}
	return filepath.ToSlash(rel), nil
}

// register records an applied fix so measurement can compare sessions before and
// after it. It stores ids, the kind, a hash of the path and the estimates:
// never a path, a file's contents or any text from it.
func register(e *Env, f *Fix, c setup.Change) error {
	db, err := store.Open(e.DataDir)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	var usd any
	if f.USDPerSession != nil {
		usd = *f.USDPerSession
	}
	_, err = db.ExecContext(context.Background(),
		`INSERT OR REPLACE INTO fixes (change_id, fix_id, kind, agent, path_hash, applied_at, est_tokens, est_usd, label)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'estimated')`,
		c.ID, f.ID, f.Kind, f.Agent, pathHash(f.Path), c.AppliedAt.UnixMilli(), f.TokensPerSession, usd)
	if err != nil {
		return err
	}
	if f.Kind == TaskScout || f.Kind == TaskCascade {
		return registerTask(db, e, f, c.ID)
	}
	if f.Kind != SettingsProfile {
		return nil
	}
	// Names of the settings only: never a value, a path or file content.
	keys, _ := json.Marshal(f.Keys)
	ver := ""
	if e.SettingsVersion != nil {
		ver = strings.TrimSpace(e.SettingsVersion(f.Agent))
	}
	_, err = db.ExecContext(context.Background(),
		`INSERT OR REPLACE INTO settings_profiles (change_id, fix_id, task, harness, keys, harness_version, catalog_version)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`, c.ID, f.ID, f.Task, f.Agent, string(keys), ver, settings.CatalogVersion)
	return err
}

// MarkReverted records that undone fixes are no longer in place, so the report
// stops attributing results to them. Errors are returned, not fatal: the files
// are already restored.
func MarkReverted(dataDir string, res []setup.UndoResult, now time.Time) error {
	var ids []string
	for _, r := range res {
		if r.FixID != "" && r.Action != "left_alone" {
			ids = append(ids, r.FixID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	db, err := store.Open(dataDir)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	for _, id := range ids {
		if _, err := db.ExecContext(context.Background(),
			`UPDATE fixes SET reverted_at = ? WHERE fix_id = ? AND reverted_at IS NULL`, now.UnixMilli(), id); err != nil {
			return err
		}
	}
	return nil
}
