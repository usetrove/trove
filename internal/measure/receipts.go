package measure

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/store"
)

// Receipt is the one-line summary of a finished session. It holds counts, token
// rates and dollar amounts only: no prompt text, file names or tool output.
type Receipt struct {
	// Session is a short form of the session key, which is itself a hash.
	Session string    `json:"session"`
	Agent   string    `json:"agent"`
	EndedAt time.Time `json:"ended_at"`
	// Model is the model used for most calls; Models is how many different ones
	// the session used.
	Model  string `json:"model"`
	Models int    `json:"models"`
	Calls  int    `json:"calls"`
	// Cost is provider-reported usage priced at list prices (estimated).
	// Unpriced counts calls left out, so the cost is then a lower bound.
	Cost Figure `json:"cost_usd"`
	// CacheHit is cache-read tokens as a percent of all input tokens (measured).
	// Nil when the session sent no input.
	CacheHit *Figure `json:"cache_hit_percent,omitempty"`
	// Saved is what optimizers that were on saved (estimated).
	Saved Figure `json:"saved_usd"`
}

// receiptHorizon is how far back a sweep looks for finished sessions, so the
// first sweep on an old database does not write thousands of receipts.
const receiptHorizon = 7 * 24 * time.Hour

// SweepReceipts writes a receipt for every session that has been idle for at
// least idle and has none yet, and returns the receipts it wrote, oldest first.
// Running it again writes nothing new. This is the session-end boundary: the
// gateway has no end event, so a session ends when its idle gap has passed
// (the same rule RollupSessions uses).
func SweepReceipts(ctx context.Context, db *store.DB, idle time.Duration, now time.Time) ([]Receipt, error) {
	rows, err := db.QueryContext(ctx, `SELECT session_key, agent, ended_at FROM sessions
		WHERE ended_at IS NOT NULL AND ended_at <= ? AND ended_at >= ?
		AND session_key NOT IN (SELECT session_key FROM receipts) ORDER BY ended_at`,
		now.Add(-idle).UnixMilli(), now.Add(-receiptHorizon).UnixMilli())
	if err != nil {
		return nil, err
	}
	type cand struct {
		key, agent string
		ended      int64
	}
	var cands []cand
	for rows.Next() {
		var c cand
		var agent sql.NullString
		if err := rows.Scan(&c.key, &agent, &c.ended); err != nil {
			_ = rows.Close()
			return nil, err
		}
		c.agent = agent.String
		cands = append(cands, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	_ = rows.Close()

	var out []Receipt
	for _, c := range cands {
		r, ok, err := buildReceipt(ctx, db, c.key, c.agent, c.ended)
		if err != nil {
			return out, err
		}
		if !ok {
			// No successful call: nothing to summarize. Mark it so it is not rescanned.
			r = Receipt{Session: shortKey(c.key), Agent: c.agent, EndedAt: time.UnixMilli(c.ended), Models: 0,
				Cost: Figure{Label: Estimated}, Saved: Figure{Label: Estimated}}
		}
		if err := insertReceipt(ctx, db, c.key, r); err != nil {
			return out, err
		}
		if ok {
			out = append(out, r)
		}
	}
	return out, nil
}

func buildReceipt(ctx context.Context, db *store.DB, key, agent string, ended int64) (Receipt, bool, error) {
	var calls, unpriced int
	var cost, saved float64
	var input, cacheWrite, cacheRead int64
	err := db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(CASE WHEN cost_usd IS NULL THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(cost_usd), 0),
		COALESCE(SUM(input_uncached_tokens), 0), COALESCE(SUM(cache_write_tokens), 0), COALESCE(SUM(cache_read_tokens), 0),
		COALESCE(SUM(CASE WHEN tokens_removed > 0 AND optimizations_applied IS NOT NULL THEN cost_diff_usd END), 0)
		FROM requests WHERE session_key = ? AND status_code > 0 AND status_code < 400`, key).
		Scan(&calls, &unpriced, &cost, &input, &cacheWrite, &cacheRead, &saved)
	if err != nil {
		return Receipt{}, false, err
	}
	if calls == 0 {
		return Receipt{}, false, nil
	}
	var model string
	if err := db.QueryRowContext(ctx, `SELECT model FROM requests WHERE session_key = ? AND status_code > 0 AND status_code < 400
		GROUP BY model ORDER BY COUNT(*) DESC, model LIMIT 1`, key).Scan(&model); err != nil {
		return Receipt{}, false, err
	}
	var models int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT model) FROM requests WHERE session_key = ? AND status_code > 0 AND status_code < 400`, key).Scan(&models); err != nil {
		return Receipt{}, false, err
	}
	r := Receipt{
		Session: shortKey(key), Agent: agent, EndedAt: time.UnixMilli(ended), Model: model, Models: models, Calls: calls,
		Cost:  Figure{Value: roundUSD(cost), Label: Estimated, N: calls - unpriced, Unpriced: unpriced},
		Saved: Figure{Value: roundUSD(saved), Label: Estimated, N: calls},
	}
	if total := input + cacheWrite + cacheRead; total > 0 {
		r.CacheHit = &Figure{Value: 100 * float64(cacheRead) / float64(total), Label: Measured, N: calls}
	}
	return r, true, nil
}

func insertReceipt(ctx context.Context, db *store.DB, key string, r Receipt) error {
	var hit any
	if r.CacheHit != nil {
		hit = r.CacheHit.Value
	}
	_, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO receipts(session_key, agent, ended_at, model, models, calls, cost_usd, unpriced, cache_hit_rate, saved_usd)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, key, r.Agent, r.EndedAt.UnixMilli(), r.Model, r.Models, r.Calls, r.Cost.Value, r.Cost.Unpriced, hit, r.Saved.Value)
	return err
}

// ListReceipts returns the newest receipts first. Sessions with no successful
// call (models = 0) are skipped.
func ListReceipts(ctx context.Context, db *store.DB, limit int) ([]Receipt, error) {
	rows, err := db.QueryContext(ctx, `SELECT session_key, agent, ended_at, model, models, calls, cost_usd, unpriced, cache_hit_rate, saved_usd
		FROM receipts WHERE models > 0 ORDER BY ended_at DESC, session_key LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Receipt{}
	for rows.Next() {
		var key, agent, model string
		var ended int64
		var models, calls, unpriced int
		var cost, saved float64
		var hit sql.NullFloat64
		if err := rows.Scan(&key, &agent, &ended, &model, &models, &calls, &cost, &unpriced, &hit, &saved); err != nil {
			return nil, err
		}
		r := Receipt{Session: shortKey(key), Agent: agent, EndedAt: time.UnixMilli(ended), Model: model, Models: models, Calls: calls,
			Cost:  Figure{Value: cost, Label: Estimated, N: calls - unpriced, Unpriced: unpriced},
			Saved: Figure{Value: saved, Label: Estimated, N: calls}}
		if hit.Valid {
			r.CacheHit = &Figure{Value: hit.Float64, Label: Measured, N: calls}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// shortKey keeps a recognizable part of a session key for display.
func shortKey(key string) string {
	base, gen, hasGen := strings.Cut(key, "~")
	if i := strings.IndexByte(base, '-'); i >= 0 && i+1 < len(base) {
		base = base[i+1:]
	}
	if len(base) > 8 {
		base = base[:8]
	}
	if hasGen {
		return base + "~" + gen
	}
	return base
}
