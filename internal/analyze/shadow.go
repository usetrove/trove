package analyze

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"time"
)

// Optimizer names, as defined by internal/optimize. Each is derived from the
// waste detector it would act on, so shadow estimates and roll-ups use the same
// names as the optimizers that act on them. prune_unused_tools has no optimizer
// yet and is estimate-only.
const (
	OptDedupe   = "duplicate_result_collapser"
	OptNoise    = "noise_normalizer"
	OptTruncate = "output_trimmer"
	OptPrune    = "prune_unused_tools"
)

// optimizerFor maps a detector to the optimizer that would remove its waste.
// Detectors not listed are advice only and have no optimizer.
var optimizerFor = map[string]string{
	DetDuplicate:   OptDedupe,
	DetNoisy:       OptNoise,
	DetOversized:   OptTruncate,
	DetUnusedTools: OptPrune,
}

// ShadowEntry is what one optimizer would have removed from one request.
type ShadowEntry struct {
	Tokens int64    `json:"tokens"`
	USD    *float64 `json:"usd"` // nil when the request could not be priced exactly
}

// Shadow is the per-request shadow-mode record. Nothing was changed.
type Shadow struct {
	Label      string                 `json:"label"` // always "estimated"
	Optimizers map[string]ShadowEntry `json:"optimizers"`
}

// shadowFor computes, without applying anything, what each optimizer would
// have removed from this request and what that is worth. Each optimizer is
// valued on its own, so the entries overlap and must not be added together.
// The removed tokens are priced at the rate they were actually billed at: a
// token that was a cache read saves the cache-read price. With the optimizer
// on from the start of a session its output is stable from request to request,
// so the cached prefix stays valid; the one-off cost of the first request after
// switching on never arises because optimizers change only at session start.
func shadowFor(fs []Finding) Shadow {
	sh := Shadow{Label: "estimated", Optimizers: map[string]ShadowEntry{}}
	for _, f := range fs {
		name, ok := optimizerFor[f.Detector]
		if !ok || f.Tokens <= 0 {
			continue
		}
		sh.Optimizers[name] = ShadowEntry{Tokens: f.Tokens, USD: f.USD}
	}
	return sh
}

// Rollup is shadow savings summed over a set of requests.
type Rollup struct {
	Label      string                     `json:"label"` // "estimated"
	Requests   int                        `json:"requests"`
	Optimizers map[string]RollupOptimizer `json:"optimizers"`
}

// RollupOptimizer is one optimizer's totals. USD counts only requests that
// could be priced; UnpricedRequests says how many could not.
type RollupOptimizer struct {
	Requests         int     `json:"requests"`
	Tokens           int64   `json:"tokens"`
	USD              float64 `json:"usd"`
	UnpricedRequests int     `json:"unpriced_requests"`
}

// Querier is the part of *sql.DB the roll-ups need.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// SessionRollup sums the stored shadow savings of one session.
func SessionRollup(ctx context.Context, db Querier, sessionKey string) (Rollup, error) {
	return rollup(ctx, db, `SELECT shadow_savings FROM requests WHERE session_key = ? AND shadow_savings IS NOT NULL`, sessionKey)
}

// DayRollup sums the stored shadow savings of the UTC day containing day.
func DayRollup(ctx context.Context, db Querier, day time.Time) (Rollup, error) {
	d := day.UTC().Truncate(24 * time.Hour)
	return rollup(ctx, db, `SELECT shadow_savings FROM requests WHERE ts >= ? AND ts < ? AND shadow_savings IS NOT NULL`,
		d.UnixMilli(), d.Add(24*time.Hour).UnixMilli())
}

func rollup(ctx context.Context, db Querier, query string, args ...any) (Rollup, error) {
	r := Rollup{Label: "estimated", Optimizers: map[string]RollupOptimizer{}}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return r, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return r, err
		}
		var sh Shadow
		if json.Unmarshal([]byte(raw), &sh) != nil {
			continue // a row from a future version; skip rather than guess
		}
		r.Requests++
		names := make([]string, 0, len(sh.Optimizers))
		for n := range sh.Optimizers {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			e, o := sh.Optimizers[n], r.Optimizers[n]
			o.Requests++
			o.Tokens += e.Tokens
			if e.USD != nil {
				o.USD += *e.USD
			} else {
				o.UnpricedRequests++
			}
			r.Optimizers[n] = o
		}
	}
	return r, rows.Err()
}
