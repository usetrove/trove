package measure

import (
	"context"

	"github.com/usetrove/trove/internal/store"
)

// History is usage found in agents' own session logs (internal/logs). It is
// kept apart from gateway data: gateway rows are what Trove saw on the wire,
// history rows are what the agent wrote down afterwards, so history figures are
// always estimated.
//
// No double counting: a Claude Code session that went through the gateway is in
// both places. A history request is counted only when its session has no
// gateway request, so the gateway's measured rows win. The match is on the
// hashed session key (the idle-gap suffix "~N" is ignored).
//
// Codex cannot be matched that way: the gateway derives its session keys from
// the prompt, history from the session id. For Codex, history from the first
// gateway Codex request onward is assumed to be seen by the gateway and left
// out. If the gateway was stopped and Codex kept running, that history is
// missed, so Codex history is a lower bound.
const historyScope = `FROM history_requests h WHERE h.ts >= ? AND h.ts < ?
	AND h.session_key NOT IN (
		SELECT CASE WHEN instr(session_key, '~') > 0 THEN substr(session_key, 1, instr(session_key, '~') - 1) ELSE session_key END
		FROM requests WHERE session_key LIKE 'c-%')
	AND NOT (h.source = 'codex' AND h.ts >= ` + codexGatewayStart + `)`

// codexGatewayStart is the time of the first Codex request the gateway recorded,
// or the largest possible time when there is none.
const codexGatewayStart = `COALESCE((SELECT MIN(ts) FROM requests WHERE agent = 'codex'), 9223372036854775807)`

// HistoryRow is history usage for one model under one plan.
type HistoryRow struct {
	Plan string `json:"plan"` // api_key, subscription or unknown
	TotalsRow
}

// HistoryTotals sums history usage in w by model and plan, leaving out
// sessions the gateway also saw.
func HistoryTotals(ctx context.Context, db *store.DB, w Window) ([]HistoryRow, error) {
	rows, err := db.QueryContext(ctx, `SELECT h.model, h.plan, COUNT(*), SUM(h.input_tokens),
		SUM(h.cache_write_5m + h.cache_write_1h + h.cache_write_other), SUM(h.cache_read), SUM(h.output_tokens),
		COALESCE(SUM(h.cost_usd), 0), SUM(CASE WHEN h.cost_usd IS NULL THEN 1 ELSE 0 END) `+historyScope+`
		GROUP BY h.model, h.plan ORDER BY h.model, h.plan`, w.From.UnixMilli(), w.To.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []HistoryRow{}
	for rows.Next() {
		var r HistoryRow
		var cost float64
		var unpriced int
		if err := rows.Scan(&r.Key, &r.Plan, &r.Requests, &r.InputTokens, &r.CacheWriteTokens, &r.CacheReadTokens, &r.OutputTokens, &cost, &unpriced); err != nil {
			return nil, err
		}
		r.CostUSD = Figure{Value: roundUSD(cost), Label: Estimated, N: r.Requests - unpriced, Unpriced: unpriced}
		out = append(out, r)
	}
	return out, rows.Err()
}

// HistoryOverlap counts sessions with history in w that were left out because
// the gateway also recorded them.
func HistoryOverlap(ctx context.Context, db *store.DB, w Window) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT h.session_key) FROM history_requests h
		WHERE h.ts >= ? AND h.ts < ? AND (h.session_key IN (
		SELECT CASE WHEN instr(session_key, '~') > 0 THEN substr(session_key, 1, instr(session_key, '~') - 1) ELSE session_key END
		FROM requests WHERE session_key LIKE 'c-%')
		OR (h.source = 'codex' AND h.ts >= `+codexGatewayStart+`))`, w.From.UnixMilli(), w.To.UnixMilli()).Scan(&n)
	return n, err
}

// HistorySessions counts distinct sessions in the history totals.
func HistorySessions(ctx context.Context, db *store.DB, w Window) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT h.session_key) `+historyScope, w.From.UnixMilli(), w.To.UnixMilli()).Scan(&n)
	return n, err
}

// HistoryWasteByCategory is WasteByCategory over history findings.
func HistoryWasteByCategory(ctx context.Context, db *store.DB, w Window) ([]WasteRow, error) {
	return wasteByCategory(ctx, db, `SELECT h.findings `+historyScope+` AND h.findings IS NOT NULL`, w.From.UnixMilli(), w.To.UnixMilli())
}

// HistoryWasteTotal is WasteTotalIn over history findings.
func HistoryWasteTotal(ctx context.Context, db *store.DB, w Window) (WasteTotal, error) {
	return wasteTotal(ctx, db, `SELECT h.findings `+historyScope+` AND h.findings IS NOT NULL`, w.From.UnixMilli(), w.To.UnixMilli())
}

// HistoryFiles describes what has been read, over all time.
type HistoryFiles struct {
	Files         int   `json:"files"`
	SkippedLines  int64 `json:"skipped_lines"`
	UnknownFormat int   `json:"-"`
	Latest        int64 `json:"-"` // unix ms of the last scan
}

// HistoryFileStats reports how many history files have been read and how many
// of their lines could not be parsed.
func HistoryFileStats(ctx context.Context, db *store.DB) (HistoryFiles, error) {
	var h HistoryFiles
	err := db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(skipped_lines), 0), COALESCE(MAX(updated_at), 0) FROM history_files`).
		Scan(&h.Files, &h.SkippedLines, &h.Latest)
	return h, err
}
