package measure

import (
	"context"
	"fmt"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/store"
)

// RuleHookReread is the guardrail for hook_repeat_read: of the reads it blocked,
// how many did the agent retry anyway. A high rate means the block is not helping
// (the agent needed the file again), so the hook goes back to shadow.
const RuleHookReread = "hook_reread"

// hookWindow is how far back the guardrail looks, so an old bad stretch does not
// count against a hook after the cause was fixed.
const hookWindow = 14 * 24 * time.Hour

// HookStats counts what one hook did. Everything here is a count the hook saw
// itself ("measured") except TokensSaved, which is derived from file sizes
// ("estimated").
type HookStats struct {
	Hook         string `json:"hook"`
	Blocked      int    `json:"blocked"`
	WouldBlock   int    `json:"would_block"`
	Advised      int    `json:"advised"`
	WouldAdvise  int    `json:"would_advise"`
	RereadAnyway int    `json:"reread_anyway"`
	Sessions     int    `json:"sessions"`
	// TokensSaved is the estimated tokens kept out of the context by blocked
	// reads that were not retried. Blocks the agent retried save nothing.
	TokensSaved Figure `json:"tokens_saved"`
	// RereadRate is retried blocks as a percent of blocked reads (measured); nil
	// until at least one read was blocked.
	RereadRate *Figure `json:"reread_rate_percent,omitempty"`
}

// HookTotals summarises hook_events since the given time, one entry per hook
// that has any rows, ordered by name.
func HookTotals(ctx context.Context, db *store.DB, since time.Time) ([]HookStats, error) {
	rows, err := db.QueryContext(ctx, `SELECT hook,
		SUM(kind = 'blocked'), SUM(kind = 'would_block'), SUM(kind = 'advised'), SUM(kind = 'would_advise'), SUM(kind = 'reread_anyway'),
		COUNT(DISTINCT session_key),
		COALESCE(SUM(CASE WHEN kind = 'blocked' THEN tokens END), 0),
		COALESCE(SUM(CASE WHEN kind = 'reread_anyway' THEN tokens END), 0)
		FROM hook_events WHERE ts >= ? GROUP BY hook ORDER BY hook`, since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []HookStats
	for rows.Next() {
		var s HookStats
		var blockedTok, rereadTok int64
		if err := rows.Scan(&s.Hook, &s.Blocked, &s.WouldBlock, &s.Advised, &s.WouldAdvise, &s.RereadAnyway, &s.Sessions, &blockedTok, &rereadTok); err != nil {
			return nil, err
		}
		saved := blockedTok - rereadTok // a retried block put the file back, so it saved nothing
		if saved < 0 {
			saved = 0
		}
		s.TokensSaved = Figure{Value: float64(saved), Label: Estimated, N: s.Blocked}
		if s.Blocked > 0 {
			s.RereadRate = &Figure{Value: 100 * float64(s.RereadAnyway) / float64(s.Blocked), Label: Measured, N: s.Blocked}
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// EvaluateHooks is the guardrail pass for hook-based optimizers. It rolls
// hook_repeat_read back to shadow when, since the last time the user cleared a
// rollback, at least m.MinSessions reads were blocked and the share the agent
// retried anyway reached m.RefetchRatePercent. It is safe to call repeatedly and
// returns the rollbacks it recorded.
func EvaluateHooks(ctx context.Context, db *store.DB, m config.Measure, now time.Time) ([]Event, error) {
	name := config.OptimizerHookRepeatRead
	active, err := Overrides(ctx, db)
	if err != nil {
		return nil, err
	}
	if _, rolled := active[name]; rolled {
		return nil, nil
	}
	cleared, err := lastCleared(ctx, db, name)
	if err != nil {
		return nil, err
	}
	since := now.Add(-hookWindow).UnixMilli()
	if cleared > since {
		since = cleared
	}
	var blocked, reread, sessions int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(SUM(kind = 'blocked'), 0), COALESCE(SUM(kind = 'reread_anyway'), 0),
		COUNT(DISTINCT session_key) FROM hook_events WHERE hook = ? AND ts >= ?`, name, since).Scan(&blocked, &reread, &sessions); err != nil {
		return nil, err
	}
	if blocked < m.MinSessions || blocked == 0 {
		return nil, nil
	}
	rate := float64(reread) / float64(blocked)
	threshold := float64(m.RefetchRatePercent) / 100
	if rate < threshold {
		return nil, nil
	}
	ev := Event{At: now.UTC(), Optimizer: name, Rule: RuleHookReread, Observed: rate, Threshold: threshold,
		SampleOn: blocked, Reference: RefNone,
		Reason: fmt.Sprintf("Trove set %s back to shadow because the agent read the same file again after %d of %d blocked reads (%.0f%%, limit %d%%). This is measured from the hook's own counts; re-enable it deliberately if you disagree.",
			name, reread, blocked, rate*100, m.RefetchRatePercent)}
	if err := recordRollback(ctx, db, ev); err != nil {
		return nil, err
	}
	return []Event{ev}, nil
}
