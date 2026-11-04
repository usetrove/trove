// Package report turns the measurement layer's numbers into what users read:
// the report, budget pacing, receipts, the status line and the share card.
//
// It computes nothing of its own that the measurement layer already computes.
// Every figure is a measure.Figure carrying its "measured" or "estimated" label,
// and the text and JSON outputs are rendered from the same Report value.
package report

import (
	"time"

	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/optimize/suggest"
)

// Report states.
const (
	StateEmpty = "empty" // nothing has ever been recorded
	StateQuiet = "quiet" // data exists, but none in this period
	StateSmall = "small" // fewer than SmallSample requests: numbers are rough
	StateOK    = "ok"
)

// SmallSample is the request count below which a report says its numbers are rough.
const SmallSample = 20

// Report is everything `trove report` shows.
type Report struct {
	Period          PeriodInfo       `json:"period"`
	State           string           `json:"state"`
	Requests        int              `json:"requests"`
	Headline        Headline         `json:"headline"`
	Budget          *Pacing          `json:"budget,omitempty"`
	Spend           Spend            `json:"spend"`
	Leaks           []Leak           `json:"leaks"`
	Optimizers      Optimizers       `json:"optimizers"`
	Recommendations []Recommendation `json:"recommendations"`
	// Fixes are the config fixes from `trove fix` that are still in place, with
	// what the data shows about each. Empty when none was applied.
	Fixes []measure.FixEffect `json:"fixes,omitempty"`
	// Notes are plain-English caveats and next steps (for example the empty state).
	Notes []string `json:"notes,omitempty"`
	// History is the part of the figures that came from agents' local session
	// logs. Nil when there is none.
	History *History `json:"history,omitempty"`
	// Tools is how the Trove MCP tools are doing. Nil when the server is not
	// registered and no agent has used them.
	Tools *Tools `json:"trove_tools,omitempty"`
	// Hooks is what the agent hooks did in this period (blocked and advised
	// calls, re-reads that followed a block). Empty when no hook has recorded
	// anything.
	Hooks []measure.HookStats `json:"hooks,omitempty"`
	// Upstreams is capacity and cost per OpenAI-compatible server (self-hosted or
	// open-weight). Empty when no such traffic was recorded.
	Upstreams []measure.UpstreamRow `json:"upstreams,omitempty"`
}

// Tools reports on the Trove MCP server: what its definitions cost, whether
// agents use the tools, and whether trimming is too aggressive.
type Tools struct {
	Registered bool `json:"registered"`
	// SchemaTokens is what the tool definitions add to every request. Estimated.
	SchemaTokens *measure.Figure `json:"schema_tokens,omitempty"`
	// Adoption holds use versus built-in tools (estimated, from session logs) and
	// the fetch-back rate (measured, from gateway requests).
	Adoption measure.Adoption `json:"adoption"`
}

// PeriodInfo describes the span covered.
type PeriodInfo struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	From  string `json:"from"` // YYYY-MM-DD, local
	To    string `json:"to"`   // YYYY-MM-DD, local, inclusive
}

// Headline is the top of the report.
type Headline struct {
	// Spend is provider-reported usage priced at list prices. Estimated: it is
	// not an invoice. Unpriced counts requests left out.
	Spend measure.Figure `json:"spend_usd"`
	// Tokens is provider-reported usage (measured).
	Tokens measure.Figure `json:"tokens"`
	// Waste is a lower bound: each request contributes only its largest finding,
	// so overlapping findings are not double counted. Estimated.
	Waste measure.Figure `json:"waste_usd"`
	// WastePercent is Waste as a share of Spend (estimated). Value is a percent.
	WastePercent measure.Figure `json:"waste_percent"`
	// Savings is what optimizers that were on saved (estimated).
	Savings measure.Figure `json:"savings_usd"`
	// APIEquivalent is the API list-price value of usage that is not paid per
	// token (a subscription, or a plan Trove could not tell). Estimated, and not
	// a bill. Nil when there is none.
	APIEquivalent *measure.Figure `json:"api_equivalent_usd,omitempty"`
	// MeasuredNote says what, if anything, has been measured about savings.
	MeasuredNote string `json:"measured_note"`
}

// Spend splits spend by agent and by model.
type Spend struct {
	ByAgent []measure.TotalsRow `json:"by_agent"`
	ByModel []measure.TotalsRow `json:"by_model"`
	// ByKind splits gateway spend into interactive, automated and unknown sessions,
	// from what the user declared with X-Trove-Kind. Empty until at least one request
	// declares a kind, so reports without it are unchanged. Automated is shown on its
	// own because that is where included API credits (Max, Ultra, Team) may apply;
	// the label does not say whether credits were actually used.
	ByKind []measure.TotalsRow `json:"by_kind,omitempty"`
}

// Leak is one kind of waste with the way to act on it.
type Leak struct {
	Category string         `json:"category"`
	Title    string         `json:"title"`
	USD      measure.Figure `json:"usd"`
	Tokens   measure.Figure `json:"tokens"`
	// Command is what to run or add to act on it. Empty when there is no command;
	// Advice then says what to do.
	Command string `json:"command,omitempty"`
	Advice  string `json:"advice"`
}

// Optimizers is the optimizer section.
type Optimizers struct {
	Baseline   *Baseline        `json:"baseline,omitempty"`
	KillSwitch bool             `json:"kill_switch"`
	Rows       []OptimizerRow   `json:"rows"`
	Guardrails []GuardrailEvent `json:"guardrail_events"`
	// NoRegression is the one claim Trove makes about quality, or "".
	Quality string `json:"quality,omitempty"`
}

// Baseline is progress through the install baseline.
type Baseline struct {
	Active         bool    `json:"active"`
	DaysElapsed    float64 `json:"days_elapsed"`
	DaysNeeded     int     `json:"days_needed"`
	SessionsSeen   int     `json:"sessions_seen"`
	SessionsNeeded int     `json:"sessions_needed"`
}

// OptimizerRow is one optimizer.
type OptimizerRow struct {
	Name       string `json:"name"`
	Configured string `json:"configured"`
	Effective  string `json:"effective"`
	// RolledBack is the guardrail reason when Trove set the optimizer back to shadow.
	RolledBack string `json:"rolled_back,omitempty"`
	// Saved is what the optimizer saved while on in the period (estimated). Nil
	// when it did not act.
	Saved *measure.Figure `json:"saved_usd,omitempty"`
	// WouldSave is the shadow-mode estimate when the optimizer is not on.
	WouldSave *measure.Figure `json:"would_save_usd,omitempty"`
	// Measured compares prompt tokens per request of on and off sessions.
	Measured *Measured `json:"measured,omitempty"`
	// Enable is the config line that turns the optimizer on, when it is worth offering.
	Enable string `json:"enable,omitempty"`
	Note   string `json:"note,omitempty"`
}

// Measured is the on-versus-off comparison (label: measured).
type Measured struct {
	Label    measure.Label `json:"label"`
	Reduced  bool          `json:"reduced"`
	Verdict  string        `json:"verdict"`
	NOn      int           `json:"n_on_sessions"`
	NOff     int           `json:"n_off_sessions"`
	OnMean   float64       `json:"on_prompt_tokens_per_request"`
	OffMean  float64       `json:"off_prompt_tokens_per_request"`
	Text     string        `json:"text"`
	Percent  float64       `json:"percent_fewer,omitempty"` // only when Reduced
	Enough   bool          `json:"enough_data"`
	Optimize string        `json:"optimizer"`
}

// GuardrailEvent is a rollback Trove made.
type GuardrailEvent struct {
	When      string `json:"when"` // YYYY-MM-DD, local
	Optimizer string `json:"optimizer"`
	Rule      string `json:"rule"`
	Reason    string `json:"reason"`
	Cleared   bool   `json:"cleared"`
}

// Recommendation is a suggestion Trove never applies by itself.
type Recommendation struct {
	suggest.Suggestion
}

// Pacing is the budget view. Every dollar figure is estimated.
type Pacing struct {
	Period         string         `json:"period"` // daily, weekly or monthly
	Name           string         `json:"name"`   // today, week or month
	From           string         `json:"from"`
	To             string         `json:"to"` // last day, inclusive
	CapUSD         float64        `json:"cap_usd"`
	Spent          measure.Figure `json:"spent_usd"`
	PercentUsed    float64        `json:"percent_used"`
	PercentElapsed float64        `json:"percent_elapsed"`
	// Status is under, on_pace, ahead, early, near_cap or over.
	Status           string          `json:"status"`
	ProjectedTotal   *measure.Figure `json:"projected_total_usd,omitempty"`
	ProjectedCapDate string          `json:"projected_cap_date,omitempty"` // YYYY-MM-DD
	// CapReached is true once spending has reached the cap.
	CapReached bool `json:"cap_reached"`
	// Lever names what would change the pace most: the biggest leak in dollars.
	Lever string `json:"lever,omitempty"`
}

func day(t time.Time) string { return t.Format(time.DateOnly) }
