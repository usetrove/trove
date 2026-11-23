package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LimitSample is one row of limit_samples (migration 0023): Claude Code's plan-usage meter as the
// status line received it. A nil or empty field was not sent and is stored as NULL. Numbers, a hashed
// session key, a model id and a version string only (PRINCIPLES.md rule 7).
type LimitSample struct {
	TS               time.Time
	SessionKey       string   // hashed, never Claude Code's own id
	FiveHourPct      *float64 // 0-100
	FiveHourResetsAt *int64   // unix ms
	SevenDayPct      *float64 // 0-100
	SevenDayResetsAt *int64   // unix ms
	CacheWarm        *bool
	CacheTTLSeconds  *int64 // 300 or 3600
	Model            string
	CCVersion        string
}

// SameValues reports whether s and o carry the same values, ignoring time and session.
func (s LimitSample) SameValues(o LimitSample) bool {
	return eqPtr(s.FiveHourPct, o.FiveHourPct) && eqPtr(s.FiveHourResetsAt, o.FiveHourResetsAt) &&
		eqPtr(s.SevenDayPct, o.SevenDayPct) && eqPtr(s.SevenDayResetsAt, o.SevenDayResetsAt) &&
		eqPtr(s.CacheWarm, o.CacheWarm) && eqPtr(s.CacheTTLSeconds, o.CacheTTLSeconds) &&
		s.Model == o.Model && s.CCVersion == o.CCVersion
}

func eqPtr[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// values are the columns after ts and session_key, in table order, with NULL for absent.
func (s LimitSample) values() []any {
	var warm any
	if s.CacheWarm != nil {
		warm = 0
		if *s.CacheWarm {
			warm = 1
		}
	}
	return []any{nullFloat(s.FiveHourPct), nullInt(s.FiveHourResetsAt), nullFloat(s.SevenDayPct), nullInt(s.SevenDayResetsAt),
		warm, nullInt(s.CacheTTLSeconds), nullString(s.Model), nullString(s.CCVersion)}
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

const limitValueCols = `five_hour_pct, five_hour_resets_at, seven_day_pct, seven_day_resets_at, cache_warm, cache_ttl, model, cc_version`

// LastLimitSample returns the session's latest sample, if any. It only reads, so the status line can
// call it on its read-only connection and skip the write entirely when nothing changed.
func (d *DB) LastLimitSample(ctx context.Context, sessionKey string) (LimitSample, bool, error) {
	var (
		ts                   int64
		fivePct, sevenPct    sql.NullFloat64
		fiveAt, sevenAt, ttl sql.NullInt64
		warm                 sql.NullInt64
		model, version       sql.NullString
	)
	err := d.QueryRowContext(ctx, `SELECT ts, `+limitValueCols+` FROM limit_samples
		WHERE session_key = ? ORDER BY ts DESC, id DESC LIMIT 1`, sessionKey).
		Scan(&ts, &fivePct, &fiveAt, &sevenPct, &sevenAt, &warm, &ttl, &model, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return LimitSample{}, false, nil
	}
	if err != nil {
		return LimitSample{}, false, err
	}
	s := LimitSample{TS: time.UnixMilli(ts), SessionKey: sessionKey, Model: model.String, CCVersion: version.String}
	if fivePct.Valid {
		s.FiveHourPct = &fivePct.Float64
	}
	if fiveAt.Valid {
		s.FiveHourResetsAt = &fiveAt.Int64
	}
	if sevenPct.Valid {
		s.SevenDayPct = &sevenPct.Float64
	}
	if sevenAt.Valid {
		s.SevenDayResetsAt = &sevenAt.Int64
	}
	if warm.Valid {
		w := warm.Int64 != 0
		s.CacheWarm = &w
	}
	if ttl.Valid {
		s.CacheTTLSeconds = &ttl.Int64
	}
	return s, true, nil
}

// InsertLimitSampleIfChanged stores s unless the session's latest sample already has the same values.
// The check and the insert are one statement, so two status lines racing for the same session cannot
// both add the same values. It reports whether a row was written.
func (d *DB) InsertLimitSampleIfChanged(ctx context.Context, s LimitSample) (bool, error) {
	if s.SessionKey == "" {
		return false, errors.New("a limit sample needs a session key")
	}
	vals := s.values()
	cols := strings.Split(limitValueCols, ", ")
	conds := make([]string, len(cols))
	for i, c := range cols {
		conds[i] = "last." + c + " IS ?" // IS: NULL equals NULL
	}
	q := `INSERT INTO limit_samples (ts, session_key, ` + limitValueCols + `)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		WHERE NOT EXISTS (
			SELECT 1 FROM (SELECT * FROM limit_samples WHERE session_key = ? ORDER BY ts DESC, id DESC LIMIT 1) AS last
			WHERE ` + strings.Join(conds, " AND ") + `)`
	args := append([]any{s.TS.UnixMilli(), s.SessionKey}, vals...)
	args = append(args, s.SessionKey)
	args = append(args, vals...)
	res, err := d.ExecContext(ctx, q, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// OpenSampleWriter opens an existing database for the status line's one small write. It creates
// nothing, runs no migration and never waits for a lock (busy_timeout 0): if another process is
// writing, the write fails at once with SQLITE_BUSY and the caller skips the sample (rule 4). A
// missing file is an error that satisfies os.IsNotExist.
func OpenSampleWriter(dataDir string) (*DB, error) {
	path := filepath.Join(dataDir, DBFileName)
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	sdb, err := sql.Open("sqlite", fileURI(path)+"?mode=rw&_pragma=busy_timeout(0)")
	if err != nil {
		return nil, fmt.Errorf("cannot open the database %s: %v", path, err)
	}
	sdb.SetMaxOpenConns(1)
	return &DB{DB: sdb, path: path}, nil
}

// fileURI is path as a SQLite file: URI, with Windows drive paths handled.
func fileURI(path string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		return "file:/" + p
	}
	return "file:" + p
}
