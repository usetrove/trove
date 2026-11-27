package measure

import (
	"context"
	"fmt"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/store"
)

// The session checkpoint (hook_session_checkpoint, B31, T2) is judged on the 20 tool calls after each /compact or
// /clear, in sessions that got the restore against sessions that did not (the hook splits "on" sessions by a hash of
// the session key; shadow sessions count as without). Per window it records re-reads of files read before the boundary
// and unchanged, failed commands that had failed before it, and the position of the first edit. All counts are
// measured; the tokens a restore added are estimated from its size.
//
// Two guardrails set it back to shadow, both with the code-write nudges' test and margin (placeholders):
//   - checkpoint_windows: the share of windows with a re-read or a repeated failure is higher with the restore than
//     without. A note that does not save the agent re-finding things is pure overhead.
//   - checkpoint_tests_after: the first allowlisted test run after a restore fails more often than test runs in
//     sessions without one. A stale note that misleads the agent shows up here (the test-pass guardrail).

// Guardrail rules for the session checkpoint, recorded in guardrail_events.
const (
	RuleCheckpointWindows    = "checkpoint_windows"
	RuleCheckpointTestsAfter = "checkpoint_tests_after"
)

// CheckpointArm is one side of the comparison.
type CheckpointArm struct {
	Windows        int `json:"windows"`
	Rereads        int `json:"rereads"`
	RepeatFailures int `json:"repeat_failures"`
	// BadWindows are windows with at least one re-read or repeated failure.
	BadWindows int `json:"bad_windows"`
	// Recovered are windows with an edit; CallsToRecover is the sum of the position of each window's first edit.
	Recovered      int `json:"recovered"`
	CallsToRecover int `json:"calls_to_recover"`
	// AddedTokens is what the restores added (or, without, would have added), estimated.
	AddedTokens int64 `json:"added_tokens"`
}

// MeanCallsToRecover is the mean position of the first edit among windows that had one; 0 when none did.
func (a CheckpointArm) MeanCallsToRecover() float64 {
	if a.Recovered == 0 {
		return 0
	}
	return float64(a.CallsToRecover) / float64(a.Recovered)
}

// CheckpointWindows is what the session checkpoint did and the comparison of its windows.
type CheckpointWindows struct {
	// Saved is the number of notes kept; Restored and Pointed are the boundaries that got a note or the pointer.
	Saved    int           `json:"saved"`
	Restored int           `json:"restored"`
	Pointed  int           `json:"pointed"`
	With     CheckpointArm `json:"with_restore"`
	Without  CheckpointArm `json:"without_restore"`
}

// Any reports whether anything was recorded.
func (c CheckpointWindows) Any() bool {
	return c.Saved > 0 || c.With.Windows > 0 || c.Without.Windows > 0
}

// CheckpointWindowsBetween reads the optimizer's events from fromMS (inclusive) to toMS (exclusive; 0 = no end).
func CheckpointWindowsBetween(ctx context.Context, db *store.DB, fromMS, toMS int64) (CheckpointWindows, error) {
	var out CheckpointWindows
	if toMS <= 0 {
		toMS = 1<<62 - 1
	}
	rows, err := db.QueryContext(ctx, `SELECT session_key, kind, bytes, tokens FROM hook_events
		WHERE hook = ? AND ts >= ? AND ts < ? ORDER BY session_key, ts, id`, config.OptimizerHookSessionCheckpoint, fromMS, toMS)
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()
	type window struct {
		arm             *CheckpointArm
		bad, recovered  bool
		recoveredAtCall int64
	}
	var cur *window
	curKey := ""
	flush := func() {
		if cur == nil {
			return
		}
		if cur.bad {
			cur.arm.BadWindows++
		}
		if cur.recovered {
			cur.arm.Recovered++
			cur.arm.CallsToRecover += int(cur.recoveredAtCall)
		}
		cur = nil
	}
	for rows.Next() {
		var key, kind string
		var b, tok int64
		if err := rows.Scan(&key, &kind, &b, &tok); err != nil {
			return out, err
		}
		if key != curKey {
			flush()
			curKey = key
		}
		switch kind {
		case "checkpointed":
			out.Saved++
		case "restored", "pointed", "would_restore":
			flush()
			arm := &out.Without
			switch kind {
			case "restored":
				arm = &out.With
				out.Restored++
			case "pointed":
				arm = &out.With
				out.Pointed++
			}
			arm.Windows++
			arm.AddedTokens += tok
			cur = &window{arm: arm}
		case "reread_unchanged":
			if cur != nil {
				cur.arm.Rereads++
				cur.bad = true
			}
		case "repeat_failure":
			if cur != nil {
				cur.arm.RepeatFailures++
				cur.bad = true
			}
		case "recovered":
			if cur != nil && !cur.recovered {
				cur.recovered, cur.recoveredAtCall = true, b
			}
		}
	}
	flush()
	return out, rows.Err()
}

// CheckpointSummary is the report's view of the period, or nil when the optimizer recorded nothing in it.
func CheckpointSummary(ctx context.Context, db *store.DB, w Window) (*CheckpointWindows, error) {
	c, err := CheckpointWindowsBetween(ctx, db, w.From.UnixMilli(), w.To.UnixMilli())
	if err != nil || !c.Any() {
		return nil, err
	}
	return &c, nil
}

// evaluateCheckpoint runs the two rules unless the optimizer is in skip, recording a rollback if one trips.
func evaluateCheckpoint(ctx context.Context, db *store.DB, m config.Measure, now time.Time, skip map[string]bool) ([]Event, error) {
	name := config.OptimizerHookSessionCheckpoint
	if skip[name] {
		return nil, nil
	}
	cleared, err := lastCleared(ctx, db, name)
	if err != nil {
		return nil, err
	}
	since := now.Add(-hookWindow).UnixMilli()
	if cleared > since {
		since = cleared
	}
	c, err := CheckpointWindowsBetween(ctx, db, since, 0)
	if err != nil {
		return nil, err
	}
	type rule struct {
		name    string
		on, off CodeRate
		what    string
	}
	rules := []rule{{RuleCheckpointWindows, CodeRate{Bad: c.With.BadWindows, N: c.With.Windows}, CodeRate{Bad: c.Without.BadWindows, N: c.Without.Windows},
		"%.0f%% of the 20-call windows after a restore (%d of %d) re-read an unchanged file or repeated a failed command, against %.0f%% (%d of %d) without one"}}
	on, off, err := TestsAfterNudge(ctx, db, name, "restored", since)
	if err != nil {
		return nil, err
	}
	rules = append(rules, rule{RuleCheckpointTestsAfter, on, off,
		"the first test run within 15 minutes after a restore failed %.0f%% of the time (%d of %d), against %.0f%% of test runs (%d of %d) in sessions without one"})
	for _, r := range rules {
		trips, limit := codeRuleTrips(r.on, r.off, m.MinSessions)
		if !trips {
			continue
		}
		what := fmt.Sprintf(r.what, r.on.Rate()*100, r.on.Bad, r.on.N, r.off.Rate()*100, r.off.Bad, r.off.N)
		ev := Event{At: now.UTC(), Optimizer: name, Rule: r.name, Observed: r.on.Rate(), Threshold: limit,
			SampleOn: r.on.N, SampleRef: r.off.N, Reference: RefOffSessions,
			Reason: fmt.Sprintf("Trove set %s back to shadow because %s, more than the %d-point margin (a placeholder) beyond chance. This is measured from the hook's own counts and test results; re-enable it deliberately if you disagree.",
				name, what, CodeWriteMarginPts)}
		if err := recordRollback(ctx, db, ev); err != nil {
			return nil, err
		}
		return []Event{ev}, nil
	}
	return nil, nil
}
