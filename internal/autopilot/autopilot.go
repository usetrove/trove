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
	// MaxAttempts is how many times one optimizer may be promoted. After that a
	// rollback is left for the user to decide.
	MaxAttempts int
}

// DefaultPolicy is the shipped "lossless-v1" policy.
func DefaultPolicy() Policy {
	return Policy{
		Eligible:     []string{"noise_normalizer", "duplicate_result_collapser"},
		MinSessions:  10,
		MinNetTokens: 2000,
		LookbackDays: 30,
		CooldownDays: 7,
		MaxAttempts:  2,
	}
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
	case ev.NetTokens < p.MinNetTokens:
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
		if ok, why := p.ready(ev); ok {
			set(config.AutopilotOn, "turned on from shadow evidence (estimated)")
			next.Attempts = cur.Attempts + 1
		} else {
			set(config.AutopilotShadow, "evidence did not hold: "+why)
		}
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
// it is, for the planner to show, and Advance refuses to promote it (see ChargeableCost).
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

// ChargeableCost says why an optimizer's cost cannot be netted, or "" when it can. Autopilot never
// promotes what it cannot charge for (rule 15). Names the registry does not know are left alone: they
// are not text-adding techniques the registry tracks.
func ChargeableCost(name string) string {
	if _, known := technique.Get(name); !known {
		return ""
	}
	if _, ok := technique.OverheadOf(name); !ok {
		return "its cost in added text is not declared as numbers, so it cannot be charged against its saving"
	}
	return ""
}
