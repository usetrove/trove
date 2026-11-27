package measure

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/usetrove/trove/internal/measure/limitfit"
	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/store"
)

// The routine steer's trial (hook_routine_steer, technique B24). Its outcome is thinking tokens per routine turn,
// from the agent's own log (the thinking figure the history parser reads, see analyze's ledgers), and the same
// difference as a share of the plan limits (limitfit). Its guardrails beside the trial's usual ones are re-prompts:
// how often the prompt after a routine turn reads as a correction.
//
// A routine turn starts at the hook's steered or would_steer event and ends at its turn_end event, the session's next
// prompt. Its requests are the main conversation's history requests in that window. Only numbers are read.

// SteerArm is one arm's figures over its finished sessions.
type SteerArm struct {
	// Sessions counts sessions with at least one routine turn whose requests carry a thinking figure.
	Sessions int `json:"sessions"`
	// Turns counts those routine turns.
	Turns int `json:"routine_turns"`
	// PerSession is each such session's mean thinking tokens per routine turn (measured).
	PerSession []float64 `json:"-"`
	// Closed counts routine turns another prompt followed; Reprompted those whose next prompt read as a correction.
	Closed     int `json:"closed_turns"`
	Reprompted int `json:"reprompted"`
}

// Mean is the mean over sessions of thinking tokens per routine turn, 0 with no sessions.
func (a SteerArm) Mean() float64 {
	if len(a.PerSession) == 0 {
		return 0
	}
	var s float64
	for _, v := range a.PerSession {
		s += v
	}
	return s / float64(len(a.PerSession))
}

func (a SteerArm) variance() float64 {
	n := len(a.PerSession)
	if n < 2 {
		return 0
	}
	m := a.Mean()
	var s float64
	for _, v := range a.PerSession {
		s += (v - m) * (v - m)
	}
	return s / float64(n-1)
}

// RepromptRate is the share of closed routine turns followed by a correction, 0 when none closed.
func (a SteerArm) RepromptRate() float64 {
	if a.Closed == 0 {
		return 0
	}
	return float64(a.Reprompted) / float64(a.Closed)
}

// SteerTrialByArm reads the trial's sessions assigned after sinceMS. A session with any event or request at or after
// cutoffMS may still be running, so it is left out.
func SteerTrialByArm(ctx context.Context, db *store.DB, optimizer string, sinceMS, cutoffMS int64) (on, off SteerArm, err error) {
	rows, err := db.QueryContext(ctx, `SELECT session_key, arm FROM experiment_assignments WHERE optimizer = ? AND assigned_at > ? ORDER BY session_key`, optimizer, sinceMS)
	if err != nil {
		return on, off, err
	}
	type asg struct{ key, arm string }
	var asgs []asg
	for rows.Next() {
		var a asg
		if err := rows.Scan(&a.key, &a.arm); err != nil {
			_ = rows.Close()
			return on, off, err
		}
		asgs = append(asgs, a)
	}
	if err := rows.Close(); err != nil {
		return on, off, err
	}
	if err := rows.Err(); err != nil {
		return on, off, err
	}
	for _, a := range asgs {
		arm := &off
		switch a.arm {
		case "on":
			arm = &on
		case "off":
		default:
			continue
		}
		s, ok, err := steerSession(ctx, db, optimizer, a.key, cutoffMS)
		if err != nil {
			return on, off, err
		}
		if !ok {
			continue
		}
		arm.Closed += s.closed
		arm.Reprompted += s.reprompted
		if s.turns > 0 {
			arm.Sessions++
			arm.Turns += s.turns
			arm.PerSession = append(arm.PerSession, s.thinking/float64(s.turns))
		}
	}
	return on, off, nil
}

type steerSess struct {
	turns              int     // routine turns with a thinking figure
	thinking           float64 // their thinking tokens
	closed, reprompted int
}

type steerEvent struct {
	ts   int64
	kind string
}

// steerSession reads one session. ok is false when it may still be running.
func steerSession(ctx context.Context, db *store.DB, optimizer, key string, cutoffMS int64) (steerSess, bool, error) {
	var s steerSess
	like := key + "~%"
	rows, err := db.QueryContext(ctx, `SELECT ts, kind FROM hook_events WHERE hook = ? AND (session_key = ? OR session_key LIKE ?)
		AND kind IN ('steered', 'would_steer', 'turn_end', 'reprompted') ORDER BY ts, id`, optimizer, key, like)
	if err != nil {
		return s, false, err
	}
	var evs []steerEvent
	for rows.Next() {
		var e steerEvent
		if err := rows.Scan(&e.ts, &e.kind); err != nil {
			_ = rows.Close()
			return s, false, err
		}
		evs = append(evs, e)
	}
	if err := rows.Close(); err != nil {
		return s, false, err
	}
	if err := rows.Err(); err != nil {
		return s, false, err
	}
	if len(evs) == 0 {
		return s, false, nil
	}
	reqs, last, err := mainThinking(ctx, db, key, evs[0].ts)
	if err != nil {
		return s, false, err
	}
	if evs[len(evs)-1].ts >= cutoffMS || last >= cutoffMS {
		return s, false, nil
	}
	s = steerTurns(evs, reqs)
	return s, true, nil
}

type thinkReq struct {
	ts       int64
	thinking sql.NullInt64
}

// mainThinking reads the main conversation's history requests from fromMS on, with their thinking figures, and the
// time of the session's newest request.
func mainThinking(ctx context.Context, db *store.DB, key string, fromMS int64) ([]thinkReq, int64, error) {
	like := key + "~%"
	var last sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT MAX(ts) FROM history_requests WHERE session_key = ? OR session_key LIKE ?`, key, like).Scan(&last); err != nil {
		return nil, 0, err
	}
	rows, err := db.QueryContext(ctx, `SELECT ts, thinking_tokens FROM history_requests
		WHERE (session_key = ? OR session_key LIKE ?) AND ts >= ? AND sidechain = 0 AND COALESCE(agent_key, '') = ''
		ORDER BY ts, request_key`, key, like, fromMS)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	var out []thinkReq
	for rows.Next() {
		var r thinkReq
		if err := rows.Scan(&r.ts, &r.thinking); err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, last.Int64, rows.Err()
}

// steerTurns is the pure part: split the requests into routine turns by the events and add up their thinking. A turn
// none of whose requests carries a thinking figure is left out, never counted as zero (rule 9).
func steerTurns(evs []steerEvent, reqs []thinkReq) steerSess {
	var s steerSess
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].ts < evs[j].ts })
	for i, e := range evs {
		switch e.kind {
		case "turn_end":
			s.closed++
			continue
		case "reprompted":
			s.reprompted++
			continue
		}
		end := int64(math.MaxInt64)
		for _, n := range evs[i+1:] {
			if n.kind == "turn_end" || n.kind == "steered" || n.kind == "would_steer" {
				end = n.ts
				break
			}
		}
		var sum int64
		seen := false
		for _, r := range reqs {
			if r.ts >= e.ts && r.ts < end && r.thinking.Valid {
				sum += r.thinking.Int64
				seen = true
			}
		}
		if seen {
			s.turns++
			s.thinking += float64(sum)
		}
	}
	return s
}

// SteerVerdict is the trial's comparison so far. Every figure is measured except Limits, which is estimated.
type SteerVerdict struct {
	On  SteerArm `json:"on"`
	Off SteerArm `json:"off"`
	// Enough is true once both arms have the minimum number of sessions with routine turns.
	Enough bool `json:"enough_data"`
	// SavedPerTurn is the off arm's mean thinking tokens per routine turn minus the on arm's (measured).
	SavedPerTurn float64 `json:"saved_thinking_tokens_per_routine_turn"`
	// Saves and Worse say the difference is beyond chance at every look (the sequential test the rollback checks
	// use), in either direction.
	Saves bool `json:"saves"`
	Worse bool `json:"worse"`
	// RepromptTrips is the re-prompt guardrail, with its plain-English reason.
	RepromptTrips bool   `json:"reprompt_trips"`
	RepromptWhy   string `json:"reprompt_reason,omitempty"`
	// Limits is SavedPerTurn as a share of the plan limits per routine turn (estimated), nil without a fit.
	Limits *limitfit.Estimate `json:"limits_per_routine_turn,omitempty"`
}

// JudgeSteerTrial compares the arms. minPerArm is the trial policy's minimum sessions per arm, repromptMarginPts the
// percentage points by which the on arm's re-prompt rate may exceed the off arm's before the guardrail trips.
func JudgeSteerTrial(on, off SteerArm, minPerArm int, repromptMarginPts float64) SteerVerdict {
	v := SteerVerdict{On: on, Off: off}
	if on.Closed >= minPerArm && off.Closed >= minPerArm {
		limit := off.RepromptRate() + repromptMarginPts/100
		if limit < 1 && seqRateExceeds(on.Reprompted, on.Closed, limit) {
			v.RepromptTrips = true
			v.RepromptWhy = fmt.Sprintf("after a steered routine turn the next prompt was a correction %.0f%% of the time (%d of %d) against %.0f%% without the steer (%d of %d), more than the %g-point margin at every look (measured)",
				on.RepromptRate()*100, on.Reprompted, on.Closed, off.RepromptRate()*100, off.Reprompted, off.Closed, repromptMarginPts)
		}
	}
	if on.Sessions < minPerArm || off.Sessions < minPerArm {
		return v
	}
	v.Enough = true
	v.SavedPerTurn = off.Mean() - on.Mean()
	se := math.Sqrt(on.variance()/float64(on.Sessions) + off.variance()/float64(off.Sessions))
	// The test is tuned for an effect of a quarter of the off arm's thinking per routine turn.
	tau := 0.25 * off.Mean()
	v.Saves = seqMeanExceeds(v.SavedPerTurn, se, tau)
	v.Worse = seqMeanExceeds(-v.SavedPerTurn, se, tau)
	return v
}

// SteerLimits expresses tokens of thinking per routine turn (billed as output) on model as a share of the plan limits,
// from the limit fit in dir. nil when there is no fit or the figure is not positive.
func SteerLimits(dir, model string, tokens float64, now time.Time) *limitfit.Estimate {
	if tokens <= 0 {
		return nil
	}
	d := limitfit.Delta{Model: model}
	d.Tokens[limitfit.TOutput] = int64(math.Round(tokens))
	if tbl, err := pricing.Default(); err == nil {
		limitfit.PriceDelta(tbl, &d, now)
	}
	return limitfit.ReadCache(dir).Percent(d)
}

// Text is the verdict's figures in one line, every number labeled.
func (v SteerVerdict) Text() string {
	if !v.Enough {
		return fmt.Sprintf("%d sessions with the steer and %d without have routine turns with a thinking figure so far", v.On.Sessions, v.Off.Sessions)
	}
	s := fmt.Sprintf("thinking per routine turn %.0f tokens with the steer against %.0f without (measured, %d and %d sessions, %d and %d routine turns)",
		v.On.Mean(), v.Off.Mean(), v.On.Sessions, v.Off.Sessions, v.On.Turns, v.Off.Turns)
	if e := v.Limits; e != nil {
		if e.FiveHour != nil {
			s += fmt.Sprintf("; the difference is about %.3f%% of the 5-hour limit per routine turn", e.FiveHour.Point)
		}
		if e.Week != nil {
			s += fmt.Sprintf(" and %.4f%% of the weekly limit", e.Week.Point)
		}
		s += " (estimated)"
	}
	s += fmt.Sprintf("; re-prompts after a routine turn %.0f%% with it and %.0f%% without (measured)", v.On.RepromptRate()*100, v.Off.RepromptRate()*100)
	return s
}
