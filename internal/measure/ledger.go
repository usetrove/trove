package measure

import (
	"context"
	"database/sql"

	"github.com/usetrove/trove/internal/analyze"
	"github.com/usetrove/trove/internal/store"
)

// HistoryLedger loads the Claude Code history requests the ledgers need: every request of every
// session that has a request in w (and was not also seen by the gateway), including requests before
// w.From, so a window's first request is compared with its real predecessor. Callers count only
// entries inside w. Numbers, hashes and codes only.
func HistoryLedger(ctx context.Context, db *store.DB, w Window) ([]analyze.LedgerRequest, error) {
	rows, err := db.QueryContext(ctx, `SELECT r.session_key, COALESCE(r.agent_key, ''), r.sidechain, r.ts, r.model, r.plan,
		COALESCE(r.agent_version, ''), COALESCE(r.turn_effort, r.effort, ''), COALESCE(r.fast_mode, 0),
		COALESCE(r.phase, ''), COALESCE(r.compacted, 0),
		r.input_tokens, r.cache_write_5m, r.cache_write_1h, r.cache_write_other, r.cache_read, r.output_tokens,
		r.thinking_tokens, r.text_bytes,
		COALESCE((SELECT SUM(t.input_bytes) FROM history_tool_calls t WHERE t.request_key = r.request_key), 0)
		FROM history_requests r
		WHERE r.source = 'claude_code' AND r.session_key IN (SELECT DISTINCT h.session_key `+historyScope+` AND h.source = 'claude_code')
		ORDER BY r.session_key, r.ts, r.request_key`, w.From.UnixMilli(), w.To.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []analyze.LedgerRequest
	for rows.Next() {
		var r analyze.LedgerRequest
		var sidechain, compacted int
		var fast int
		var thinking, text sql.NullInt64
		if err := rows.Scan(&r.SessionKey, &r.AgentKey, &sidechain, &r.TS, &r.Model, &r.Plan,
			&r.AgentVersion, &r.Effort, &fast, &r.Phase, &compacted,
			&r.Input, &r.CacheWrite5m, &r.CacheWrite1h, &r.CacheWriteNA, &r.CacheRead, &r.Output,
			&thinking, &text, &r.ToolInputBytes); err != nil {
			return nil, err
		}
		r.Sidechain, r.Compacted = sidechain != 0, compacted != 0
		if fast != 0 {
			r.ServiceTier = "fast"
		}
		if thinking.Valid {
			v := thinking.Int64
			r.Thinking = &v
		}
		r.TextBytes, r.HasSizes = text.Int64, text.Valid
		out = append(out, r)
	}
	return out, rows.Err()
}
