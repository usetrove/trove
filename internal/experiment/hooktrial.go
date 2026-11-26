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
	return AnalyzeHookTrialBy(ctx, db, optimizer, x, notBeforeMS, now, idle, Weigher{})
}

// SessionUsage is one session's provider-reported usage, for a Weigher. Tokens come from the agent's history; a
// session the gateway recorded has only its total and its dollars (FromGateway).
type SessionUsage struct {
	Model                                             string
	Input, Write5m, Write1h, WriteOther, Read, Output int64
	USD                                               float64
	HasUSD                                            bool
	FromGateway                                       bool
}

// Weigher turns a session's usage into the figure a hook trial is judged on, under Metric. ok false leaves the
// session out (it is counted in Excluded). The zero Weigher keeps tokens per session.
type Weigher struct {
	Metric string
	Weigh  func(SessionUsage) (float64, bool)
}

// AnalyzeHookTrialBy is AnalyzeHookTrial judged on w's figure instead of tokens: the optimize policy uses it to
// judge a trial on the bill (percent of the plan limits, or API-equivalent cost). Everything else is the same.
func AnalyzeHookTrialBy(ctx context.Context, db *store.DB, optimizer string, x config.Experiment, notBeforeMS int64, now time.Time, idle time.Duration, w Weigher) (Result, error) {
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
		if w.Weigh != nil {
			u, err := sessionUsage(ctx, db, a.key, s)
			if err != nil {
				return Result{}, err
			}
			v, ok := w.Weigh(u)
			if !ok {
				excluded++
				continue
			}
			s.cost = v
		}
		s.arm = a.arm
		kept = append(kept, s)
	}
	headline := MetricTokens
	if w.Weigh != nil && w.Metric != "" {
		headline = w.Metric
	}
	res, err := analyzeAll(ctx, db, optimizer, x, Priors{}, kept, excluded, 0, headline)
	if err != nil {
		return res, err
	}
	on, off, err := measure.TestFailByArm(ctx, db, optimizer, notBeforeMS, cutoff)
	if err != nil {
		return res, err
	}
	res.TestFail = &TestFailResult{OnRate: on.Rate(), OffRate: off.Rate(), OnSessions: on.Sessions, OffSessions: off.Sessions, Label: measure.Measured}
	return res, nil
}

// hookSession reads one assigned session's figures. ok is false when it has no data yet or is still running.
func hookSession(ctx context.Context, db *store.DB, key string, cutoffMS int64) (sess, bool, error) {
	s := sess{key: key}
	like := key + "~%"

	// The gateway's record first: it has the session's own roll-up of tool errors.
	var gn int
	var last sql.NullInt64
	var tokens, turns, output, unpriced int64
	var usd float64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*), MAX(COALESCE(ended_at, started_at)),
		COALESCE(SUM(total_input_uncached_tokens + total_cache_write_tokens + total_cache_read_tokens + total_output_tokens), 0),
		COALESCE(SUM(request_count), 0), COALESCE(SUM(total_output_tokens), 0), COALESCE(SUM(total_cost_usd), 0),
		(SELECT COUNT(*) FROM requests r WHERE (r.session_key = ? OR r.session_key LIKE ?) AND r.cost_usd IS NULL)
		FROM sessions WHERE session_key = ? OR session_key LIKE ?`, key, like, key, like).
		Scan(&gn, &last, &tokens, &turns, &output, &usd, &unpriced); err != nil {
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
		s.cost, s.turns, s.errs, s.output = float64(tokens), float64(turns), errs, float64(output)
		s.usd, s.hasUSD = usd, unpriced == 0
		return s, true, nil
	}

	var hn int
	var hlast sql.NullInt64
	var herrs, priced int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*), MAX(ts),
		COALESCE(SUM(input_tokens + cache_write_5m + cache_write_1h + cache_write_other + cache_read + output_tokens), 0),
		COALESCE(SUM(errors), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(cost_usd), 0), COUNT(cost_usd)
		FROM history_requests WHERE session_key = ? OR session_key LIKE ?`, key, like).
		Scan(&hn, &hlast, &tokens, &herrs, &output, &usd, &priced); err != nil {
		return s, false, err
	}
	if hn == 0 || !hlast.Valid || hlast.Int64 >= cutoffMS {
		return s, false, nil
	}
	s.cost, s.turns, s.errs, s.output = float64(tokens), float64(hn), float64(herrs), float64(output)
	// Dollars from the agent's history are the provider-reported tokens at Trove's price table. A session with
	// any unpriced request has none, so it is left out of cost per turn rather than counted cheap.
	s.usd, s.hasUSD = usd, priced == int64(hn)
	return s, true, nil
}

// sessionUsage reads the per-type tokens and the most-used model of a session from the agent's history. A session
// with no history rows was read from the gateway's record: it keeps its dollars and is marked FromGateway.
func sessionUsage(ctx context.Context, db *store.DB, key string, s sess) (SessionUsage, error) {
	u := SessionUsage{USD: s.usd, HasUSD: s.hasUSD}
	like := key + "~%"
	var n int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(cache_write_5m), 0),
		COALESCE(SUM(cache_write_1h), 0), COALESCE(SUM(cache_write_other), 0), COALESCE(SUM(cache_read), 0), COALESCE(SUM(output_tokens), 0)
		FROM history_requests WHERE session_key = ? OR session_key LIKE ?`, key, like).
		Scan(&n, &u.Input, &u.Write5m, &u.Write1h, &u.WriteOther, &u.Read, &u.Output); err != nil {
		return u, err
	}
	if n == 0 {
		u.FromGateway = true
		return u, nil
	}
	var model sql.NullString
	err := db.QueryRowContext(ctx, `SELECT model FROM history_requests WHERE (session_key = ? OR session_key LIKE ?) AND model != ''
		GROUP BY model ORDER BY COUNT(*) DESC, model LIMIT 1`, key, like).Scan(&model)
	if err != nil && err != sql.ErrNoRows {
		return u, err
	}
	u.Model = model.String
	return u, nil
}
