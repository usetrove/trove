package measure

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/store"
)

// Headless and SDK runs in Claude Code's history: how much of a week's use came from `claude -p` and Agent SDK
// sessions, the work a plan's monthly API credits can pay for instead (docs/headless.md). The split rests on the
// history line's `entrypoint`, which Claude Code writes but does not document (docs/logs.md): `cli` for interactive
// sessions and `sdk-cli` for print-mode runs were observed; any `sdk-` value is counted as headless. So the split is
// labelled observed, never verified.

// EntrypointSource is how the entrypoint field is known: observed in logs, not documented.
const EntrypointSource = "observed"

// HeadlessWindow is the span the share is read over: one weekly limit.
const HeadlessWindow = 7 * 24 * time.Hour

// IsHeadlessEntrypoint reports whether an entrypoint value marks a print-mode or SDK run.
func IsHeadlessEntrypoint(e string) bool { return strings.HasPrefix(e, "sdk-") }

// HeadlessShare is a week of Claude Code history split by entrypoint. Costs are at API list prices (estimated), from
// requests that were priced.
type HeadlessShare struct {
	From int64 `json:"from_ms"`
	To   int64 `json:"to_ms"`
	// Sessions and Requests are the headless ones (measured counts).
	Sessions int `json:"sessions"`
	Requests int `json:"requests"`
	// Entrypoints are the headless values seen, sorted.
	Entrypoints []string `json:"entrypoints"`
	// EntrypointSource is "observed": the field is not documented.
	EntrypointSource string `json:"entrypoint_source"`
	// USD is the headless requests' cost; AllUSD every request's (estimated).
	USD    Figure `json:"api_equivalent_usd"`
	AllUSD Figure `json:"all_api_equivalent_usd"`
	// Share is USD as a percent of AllUSD (estimated); nil when nothing was priced.
	Share *Figure `json:"share_percent,omitempty"`
	// Unknown counts requests whose line had no entrypoint, so they are in neither side.
	Unknown int `json:"requests_without_entrypoint"`
}

// HeadlessWeek reads the Claude Code history requests in the week ending at to.
func HeadlessWeek(ctx context.Context, db *store.DB, to time.Time) (HeadlessShare, error) {
	h := HeadlessShare{From: to.Add(-HeadlessWindow).UnixMilli(), To: to.UnixMilli(), EntrypointSource: EntrypointSource,
		USD: Figure{Label: Estimated}, AllUSD: Figure{Label: Estimated}, Entrypoints: []string{}}
	rows, err := db.QueryContext(ctx, `SELECT session_key, COALESCE(entrypoint, ''), cost_usd FROM history_requests
		WHERE source = 'claude_code' AND ts >= ? AND ts < ?`, h.From, h.To)
	if err != nil {
		return h, err
	}
	defer func() { _ = rows.Close() }()
	sessions := map[string]bool{}
	eps := map[string]bool{}
	for rows.Next() {
		var key, ep string
		var cost sql.NullFloat64
		if err := rows.Scan(&key, &ep, &cost); err != nil {
			return h, err
		}
		if cost.Valid {
			h.AllUSD.Value += cost.Float64
			h.AllUSD.N++
		}
		switch {
		case ep == "":
			h.Unknown++
		case IsHeadlessEntrypoint(ep):
			h.Requests++
			sessions[key] = true
			eps[ep] = true
			if cost.Valid {
				h.USD.Value += cost.Float64
				h.USD.N++
			} else {
				h.USD.Unpriced++
			}
		}
	}
	if err := rows.Err(); err != nil {
		return h, err
	}
	h.Sessions = len(sessions)
	for e := range eps {
		h.Entrypoints = append(h.Entrypoints, e)
	}
	sort.Strings(h.Entrypoints)
	if h.AllUSD.Value > 0 {
		h.Share = &Figure{Value: 100 * h.USD.Value / h.AllUSD.Value, Label: Estimated, N: h.USD.N}
	}
	return h, nil
}
