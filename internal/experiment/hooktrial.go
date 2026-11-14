package experiment

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/store"
)

// AnalyzeHookTrial compares sessions for an optimizer that runs in the plugin's hooks, which assign each
// session an arm themselves (see hooks.reset) and record it in experiment_assignments.
//
// It differs from Analyze in three ways, all because a hooks user may have no gateway:
//
//   - The headline is tokens per session (input, cache and output), not dollars, because a subscription user
//     does not pay per token and the sessions may have no price. Turns and tool errors are compared as before.
//     Abandoned sessions are not compared: only the gateway sees them, and a zero would be invented.
//   - A session's figures come from the gateway's own record of it when there is one, and otherwise from the
//     agent's local history (history_requests), joined on the shared session key. A session in both is taken
//     from the gateway only, so it is never counted twice.
//   - Every assigned session counts under the arm it was assigned (intention to treat), whether or not the
//     hook acted in it. Leaving out "on" sessions where the hook never acted would leave out exactly the ones
//     it could not have changed, and flatter it.
//
// A session counts only once it has been idle for idle, so a session still running is not compared on part of
// its tokens. Sessions with no data yet are left out and counted in Excluded. History token counts are a lower
// bound on what was used; both arms are measured the same way, so the comparison is fair, and the label says
// "measured" only for what was counted.
func AnalyzeHookTrial(ctx context.Context, db *store.DB, optimizer string, x config.Experiment, notBeforeMS int64, now time.Time, idle time.Duration) (Result, error) {
	rows, err := db.QueryContext(ctx, `SELECT session_key, arm FROM experiment_assignments
		WHERE optimizer = ? AND assigned_at > ? ORDER BY session_key`, optimizer, notBeforeMS)
	if err != nil {
		return Result{}, err
	}
	type asg struct{ key, arm string }
	var asgs []asg
	for rows.Next() {
		var a asg
		if err := rows.Scan(&a.key, &a.arm); err != nil {
			_ = rows.Close()
			return Result{}, err
		}
		asgs = append(asgs, a)
	}
	if err := rows.Close(); err != nil {
		return Result{}, err
	}
	if err := rows.Err(); err != nil {
		return Result{}, err
	}
	cutoff := now.Add(-idle).UnixMilli()
	var kept []sess
	excluded := 0
	for _, a := range asgs {
		s, ok, err := hookSession(ctx, db, a.key, cutoff)
		if err != nil {
			return Result{}, err
		}
		if !ok {
			excluded++
			continue
		}
		s.arm = a.arm
		kept = append(kept, s)
	}
	return analyzeAll(ctx, db, optimizer, x, Priors{}, kept, excluded, 0, MetricTokens)
}

// hookSession reads one assigned session's figures. ok is false when it has no data yet or is still running.
func hookSession(ctx context.Context, db *store.DB, key string, cutoffMS int64) (sess, bool, error) {
	s := sess{key: key}
	like := key + "~%"

	// The gateway's record first: it has the session's own roll-up of tool errors.
	var gn int
	var last sql.NullInt64
	var tokens, turns int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*), MAX(COALESCE(ended_at, started_at)),
		COALESCE(SUM(total_input_uncached_tokens + total_cache_write_tokens + total_cache_read_tokens + total_output_tokens), 0),
		COALESCE(SUM(request_count), 0) FROM sessions WHERE session_key = ? OR session_key LIKE ?`, key, like).
		Scan(&gn, &last, &tokens, &turns); err != nil {
		return s, false, err
	}
	if gn > 0 {
		if !last.Valid || last.Int64 >= cutoffMS {
			return s, false, nil
		}
		sig, err := db.QueryContext(ctx, `SELECT outcome_signals FROM sessions WHERE session_key = ? OR session_key LIKE ?`, key, like)
		if err != nil {
			return s, false, err
		}
		defer func() { _ = sig.Close() }()
		var errs float64
		for sig.Next() {
			var raw sql.NullString
			if err := sig.Scan(&raw); err != nil {
				return s, false, err
			}
			if !raw.Valid {
				return s, false, nil // not rolled up yet
			}
			var ss measure.SessionSignals
			if json.Unmarshal([]byte(raw.String), &ss) != nil {
				return s, false, nil
			}
			errs += float64(ss.ToolErrors)
		}
		if err := sig.Err(); err != nil {
			return s, false, err
		}
		s.cost, s.turns, s.errs = float64(tokens), float64(turns), errs
		return s, true, nil
	}

	var hn int
	var hlast sql.NullInt64
	var herrs int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*), MAX(ts),
		COALESCE(SUM(input_tokens + cache_write_5m + cache_write_1h + cache_write_other + cache_read + output_tokens), 0),
		COALESCE(SUM(errors), 0) FROM history_requests WHERE session_key = ? OR session_key LIKE ?`, key, like).
		Scan(&hn, &hlast, &tokens, &herrs); err != nil {
		return s, false, err
	}
	if hn == 0 || !hlast.Valid || hlast.Int64 >= cutoffMS {
		return s, false, nil
	}
	s.cost, s.turns, s.errs = float64(tokens), float64(hn), float64(herrs)
	return s, true, nil
}
