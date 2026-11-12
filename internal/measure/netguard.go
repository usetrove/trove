package measure

import "fmt"

// NetGuard decides whether an optimizer should be switched off for later sessions.
// It is a pure rule over a window of measured numbers. A caller applies the
// decision only at a session boundary (CLAUDE.md rule 5: optimizers are switched
// on or off between sessions, never mid-session).
//
// The rule has two triggers, and both need enough requests to be trusted:
//   - net saving is negative: tokens the optimizer removed are fewer than the
//     tokens it costs (its declared overhead, counted per request it ran in);
//   - fail-open rate is high: the share of its requests that fell back to the
//     original body, because it timed out or errored, is above the limit.
type NetGuard struct {
	// MinRequests is the number of requests the optimizer must have run in before
	// either trigger can fire. Below it the verdict is "not enough data".
	MinRequests int
	// MaxFailOpenRate is the largest share of requests allowed to fail open (0.10 = 10%).
	MaxFailOpenRate float64
}

// DefaultNetGuard is the shipped policy. Changing it changes when optimizers are
// switched off, so it is a policy change, not a tuning detail.
var DefaultNetGuard = NetGuard{MinRequests: 20, MaxFailOpenRate: 0.10}

// NetWindow is what the rule sees for one optimizer over a window of sessions.
type NetWindow struct {
	Requests       int   // requests the optimizer ran in (its pipeline was active)
	TokensRemoved  int64 // estimated tokens it removed in those requests
	OverheadTokens int64 // tokens it cost (declared overhead, per request)
	FailOpen       int   // of those requests, how many failed open
}

// NetVerdict is the rule's answer for one optimizer.
type NetVerdict struct {
	// Disable is true when a trigger fired on enough data.
	Disable bool `json:"disable"`
	// Enough is false when the window has too few requests to judge.
	Enough       bool    `json:"enough_data"`
	NetTokens    int64   `json:"net_tokens"`
	FailOpenRate float64 `json:"fail_open_rate"`
	// Reason is plain English for the report. Empty when the optimizer stays on.
	Reason string `json:"reason,omitempty"`
}

// Evaluate applies the rule to one optimizer's window.
func (g NetGuard) Evaluate(w NetWindow) NetVerdict {
	v := NetVerdict{NetTokens: w.TokensRemoved - w.OverheadTokens}
	if w.Requests > 0 {
		v.FailOpenRate = float64(w.FailOpen) / float64(w.Requests)
	}
	if w.Requests < g.MinRequests {
		return v // Enough is false: keep it on, report that there was not enough data
	}
	v.Enough = true
	switch {
	case v.NetTokens < 0:
		v.Disable = true
		v.Reason = fmt.Sprintf("net saving is negative: it removed %d tokens and cost %d in %d requests", w.TokensRemoved, w.OverheadTokens, w.Requests)
	case v.FailOpenRate > g.MaxFailOpenRate:
		v.Disable = true
		v.Reason = fmt.Sprintf("%.0f%% of its requests fell back to the original (limit %.0f%%), so it is not reliably saving anything",
			v.FailOpenRate*100, g.MaxFailOpenRate*100)
	}
	return v
}
