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
	OtherFixes int `json:"other_fixes_in_place,omitempty"`
	// TTL is set for cache lifetime fixes, which are judged on where the cache
	// writes went rather than on prompt size.
	TTL  *TTLShift `json:"cache_lifetime,omitempty"`
	Text string    `json:"text"`
}

// TTLShift is the share of Claude Code's lifetime-split cache writes made at
// one hour, in sessions that started before and after a cache lifetime fix.
// Token counts from history only.
type TTLShift struct {
	Subagents     bool    `json:"subagents"`
	Before1hShare float64 `json:"before_1h_share"`
	After1hShare  float64 `json:"after_1h_share"`
	// Target is the lifetime the fix set, inferred from the side it moved away
	// from: the fix is only proposed when most writes were at the other one.
	Target string `json:"target"`
	// Moved is true when, with enough sessions after, at least ttlMovedShare of
	// the writes are at Target.
	Moved bool `json:"moved_as_predicted"`
}

// ttlMovedShare is the share of writes at the new lifetime that counts as the
// change having taken effect. Not 1: older Claude Code builds or requests the
// setting does not cover may still write at the old lifetime.
const ttlMovedShare = 0.8

type fixRow struct {
	id, kind string
	applied  int64
	tokens   int64
	usd      *float64
}

// FixEffects reports on every fix that is still in place, newest first. It
// reads session totals only (counts and timestamps), never text.
func FixEffects(ctx context.Context, db *store.DB, m config.Measure) ([]FixEffect, error) {
	rows, err := db.QueryContext(ctx, `SELECT fix_id, kind, applied_at, est_tokens, est_usd FROM fixes WHERE reverted_at IS NULL AND kind != 'settings_profile' ORDER BY applied_at DESC`)
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
		if f.kind == "cache_ttl_main" || f.kind == "cache_ttl_subagents" {
			if err := ttlEffect(ctx, db, &e, f, m); err != nil {
				return nil, err
			}
			out = append(out, e)
			continue
		}
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

// ttlEffect fills e for a cache lifetime fix: the one-hour share of cache
// writes in the fixed bucket, before and after, grouped by session start so a
// session running when the fix was applied counts as before.
func ttlEffect(ctx context.Context, db *store.DB, e *FixEffect, f fixRow, m config.Measure) error {
	side := 0
	if f.kind == "cache_ttl_subagents" {
		side = 1
	}
	rows, err := db.QueryContext(ctx, `SELECT s.start >= ?, COUNT(DISTINCT h.session_key), SUM(h.cache_write_5m), SUM(h.cache_write_1h)
		FROM history_requests h JOIN (SELECT session_key, MIN(ts) AS start FROM history_requests
			WHERE source = 'claude_code' GROUP BY session_key) s ON s.session_key = h.session_key
		WHERE h.source = 'claude_code' AND h.sidechain = ? GROUP BY s.start >= ?`, f.applied, side, f.applied)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	t := &TTLShift{Subagents: side == 1}
	e.TTL = t
	var w5b, w1b, w5a, w1a int64
	for rows.Next() {
		var after bool
		var n int
		var w5, w1 int64
		if err := rows.Scan(&after, &n, &w5, &w1); err != nil {
			return err
		}
		if after {
			e.NAfter, w5a, w1a = n, w5, w1
		} else {
			e.NBefore, w5b, w1b = n, w5, w1
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	share := func(w5, w1 int64) float64 {
		if w5+w1 == 0 {
			return 0
		}
		return float64(w1) / float64(w5+w1)
	}
	t.Before1hShare, t.After1hShare = share(w5b, w1b), share(w5a, w1a)
	t.Target = "1h"
	if t.Before1hShare >= 0.5 {
		t.Target = "5m"
	}
	if e.NBefore < m.MinSessions || e.NAfter < m.MinSessions || w5a+w1a == 0 || w5b+w1b == 0 {
		e.Text = fmt.Sprintf("%s: %d session(s) with cache writes before and %d after; at least %d are needed on each side. The savings shown are estimates.", TextNotEnough, e.NBefore, e.NAfter, m.MinSessions)
		return nil
	}
	e.Enough, e.Label = true, Measured
	atTarget := t.After1hShare
	if t.Target == "5m" {
		atTarget = 1 - t.After1hShare
	}
	t.Moved = atTarget >= ttlMovedShare
	if t.Moved {
		e.Text = fmt.Sprintf("cache writes moved as predicted: %.0f%% were at one hour before the fix and %.0f%% after", 100*t.Before1hShare, 100*t.After1hShare)
	} else {
		e.Text = fmt.Sprintf("cache writes did not move as predicted (%.0f%% at one hour before, %.0f%% after); check that Claude Code is 2.1.242 or later and that nothing else sets the lifetime", 100*t.Before1hShare, 100*t.After1hShare)
	}
	if e.OtherFixes > 0 {
		e.Text += "; other fixes are in place too"
	}
	return nil
}
