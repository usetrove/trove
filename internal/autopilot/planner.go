package autopilot

import (
	"sort"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/optimize"
)

// Planner verdicts.
const (
	VerdictOn              = "on"
	VerdictWouldTurnOn     = "would_turn_on"
	VerdictNeedsYourChoice = "needs_your_choice"
	VerdictCollecting      = "collecting"
	VerdictUnavailable     = "unavailable"
	VerdictBlocked         = "blocked"
	VerdictNotWorthIt      = "not_worth_it"
)

// Tier says what kind of change an optimizer makes, from docs/optimize.md.
func Tier(name string) string {
	if name == config.OptimizerOutputTrimmer {
		return "recoverable" // shortens output; the full text can be fetched back
	}
	return "lossless"
}

// Plan is the shadow planner. For every optimizer it says what Autopilot would do
// with the evidence so far, including optimizers it may never turn on by itself.
// It is advice only: it reads numbers and changes no setting and no request. It
// is the second step of the build order in docs/autopilot.md; an active planner
// needs a session-level A/B showing it beats the fixed list after its own
// overhead, and none has been run.
//
// rows are the shadow estimates (each optimizer valued on its own, so they
// overlap and must not be added together). entries are Autopilot's current states.
// blocked holds, by optimizer name, the technique registry's reason that something already
// on cannot run beside it.
func (p Policy) Plan(rows map[string]measure.ShadowRow, entries map[string]config.AutopilotEntry, baselineDone bool, blocked, unavailable map[string]string) []config.AutopilotPlanItem {
	var out []config.AutopilotPlanItem
	for _, name := range optimize.PipelineNames {
		row := rows[name]
		net := p.NetOf(name, row.Sessions, row.Events, row.Tokens.Value)
		it := config.AutopilotPlanItem{Optimizer: name, Tier: Tier(name), Sessions: row.Sessions, NetTokens: net}
		ok, why := p.ready(Evidence{BaselineDone: baselineDone, ShadowSessions: row.Sessions, NetTokens: net})
		switch {
		case entries[name].State == config.AutopilotOn:
			it.Verdict, it.Reason = VerdictOn, "Autopilot turned it on"
		case entries[name].State == config.AutopilotProbation:
			it.Verdict, it.Reason = VerdictCollecting, "on trial for about half of new sessions: "+entries[name].Reason
		case blocked[name] != "":
			it.Verdict, it.Reason = VerdictBlocked, blocked[name]
		case unavailable[name] != "":
			it.Verdict, it.Reason = VerdictUnavailable, unavailable[name]
		case NoSavingEstimate(name) && !p.NeedsProbation(name):
			it.Verdict, it.Reason = VerdictNeedsYourChoice, noEstimateReason
		case ok && p.NeedsProbation(name):
			it.Verdict, it.Reason = VerdictWouldTurnOn, "enough shadow evidence; Autopilot starts a trial against sessions without it at the next check"
		case ok && p.IsEligible(name):
			it.Verdict, it.Reason = VerdictWouldTurnOn, "enough shadow evidence; Autopilot turns it on at the next check"
		case ok:
			it.Verdict = VerdictNeedsYourChoice
			it.Reason = "the evidence looks good, but Autopilot never turns this one on by itself; you can set it in config.toml"
		case row.Sessions >= p.MinSessions && net <= 0:
			it.Verdict, it.Reason = VerdictNotWorthIt, "estimated net saving is not positive after its own cost"
		default:
			it.Verdict, it.Reason = VerdictCollecting, why
		}
		out = append(out, it)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Optimizer < out[j].Optimizer })
	return out
}
