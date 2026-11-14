package measure

import (
	"context"

	"github.com/usetrove/trove/internal/store"
)

// The hooks and the gateway keep their shadow figures in different places. The gateway
// stores them on each request row (shadow_savings); the hooks store counts in hook_events,
// one row per decision, with the optimizer's name in the hook column. These functions read
// the hooks' side, so that a person with no gateway (every subscription user) has evidence
// too. Everything here is a count or an estimate; nothing is content.

// HookShadow is what one optimizer would have kept out of the context in the hooks, from the
// events it recorded in shadow mode. Tokens are estimated from sizes, not read from the
// provider, so they are an estimate and say nothing about whether the agent needed them.
type HookShadow struct {
	Optimizer string `json:"optimizer"`
	// Sessions is how many distinct sessions recorded at least one would-have-acted event.
	Sessions int `json:"sessions"`
	// Events is how many such events there were.
	Events int `json:"events"`
	// Tokens is the estimated tokens those events would have kept out of the context.
	Tokens int64 `json:"tokens"`
}

// hookShadowKinds are the event kinds that mean "it would have removed tokens, and did not".
// would_advise is left out: advice removes nothing by itself.
const hookShadowKinds = `('would_rewrite', 'would_block')`

// HookShadowEstimates returns, by optimizer name, what each hook optimizer would have saved in
// the window. An optimizer with no shadow events is absent.
func HookShadowEstimates(ctx context.Context, db *store.DB, w Window) (map[string]HookShadow, error) {
	rows, err := db.QueryContext(ctx, `SELECT hook, COUNT(DISTINCT session_key), COUNT(*), COALESCE(SUM(tokens), 0)
		FROM hook_events WHERE ts >= ? AND ts < ? AND kind IN `+hookShadowKinds+` GROUP BY hook`,
		w.From.UnixMilli(), w.To.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]HookShadow{}
	for rows.Next() {
		var h HookShadow
		if err := rows.Scan(&h.Optimizer, &h.Sessions, &h.Events, &h.Tokens); err != nil {
			return nil, err
		}
		out[h.Optimizer] = h
	}
	return out, rows.Err()
}

// HookSessions is how many distinct sessions the hooks have recorded anything for. The baseline
// counts sessions, and the gateway's own table of sessions is written only by the gateway, so
// without this a person with no gateway would stay in the baseline forever.
func HookSessions(ctx context.Context, db *store.DB) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT session_key) FROM hook_events`).Scan(&n)
	return n, err
}

// Traffic says which channels recorded anything in a window.
type Traffic struct {
	// Gateway is true when any request went through the gateway.
	Gateway bool `json:"gateway"`
	// Hooks is true when the hooks recorded any event.
	Hooks bool `json:"hooks"`
}

// TrafficSeen reports which channels were used in the window, so a technique that needs a channel
// the user is not using can be called unavailable and not left "collecting" for ever.
func TrafficSeen(ctx context.Context, db *store.DB, w Window) (Traffic, error) {
	var t Traffic
	var g, h int
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM requests WHERE ts >= ? AND ts < ?)`,
		w.From.UnixMilli(), w.To.UnixMilli()).Scan(&g); err != nil {
		return t, err
	}
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hook_events WHERE ts >= ? AND ts < ?)`,
		w.From.UnixMilli(), w.To.UnixMilli()).Scan(&h); err != nil {
		return t, err
	}
	t.Gateway, t.Hooks = g != 0, h != 0
	return t, nil
}
