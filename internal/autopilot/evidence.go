package autopilot

import (
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/technique"
)

// Where the evidence comes from.
//
// An optimizer can run in two places: the gateway (API-key traffic) and the hooks (every login,
// including subscriptions). Each keeps its shadow figures in its own table, so Autopilot reads both
// and combines them here.
//
// Combining takes the LARGER of the two figures, not the sum. When both channels are in shadow they
// look at the same waste: a noisy tool result is seen by the hook when the tool returns and again by
// the gateway when the request carries it, and neither removes it. Adding them would count the same
// tokens twice, so the larger figure is the safe one: it never claims more than either channel saw.
// (If the optimizer is already on in one channel, that channel stops recording shadow events and the
// other's figure stands on its own.)

// combine returns the evidence for one optimizer from the gateway and the hooks.
func combine(gw measure.ShadowRow, hk measure.HookShadow) (sessions int, tokens float64) {
	sessions, tokens = gw.Sessions, gw.Tokens.Value
	if hk.Sessions > sessions {
		sessions = hk.Sessions
	}
	if float64(hk.Tokens) > tokens {
		tokens = float64(hk.Tokens)
	}
	return sessions, tokens
}

// combinedRows builds the shadow rows the planner reads from both channels, for the given names.
func combinedRows(names []string, gw map[string]measure.ShadowRow, hk map[string]measure.HookShadow) map[string]measure.ShadowRow {
	out := make(map[string]measure.ShadowRow, len(names))
	for _, n := range names {
		s, t := combine(gw[n], hk[n])
		if s == 0 && t == 0 {
			continue
		}
		out[n] = measure.ShadowRow{Optimizer: n, Sessions: s, Tokens: measure.Figure{Value: t, Label: measure.Estimated}}
	}
	return out
}

// unavailableFor says why the named optimizer cannot run in the setup the traffic shows, or "" when
// it can, or when nothing has been recorded yet and it is still collecting. It uses the technique
// registry's account of where each technique runs, so there is one list of that.
func unavailableFor(name string, tr measure.Traffic) string {
	t, ok := technique.Get(name)
	if !ok || (!tr.Gateway && !tr.Hooks) {
		return ""
	}
	hasLayer := func(l technique.Layer) bool {
		for _, x := range t.Layers {
			if x == l {
				return true
			}
		}
		return false
	}
	switch {
	case t.Gateway && !tr.Gateway:
		return "it needs the gateway, and none of your recent traffic went through it"
	case !t.Gateway && hasLayer(technique.LayerToolBoundary) && !hasLayer(technique.LayerRequest) && !tr.Hooks:
		return "it runs in the plugin's hooks, and the hooks have recorded nothing recently"
	}
	return ""
}

// sessionsSeen is how many sessions count toward the baseline. The gateway's table of sessions is
// written only by the gateway, so a person with no gateway has none there; their sessions are in
// the hooks' events. The larger count is used so neither channel can hold the other back.
func sessionsSeen(gatewaySessions, hookSessions int) int {
	if hookSessions > gatewaySessions {
		return hookSessions
	}
	return gatewaySessions
}
