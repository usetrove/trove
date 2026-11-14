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
	// OverheadTokens is each optimizer's declared token cost per session (rule 15). The
	// two lossless transforms inject no text, so it is empty for them.
	OverheadTokens map[string]int64
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
