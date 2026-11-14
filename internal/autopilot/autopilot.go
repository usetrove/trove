// Package autopilot moves optimizers through off -> shadow -> candidate -> on, and
// back to shadow, from evidence Trove already measures. See docs/autopilot.md.
//
// Rules it keeps (CLAUDE.md): changes reach only sessions that start afterwards,
// because the engine pins each session's states when it begins (rule 5); only
// optimizers the policy names are ever promoted, and only after shadow evidence
// (rule 6); a user's own setting always wins; any error leaves things as they
// are, which means nothing newly turned on (rule 4).
//
// This file is the pure part: no files, no database, no clock of its own.
package autopilot

import (
	"fmt"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/technique"
)

// Policy is the rule set. DefaultPolicy's numbers are conservative placeholders
// until a benchmark sets them; they are labeled unvalidated wherever shown.
type Policy struct {
	// Name is the policy version the user consented to ("lossless-v1" or "lossless-v2").
	Name string
	// Eligible lists the optimizers Autopilot may promote. Only lossless ones that
	// leave the cached prefix alone are listed on day one.
	Eligible []string
	// MinSessions is how many sessions must have run the optimizer in shadow.
	MinSessions int
	// MinNetTokens is the estimated net tokens it must have removed in the window.
	MinNetTokens int64
	// LookbackDays is the evidence window.
	LookbackDays int
	// CooldownDays is how long a rolled-back optimizer waits before another try.
	CooldownDays int
	// MaxAttempts is how many times one optimizer may be promoted, or put on probation. After that a
	// rollback is left for the user to decide.
	MaxAttempts int
	// Probation lists the recoverable optimizers that may be kept only after a session-level A/B
	// comparison (docs/autopilot-probation.md). In list order, one at a time.
	Probation []string
	// ProbationMinSessionsPerArm is how many finished sessions each side of the comparison needs
	// before it can promote. A placeholder until a benchmark sets it.
	ProbationMinSessionsPerArm int
	// ProbationMaxDays is how long a probation may run without a clear result before the optimizer
	// goes back to shadow. A placeholder until a benchmark sets it.
	ProbationMaxDays int
}

// DefaultPolicy is the shipped "lossless-v1" policy.
func DefaultPolicy() Policy {
	return Policy{
		Name:         config.AutopilotPolicy,
		Eligible:     []string{"noise_normalizer", "duplicate_result_collapser"},
		MinSessions:  10,
		MinNetTokens: 2000,
		LookbackDays: 30,
		CooldownDays: 7,
		MaxAttempts:  2,
	}
}

// PolicyV2 is "lossless-v2": the v1 list, plus recoverable optimizers that must pass probation. They are
// listed here but cannot be promoted yet: reading the comparison's result is a later step, so a probation
// can only end by timing out or being rolled back. Nothing offers this policy to a user yet.
func PolicyV2() Policy {
	p := DefaultPolicy()
	p.Name = config.AutopilotPolicyV2
	p.Probation = []string{"hook_repeat_read", "hook_quiet_commands"}
	p.ProbationMinSessionsPerArm = 20
	p.ProbationMaxDays = 28
	return p
}

// PolicyFor returns the policy a consent record names, or DefaultPolicy for one this version does not know
// (which only ever shrinks what Autopilot may do).
func PolicyFor(name string) Policy {
	if name == config.AutopilotPolicyV2 {
		return PolicyV2()
	}
	return DefaultPolicy()
}

// NeedsProbation reports whether the named optimizer must pass probation before it is kept.
func (p Policy) NeedsProbation(name string) bool {
	for _, e := range p.Probation {
		if e == name {
			return true
		}
	}
	return false
}

// Names returns every optimizer the policy may move: the lossless list, then the probation list.
func (p Policy) Names() []string {
	return append(append([]string{}, p.Eligible...), p.Probation...)
}

// IsEligible reports whether the policy may promote the named optimizer.
func (p Policy) IsEligible(name string) bool {
	for _, e := range p.Eligible {
		if e == name {
			return true
		}
	}
	return false
}

// Evidence is what Decide needs to know about one optimizer. All of it is a
// count or an estimate; none of it is content.
type Evidence struct {
	// BaselineDone is true once the install's baseline period has passed.
	BaselineDone bool
	// ShadowSessions is how many sessions in the window ran it in shadow.
	ShadowSessions int
	// NetTokens is estimated tokens removed minus declared overhead, in the window.
	NetTokens int64
	// RolledBack is true while a guardrail rollback to shadow is in effect.
	RolledBack bool
	// Blocked is a plain-English reason the technique registry gives when something that is
	// already on cannot run beside this optimizer. Empty means nothing is in the way.
	Blocked string
	// Unavailable is a plain-English reason this optimizer cannot run in the user's setup (it
	// needs a channel, the gateway or the hooks, that recorded nothing while the other did). Empty
	// means it can run, or nothing has been seen yet so it is still collecting.
	Unavailable string
	// NeedsProbation is true when the policy requires a session-level comparison before this optimizer is kept.
	NeedsProbation bool
	// NoSavingEstimate is true when shadow mode cannot put a number on the saving (see the registry's
	// NoSavingEstimate). The net-token threshold is skipped for it, and only probation can judge it.
	NoSavingEstimate bool
	// CanProbation says whether a probation can run for this user now. Empty means it can; otherwise it is
	// the plain-English reason it cannot (A/B comparison is off, or there is no gateway to assign arms).
	CanProbation string
	// OtherInProbation names another optimizer already in probation, or is empty. Only one runs at a time.
	OtherInProbation string
	// Probation is the comparison's result so far, while in probation. The zero value means no result yet.
	Probation ProbationEvidence
}

// ProbationEvidence is what the session-level comparison shows so far. Status is the experiment package's
// status string ("saves", "costs_more", "worse_outcomes", "no_clear_effect", "not_enough_data"), or empty
// when no comparison has been read.
type ProbationEvidence struct {
	Status    string
	NOn, NOff int
}

// Decision is Decide's answer.
type Decision struct {
	Entry config.AutopilotEntry
	// ClearRollback asks the caller to lift the guardrail rollback so the
	// optimizer can be tried again. Set only after the cool-down.
	ClearRollback bool
}

func (p Policy) ready(ev Evidence) (bool, string) {
	switch {
	case ev.Blocked != "":
		return false, "held back: " + ev.Blocked
	case ev.Unavailable != "":
		return false, "not available in your setup: " + ev.Unavailable
	case !ev.BaselineDone:
		return false, "waiting for the baseline period to finish"
	case ev.ShadowSessions < p.MinSessions:
		return false, fmt.Sprintf("%d of %d shadow sessions so far", ev.ShadowSessions, p.MinSessions)
	case !ev.NoSavingEstimate && ev.NetTokens < p.MinNetTokens:
		return false, fmt.Sprintf("estimated net saving is %d tokens, below the %d needed", ev.NetTokens, p.MinNetTokens)
	}
	return true, ""
}

// Decide is the state machine for one eligible optimizer. It is a pure function
// of its inputs. The caller applies the result, and states reach the engine only
// through pins taken when a session starts.
func (p Policy) Decide(cur config.AutopilotEntry, ev Evidence, now time.Time) Decision {
	next := cur
	set := func(state, reason string) {
		next.State, next.Reason, next.SinceMS = state, reason, now.UnixMilli()
	}
	switch cur.State {
	case "", config.AutopilotOff:
		set(config.AutopilotShadow, "collecting shadow estimates")
	case config.AutopilotShadow:
		if ok, why := p.ready(ev); ok {
			set(config.AutopilotCandidate, "evidence looks good; confirming on the next check")
		} else {
			next.Reason = why
		}
	case config.AutopilotCandidate:
		ok, why := p.ready(ev)
		switch {
		case !ok:
			set(config.AutopilotShadow, "evidence did not hold: "+why)
		case !ev.NeedsProbation:
			set(config.AutopilotOn, "turned on from shadow evidence (estimated)")
			next.Attempts = cur.Attempts + 1
		case cur.Attempts >= p.MaxAttempts:
			set(config.AutopilotShadow, "already tried the maximum number of times; leaving it for you to decide")
		case ev.CanProbation != "":
			set(config.AutopilotShadow, "needs a trial against sessions without it, which cannot run: "+ev.CanProbation)
		case ev.OtherInProbation != "":
			next.Reason = "waiting for " + ev.OtherInProbation + " to finish its trial"
		default:
			set(config.AutopilotProbation, "on for about half of new sessions, chosen at random, to compare with the rest")
			next.Attempts = cur.Attempts + 1
		}
	case config.AutopilotProbation:
		return p.decideProbation(cur, ev, now)
	case config.AutopilotOn:
		if ev.RolledBack {
			set(config.AutopilotRolledBack, "a guardrail rolled it back to shadow")
		} else if ev.Unavailable != "" {
			// Something it needs went away, for example Claude Code was updated and the hook probe no longer
			// covers it. The program already runs it as shadow in that case; say so, and let it earn its way back.
			set(config.AutopilotShadow, "no longer available in your setup: "+ev.Unavailable)
		}
	case config.AutopilotRolledBack:
		switch {
		case cur.Attempts >= p.MaxAttempts:
			next.Reason = "rolled back after the maximum number of tries; leaving it for you to decide"
		case now.Sub(time.UnixMilli(cur.SinceMS)) >= time.Duration(p.CooldownDays)*24*time.Hour:
			set(config.AutopilotShadow, "cool-down over; collecting shadow estimates again")
			return Decision{Entry: next, ClearRollback: ev.RolledBack}
		default:
			next.Reason = "rolled back; waiting out the cool-down"
		}
	default:
		set(config.AutopilotShadow, "unknown state reset to shadow")
	}
	return Decision{Entry: next}
}

// NetOf is the estimated net tokens an optimizer kept out of the context across the evidence: what
// it removed, minus the text it adds (rule 15). The cost comes from the technique registry, so there
// is one place it is declared. It is charged per session for text every session pays, and per event for
// text added each time it acts, unless the optimizer's own estimate already subtracts that. An
// optimizer that adds text without declaring numbers cannot be charged here: its figure is returned as
// it is, for the planner to show, and Advance refuses to promote it (see CannotJudge).
func (p Policy) NetOf(name string, sessions, events int, removed float64) int64 {
	o, ok := technique.OverheadOf(name)
	if !ok {
		return int64(removed)
	}
	net := int64(removed) - o.PerSession*int64(sessions)
	if !o.Netted {
		net -= o.PerUse * int64(events)
	}
	return net
}

// CannotJudge says why Autopilot cannot judge an optimizer from shadow evidence (its cost cannot be netted, or its
// shadow mode has no saving estimate), or "" when it can. Autopilot never
// promotes what it cannot charge for (rule 15). Names the registry does not know are left alone: they
// are not text-adding techniques the registry tracks.
func (p Policy) CannotJudge(name string) string {
	if _, known := technique.Get(name); !known {
		return ""
	}
	// A technique with no shadow estimate can still be judged by probation, which measures sessions.
	if NoSavingEstimate(name) && !p.NeedsProbation(name) {
		return noEstimateReason
	}
	if _, ok := technique.OverheadOf(name); !ok {
		return "its cost in added text is not declared as numbers, so it cannot be charged against its saving"
	}
	return ""
}

const noEstimateReason = "its shadow mode counts what it would have done but cannot estimate the tokens that would save, so Autopilot has no saving to judge"

// NoSavingEstimate reports whether the named technique's shadow mode cannot put a number on its saving.
func NoSavingEstimate(name string) bool {
	t, ok := technique.Get(name)
	return ok && t.NoSavingEstimate
}

// decideProbation is the probation branch of Decide. The optimizer is on for a random half of new sessions.
// It is kept only on a clean comparison, and goes back to shadow when the comparison is bad, cannot run, or
// shows nothing by the deadline.
func (p Policy) decideProbation(cur config.AutopilotEntry, ev Evidence, now time.Time) Decision {
	next := cur
	set := func(state, reason string) {
		next.State, next.Reason, next.SinceMS = state, reason, now.UnixMilli()
	}
	pe := ev.Probation
	switch {
	case ev.RolledBack:
		set(config.AutopilotRolledBack, "a guardrail rolled it back to shadow during the trial")
	case ev.Unavailable != "":
		set(config.AutopilotShadow, "no longer available in your setup: "+ev.Unavailable)
	case ev.CanProbation != "":
		set(config.AutopilotShadow, "the trial cannot continue: "+ev.CanProbation)
	case pe.Status == "costs_more" || pe.Status == "worse_outcomes":
		set(config.AutopilotRolledBack, "the trial showed it makes sessions worse (measured); back to shadow")
	case pe.Status == "saves" && pe.NOn >= p.ProbationMinSessionsPerArm && pe.NOff >= p.ProbationMinSessionsPerArm:
		set(config.AutopilotOn, "kept after a clean trial against sessions without it (measured)")
	case now.Sub(time.UnixMilli(cur.SinceMS)) >= time.Duration(p.ProbationMaxDays)*24*time.Hour:
		set(config.AutopilotShadow, fmt.Sprintf("no measurable benefit after %d days of trial; leaving it in shadow", p.ProbationMaxDays))
	default:
		next.Reason = fmt.Sprintf("trial running: %d sessions with it on and %d with it off so far, each side needs %d", pe.NOn, pe.NOff, p.ProbationMinSessionsPerArm)
	}
	return Decision{Entry: next}
}
