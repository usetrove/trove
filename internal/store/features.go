package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

// RepoFeaturesRow is the stored bucketed description of a repo. Fields hold
// fixed categories and numbers only (see internal/features).
type RepoFeaturesRow struct {
	RepoKey       string
	ComputedAt    time.Time
	SizeBucket    string // S, M, L or XL
	LangMix       string // "go:70,python:30"
	TestFramework string
}

// SessionFeaturesRow is one session's stored features: numbers only.
type SessionFeaturesRow struct {
	SessionKey   string
	RepoKey      string
	RecordedAt   time.Time
	FilesRead    int
	FilesEdited  int
	RepeatReads  int
	ScoutCalls   int
	Compactions  int
	Retries      int
	ToolResults  int
	LargeResults int
	AvgDupRatio  float64
	ErrorShare   float64
	StackShare   float64
}

// Features stores content-derived features (internal/features).
type Features struct{ db *DB }

// Features returns the feature store for d.
func (d *DB) Features() *Features { return &Features{db: d} }

// repoSaltKey is the settings key holding this install's random salt for repo keys.
const repoSaltKey = "repo_key_salt"

// Salt returns the per-install salt used to hash working directories into repo
// keys, creating it on first use. It is random, local and never leaves the device.
func (f *Features) Salt(ctx context.Context) (string, error) {
	var v string
	err := f.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, repoSaltKey).Scan(&v)
	if err == nil {
		return v, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	v = hex.EncodeToString(b)
	// A concurrent writer may have won; the first value stays.
	if _, err := f.db.ExecContext(ctx, `INSERT OR IGNORE INTO settings (key, value) VALUES (?, ?)`, repoSaltKey, v); err != nil {
		return "", err
	}
	err = f.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, repoSaltKey).Scan(&v)
	return v, err
}

// ExistingSalt returns the repo-key salt if one has been created, and never
// creates it, so read-only commands (the report) leave no trace.
func (f *Features) ExistingSalt(ctx context.Context) (string, bool, error) {
	var v string
	err := f.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, repoSaltKey).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return v, err == nil, err
}

// PutRepo stores or replaces the features of a repo.
func (f *Features) PutRepo(ctx context.Context, r RepoFeaturesRow) error {
	_, err := f.db.ExecContext(ctx, `INSERT INTO repo_features (repo_key, computed_at, size_bucket, lang_mix, test_framework)
		VALUES (?,?,?,?,?)
		ON CONFLICT(repo_key) DO UPDATE SET computed_at=excluded.computed_at, size_bucket=excluded.size_bucket,
		lang_mix=excluded.lang_mix, test_framework=excluded.test_framework`,
		r.RepoKey, r.ComputedAt.UnixMilli(), r.SizeBucket, r.LangMix, r.TestFramework)
	return err
}

// Repo returns the stored features of a repo.
func (f *Features) Repo(ctx context.Context, key string) (RepoFeaturesRow, bool, error) {
	r := RepoFeaturesRow{RepoKey: key}
	var at int64
	err := f.db.QueryRowContext(ctx, `SELECT computed_at, size_bucket, lang_mix, test_framework FROM repo_features WHERE repo_key = ?`, key).
		Scan(&at, &r.SizeBucket, &r.LangMix, &r.TestFramework)
	if errors.Is(err, sql.ErrNoRows) {
		return RepoFeaturesRow{}, false, nil
	}
	if err != nil {
		return RepoFeaturesRow{}, false, err
	}
	r.ComputedAt = time.UnixMilli(at).UTC()
	return r, true, nil
}

// PutSession stores a session's features. The first row for a session wins, so a
// repeated session-end hook does not double count.
func (f *Features) PutSession(ctx context.Context, s SessionFeaturesRow) error {
	_, err := f.db.ExecContext(ctx, `INSERT OR IGNORE INTO session_features
		(session_key, repo_key, recorded_at, files_read, files_edited, repeat_reads, scout_calls, compactions, retries,
		 tool_results, large_results, avg_dup_ratio, error_share, stack_share)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		s.SessionKey, s.RepoKey, s.RecordedAt.UnixMilli(), s.FilesRead, s.FilesEdited, s.RepeatReads, s.ScoutCalls,
		s.Compactions, s.Retries, s.ToolResults, s.LargeResults, s.AvgDupRatio, s.ErrorShare, s.StackShare)
	return err
}

// Sessions returns the most recent limit sessions recorded for a repo, newest first.
func (f *Features) Sessions(ctx context.Context, repoKey string, limit int) ([]SessionFeaturesRow, error) {
	rows, err := f.db.QueryContext(ctx, `SELECT session_key, repo_key, recorded_at, files_read, files_edited, repeat_reads,
		scout_calls, compactions, retries, tool_results, large_results, avg_dup_ratio, error_share, stack_share
		FROM session_features WHERE repo_key = ? ORDER BY recorded_at DESC, session_key LIMIT ?`, repoKey, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []SessionFeaturesRow
	for rows.Next() {
		var s SessionFeaturesRow
		var at int64
		if err := rows.Scan(&s.SessionKey, &s.RepoKey, &at, &s.FilesRead, &s.FilesEdited, &s.RepeatReads, &s.ScoutCalls,
			&s.Compactions, &s.Retries, &s.ToolResults, &s.LargeResults, &s.AvgDupRatio, &s.ErrorShare, &s.StackShare); err != nil {
			return nil, err
		}
		s.RecordedAt = time.UnixMilli(at).UTC()
		out = append(out, s)
	}
	return out, rows.Err()
}
