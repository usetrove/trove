package measure

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/store"
)

// FixEffect is what the data shows about one config fix applied by `trove fix`.
//
// Config fixes change a file the agent reads, so there is no on/off arm to
// interleave. The comparison is sessions that started before the fix with
// sessions that started after it, on prompt tokens per request as the provider
// reported them. Sessions differ, so a reduction supports the estimate and does
// not prove it. A session that was already running when the fix was applied is
// in neither group, because a fix never applies mid-session.
type FixEffect struct {
	FixID     string    `json:"fix_id"`
	Kind      string    `json:"kind"`
	AppliedAt time.Time `json:"applied_at"`
	// EstTokens and EstUSD are the estimates made when the fix was proposed,
	// per session. They are always labeled estimated.
	EstTokens int64    `json:"est_tokens_per_session"`
	EstUSD    *float64 `json:"est_usd_per_session"`
	NBefore   int      `json:"n_before_sessions"`
	NAfter    int      `json:"n_after_sessions"`
	// BeforeMean and AfterMean are mean prompt tokens per request across
	// sessions (input + cache writes + cache reads), as reported by the provider.
	BeforeMean float64 `json:"before_tokens_per_request,omitempty"`
	AfterMean  float64 `json:"after_tokens_per_request,omitempty"`
	// Label is "measured" only when both sides have enough sessions; the
	// figures above are then provider-reported usage. Until then it is
	// "estimated" and only EstTokens/EstUSD mean anything.
	Label   Label   `json:"label"`
	Enough  bool    `json:"enough_data"`
	Reduced bool    `json:"reduced"`
	Percent float64 `json:"percent_reduction,omitempty"`
	// OtherFixes counts other fixes in place; their effects are mixed in.
	OtherFixes int    `json:"other_fixes_in_place,omitempty"`
	Text       string `json:"text"`
}

type fixRow struct {
	id, kind string
	applied  int64
	tokens   int64
	usd      *float64
}

// FixEffects reports on every fix that is still in place, newest first. It
// reads session totals only (counts and timestamps), never text.
func FixEffects(ctx context.Context, db *store.DB, m config.Measure) ([]FixEffect, error) {
	rows, err := db.QueryContext(ctx, `SELECT fix_id, kind, applied_at, est_tokens, est_usd FROM fixes WHERE reverted_at IS NULL ORDER BY applied_at DESC`)
	if err != nil {
		return nil, err
	}
	var fixes []fixRow
	for rows.Next() {
		var f fixRow
		var usd *float64
		if err := rows.Scan(&f.id, &f.kind, &f.applied, &f.tokens, &usd); err != nil {
			_ = rows.Close()
			return nil, err
		}
		f.usd = usd
		fixes = append(fixes, f)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil || len(fixes) == 0 {
		return nil, err
	}

	sess, err := sessionPoints(ctx, db)
	if err != nil {
		return nil, err
	}
	out := make([]FixEffect, 0, len(fixes))
	for _, f := range fixes {
		e := FixEffect{FixID: f.id, Kind: f.kind, AppliedAt: time.UnixMilli(f.applied).UTC(), EstTokens: f.tokens, EstUSD: f.usd,
			Label: Estimated, OtherFixes: len(fixes) - 1}
		var before, after []float64
		for _, s := range sess {
			switch {
			case s.start < f.applied:
				before = append(before, s.perReq)
			case s.start >= f.applied:
				after = append(after, s.perReq)
			}
		}
		e.NBefore, e.NAfter = len(before), len(after)
		if len(before) < m.MinSessions || len(after) < m.MinSessions {
			e.Text = fmt.Sprintf("%s: %d session(s) before and %d after; at least %d are needed on each side. The savings shown are estimates.", TextNotEnough, len(before), len(after), m.MinSessions)
			out = append(out, e)
			continue
		}
		e.Enough, e.Label = true, Measured
		e.BeforeMean, e.AfterMean = mean(before), mean(after)
		se := math.Sqrt(sampleVar(before)/float64(len(before)) + sampleVar(after)/float64(len(after)))
		e.Reduced = e.BeforeMean-e.AfterMean > z95*se && e.BeforeMean > e.AfterMean
		switch {
		case e.Reduced:
			e.Percent = 100 * (e.BeforeMean - e.AfterMean) / e.BeforeMean
			e.Text = "sessions after the fix sent fewer prompt tokens per request than sessions before it; sessions differ, so this supports but does not prove the saving"
		default:
			e.Text = "no clear reduction in prompt tokens per request after the fix yet"
		}
		if e.OtherFixes > 0 {
			e.Text += "; other fixes are in place too, so their effects are mixed together"
		}
		out = append(out, e)
	}
	return out, nil
}

type sessionPoint struct {
	start  int64   // unix ms of the session's first request
	perReq float64 // provider-reported prompt tokens per request
}

// sessionPoints returns one point per session, from the gateway's sessions and
// from agent history that the gateway did not also record.
func sessionPoints(ctx context.Context, db *store.DB) ([]sessionPoint, error) {
	var pts []sessionPoint
	seen := map[string]bool{}
	rows, err := db.QueryContext(ctx, `SELECT session_key, started_at,
		CAST(total_input_uncached_tokens + total_cache_write_tokens + total_cache_read_tokens AS REAL) / request_count
		FROM sessions WHERE request_count > 0`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k string
		var p sessionPoint
		if err := rows.Scan(&k, &p.start, &p.perReq); err != nil {
			_ = rows.Close()
			return nil, err
		}
		seen[k] = true
		pts = append(pts, p)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = db.QueryContext(ctx, `SELECT session_key, MIN(ts),
		CAST(SUM(input_tokens + cache_write_5m + cache_write_1h + cache_write_other + cache_read) AS REAL) / COUNT(*)
		FROM history_requests WHERE sidechain = 0 GROUP BY session_key`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var k string
		var p sessionPoint
		if err := rows.Scan(&k, &p.start, &p.perReq); err != nil {
			return nil, err
		}
		if !seen[k] {
			pts = append(pts, p)
		}
	}
	return pts, rows.Err()
}
