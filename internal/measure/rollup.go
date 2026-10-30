package measure

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/store"
)

// abandonWindow and abandonRequests define "abandoned shortly after an
// optimizer acted": the session made at most abandonRequests more requests, and
// ended within abandonWindow, after the first request an optimizer changed.
// This is a heuristic: a task that was simply finished looks the same.
const (
	abandonWindow   = 5 * time.Minute
	abandonRequests = 2
	// commitGrace is how long after a session ends commits still count as its own.
	commitGrace = 30 * time.Minute
)

// SessionSignals is the finished-session roll-up stored in sessions.outcome_signals.
// Counts and optimizer names only.
type SessionSignals struct {
	Turns     int   `json:"turns"`
	Tokens    int64 `json:"tokens"`
	DurationS int64 `json:"duration_s"`
	// HasTools is false for sessions whose requests carried no readable tool
	// signals (for example OpenAI traffic); they are left out of tool-based rules.
	HasTools   bool `json:"has_tools"`
	ToolCalls  int  `json:"tool_calls"`
	Repeats    int  `json:"repeats"`
	ToolErrors int  `json:"tool_errors"`
	Collapsed  int  `json:"collapsed"`
	Refetches  int  `json:"refetches"`
	Abandoned  bool `json:"abandoned_after_action"`
	Commits    *int `json:"commits,omitempty"`
	// On lists the optimizers pinned "on" for the session; Acted those that
	// actually changed at least one of its requests.
	On       []string `json:"on"`
	Acted    []string `json:"acted"`
	PinnedAt int64    `json:"pinned_at"` // ms; 0 if the session had no pin
}

// RollupSessions summarises sessions that have been idle for longer than idle
// and have no roll-up yet, writing sessions.outcome_signals. It is idempotent
// and runs off the request path. Only counts are read or written.
func RollupSessions(ctx context.Context, db *store.DB, m config.Measure, now time.Time, idle time.Duration) (int, error) {
	return rollupSessions(ctx, db, m, now, idle, "")
}

// rollupSessions is RollupSessions limited to one session (and its idle-gap
// successors, which share the key plus "~n") when baseKey is not empty.
func rollupSessions(ctx context.Context, db *store.DB, m config.Measure, now time.Time, idle time.Duration, baseKey string) (int, error) {
	cutoff := now.Add(-idle).UnixMilli()
	rows, err := db.QueryContext(ctx, `SELECT session_key, started_at, COALESCE(ended_at, started_at), request_count,
		total_input_uncached_tokens + total_cache_write_tokens + total_cache_read_tokens + total_output_tokens
		FROM sessions WHERE outcome_signals IS NULL AND COALESCE(ended_at, started_at) < ?
		AND (? = '' OR session_key = ? OR session_key LIKE ?)`, cutoff, baseKey, baseKey, baseKey+"~%")
	if err != nil {
		return 0, err
	}
	type sess struct {
		key                string
		start, end, tokens int64
		turns              int
	}
	var todo []sess
	for rows.Next() {
		var s sess
		if err := rows.Scan(&s.key, &s.start, &s.end, &s.turns, &s.tokens); err != nil {
			_ = rows.Close()
			return 0, err
		}
		todo = append(todo, s)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	done := 0
	for _, s := range todo {
		sig, err := summarise(ctx, db, s.key)
		if err != nil {
			return done, err
		}
		sig.Turns, sig.Tokens, sig.DurationS = s.turns, s.tokens, (s.end-s.start)/1000
		if m.GitSignals && m.GitRepo != "" {
			// A git failure loses this one optional signal, never the roll-up.
			if n, err := CommitCount(ctx, m.GitRepo, time.UnixMilli(s.start), time.UnixMilli(s.end).Add(commitGrace)); err == nil {
				sig.Commits = &n
			}
		}
		b, err := json.Marshal(sig)
		if err != nil {
			return done, err
		}
		if _, err := db.ExecContext(ctx, `UPDATE sessions SET outcome_signals = ? WHERE session_key = ?`, string(b), s.key); err != nil {
			return done, err
		}
		done++
	}
	return done, nil
}

type appliedJSON struct {
	Optimizers []struct {
		Name          string `json:"name"`
		State         string `json:"state"`
		TokensRemoved int64  `json:"tokens_removed"`
	} `json:"optimizers"`
}

// summarise reads one session's requests and pin. The last request that has
// signals describes the whole session, because each request carries the history.
func summarise(ctx context.Context, db *store.DB, key string) (SessionSignals, error) {
	sig := SessionSignals{On: []string{}, Acted: []string{}}
	var states string
	switch err := db.QueryRowContext(ctx, `SELECT states, pinned_at FROM session_pins WHERE session_key = ?`, key).Scan(&states, &sig.PinnedAt); {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return sig, err
	default:
		var m map[string]string
		if json.Unmarshal([]byte(states), &m) == nil {
			for n, st := range m {
				if st == config.StateOn {
					sig.On = append(sig.On, n)
				}
			}
			sort.Strings(sig.On)
		}
	}
	rows, err := db.QueryContext(ctx, `SELECT ts, signals, optimizations_applied FROM requests WHERE session_key = ? ORDER BY ts, id`, key)
	if err != nil {
		return sig, err
	}
	defer func() { _ = rows.Close() }()
	acted := map[string]bool{}
	var tss []int64
	firstAct := -1
	for rows.Next() {
		var ts int64
		var sg, ap sql.NullString
		if err := rows.Scan(&ts, &sg, &ap); err != nil {
			return sig, err
		}
		tss = append(tss, ts)
		if sg.Valid {
			var s Signals
			if json.Unmarshal([]byte(sg.String), &s) == nil {
				sig.HasTools = true
				sig.ToolCalls, sig.Repeats, sig.ToolErrors = s.ToolCalls, s.Repeats(), s.ToolErrors
				sig.Collapsed, sig.Refetches = s.Collapsed, s.Refetches
			}
		}
		if ap.Valid {
			var a appliedJSON
			if json.Unmarshal([]byte(ap.String), &a) == nil {
				for _, o := range a.Optimizers {
					if o.State == config.StateOn && o.TokensRemoved > 0 {
						acted[o.Name] = true
						if firstAct < 0 {
							firstAct = len(tss) - 1
						}
					}
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return sig, err
	}
	for n := range acted {
		sig.Acted = append(sig.Acted, n)
	}
	sort.Strings(sig.Acted)
	if firstAct >= 0 {
		after := len(tss) - 1 - firstAct
		span := time.Duration(tss[len(tss)-1]-tss[firstAct]) * time.Millisecond
		sig.Abandoned = after <= abandonRequests && span <= abandonWindow
	}
	return sig, nil
}
