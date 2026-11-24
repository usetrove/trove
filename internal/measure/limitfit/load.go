package limitfit

import (
	"context"
	"database/sql"
	"time"

	"github.com/usetrove/trove/internal/store"
)

// holdBack is how far behind the last history scan requests may still be missing: the history reader
// holds back requests whose tool calls have no result yet, for up to 10 minutes (internal/logs).
const holdBack = 10 * 60_000

// Load reads the meter readings and Claude Code requests of the last LookbackDays. Numbers, hashed
// session keys and model names only.
func Load(ctx context.Context, db *store.DB, now time.Time) (Data, error) {
	d := Data{Now: now.UnixMilli()}
	cutoff := now.Add(-time.Duration(LookbackDays+1) * 24 * time.Hour).UnixMilli()
	rows, err := db.QueryContext(ctx, `SELECT ts, session_key, five_hour_pct, five_hour_resets_at, seven_day_pct, seven_day_resets_at
		FROM limit_samples WHERE ts >= ? ORDER BY ts, id`, cutoff)
	if err != nil {
		return d, err
	}
	for rows.Next() {
		var s Sample
		var f, w sql.NullFloat64
		var fr, wr sql.NullInt64
		if err := rows.Scan(&s.TS, &s.Session, &f, &fr, &w, &wr); err != nil {
			_ = rows.Close()
			return d, err
		}
		if f.Valid && fr.Valid {
			s.FiveHour, s.FiveHourReset = &f.Float64, &fr.Int64
		}
		if w.Valid && wr.Valid {
			s.Week, s.WeekReset = &w.Float64, &wr.Int64
		}
		d.Samples = append(d.Samples, s)
	}
	if err := rows.Close(); err != nil {
		return d, err
	}
	rows, err = db.QueryContext(ctx, `SELECT ts, session_key, model, input_tokens, output_tokens, cache_read,
		cache_write_5m, cache_write_1h, cache_write_other, cost_usd
		FROM history_requests WHERE source = 'claude_code' AND ts >= ? ORDER BY ts, request_key`, cutoff)
	if err != nil {
		return d, err
	}
	for rows.Next() {
		var q Request
		var other int64
		var cost sql.NullFloat64
		if err := rows.Scan(&q.TS, &q.Session, &q.Model, &q.Tokens[TInput], &q.Tokens[TOutput], &q.Tokens[TCacheRead],
			&q.Tokens[TWrite5m], &q.Tokens[TWrite1h], &other, &cost); err != nil {
			_ = rows.Close()
			return d, err
		}
		q.Unsplit = other > 0
		q.USD, q.Priced = cost.Float64, cost.Valid
		d.Requests = append(d.Requests, q)
	}
	if err := rows.Close(); err != nil {
		return d, err
	}
	var last sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT MAX(updated_at) FROM history_files WHERE source = 'claude_code'`).Scan(&last); err != nil {
		return d, err
	}
	if last.Valid {
		d.HistoryThrough = last.Int64 - holdBack
	}
	return d, nil
}

// NewestSample is the time of the newest meter reading, or 0.
func NewestSample(ctx context.Context, db *store.DB) int64 {
	var ts sql.NullInt64
	_ = db.QueryRowContext(ctx, `SELECT MAX(ts) FROM limit_samples`).Scan(&ts)
	return ts.Int64
}

// Refresh recomputes the fit and saves it when the saved one is stale. Best effort: on any error the
// saved fit stays as it was. It returns the fit in force.
func Refresh(ctx context.Context, db *store.DB, dir string, now time.Time) *Result {
	cur := ReadCache(dir)
	newest := NewestSample(ctx, db)
	if newest == 0 {
		return cur
	}
	if !Stale(cur, newest, now) {
		return cur
	}
	d, err := Load(ctx, db, now)
	if err != nil {
		return cur
	}
	r := Compute(d)
	_ = WriteCache(dir, r) // best effort: the fit is still returned for this run
	return &r
}
