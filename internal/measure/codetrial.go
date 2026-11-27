package measure

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"time"

	"github.com/usetrove/trove/internal/measure/limitfit"
	"github.com/usetrove/trove/internal/store"
)

// Code-output trials. A rule set that asks the agent to write less code (Ponytail, technique W9) is judged on what
// it is for: the output tokens the agent spends writing code, per session, and whether tests still pass. The code
// figure is the byte size of the inputs of the code-writing tools (CodeTools) in the session's history, main
// conversation and subagents alike (history_tool_calls, never their content), turned into tokens with
// CodeBytesPerToken, so it is estimated. Every finished session with history counts, including those that wrote
// no code, so a rule set that stops the agent writing code at all is not hidden by leaving those sessions out.

// CodeTools are the tools whose input is code the agent wrote (Claude Code tool names, code.claude.com/docs/en/hooks,
// verified 2026-11-24).
var CodeTools = []string{"Write", "Edit", "MultiEdit", "NotebookEdit"}

// CodeBytesPerToken turns tool-input bytes into output tokens. It is the same prior the output split uses before it
// has calibrated (analyze.DefaultBytesPerToken); the trial compares bytes, so the ratio only scales the figures shown.
const CodeBytesPerToken = 3.5

// CodeArm is one arm's figures over its finished sessions.
type CodeArm struct {
	// Sessions counts finished sessions with history; Coding those with at least one code-writing call.
	Sessions int `json:"sessions"`
	Coding   int `json:"coding_sessions"`
	// PerSession is each session's code-writing input bytes.
	PerSession []float64 `json:"-"`
}

// MeanTokens is the mean code-writing output per session in tokens (estimated), 0 with no sessions.
func (a CodeArm) MeanTokens() float64 { return mean(a.PerSession) / CodeBytesPerToken }

// CodeTrialByArm reads the trial's sessions assigned after sinceMS. A session with a request at or after cutoffMS may
// still be running, so it is left out. model is the model most of the trial's requests used, for the limit figure.
func CodeTrialByArm(ctx context.Context, db *store.DB, optimizer string, sinceMS, cutoffMS int64) (on, off CodeArm, model string, err error) {
	rows, err := db.QueryContext(ctx, `SELECT a.arm,
		(SELECT MAX(r.ts) FROM history_requests r WHERE r.session_key = a.session_key OR r.session_key LIKE a.session_key || '~%'),
		(SELECT COALESCE(SUM(t.input_bytes), 0) FROM history_tool_calls t JOIN history_requests r ON r.request_key = t.request_key
			WHERE (r.session_key = a.session_key OR r.session_key LIKE a.session_key || '~%')
			AND t.tool_name IN ('Write', 'Edit', 'MultiEdit', 'NotebookEdit'))
		FROM experiment_assignments a WHERE a.optimizer = ? AND a.assigned_at > ? ORDER BY a.session_key`, optimizer, sinceMS)
	if err != nil {
		return on, off, "", err
	}
	for rows.Next() {
		var arm string
		var last sql.NullInt64
		var bytes int64
		if err := rows.Scan(&arm, &last, &bytes); err != nil {
			_ = rows.Close()
			return on, off, "", err
		}
		if !last.Valid || last.Int64 >= cutoffMS {
			continue
		}
		a := &off
		switch arm {
		case "on":
			a = &on
		case "off":
		default:
			continue
		}
		a.Sessions++
		if bytes > 0 {
			a.Coding++
		}
		a.PerSession = append(a.PerSession, float64(bytes))
	}
	if err := rows.Close(); err != nil {
		return on, off, "", err
	}
	if err := rows.Err(); err != nil {
		return on, off, "", err
	}
	var m sql.NullString
	err = db.QueryRowContext(ctx, `SELECT r.model FROM experiment_assignments a JOIN history_requests r
		ON (r.session_key = a.session_key OR r.session_key LIKE a.session_key || '~%')
		WHERE a.optimizer = ? AND a.assigned_at > ? AND r.model <> '' AND r.ts < ?
		GROUP BY r.model ORDER BY COUNT(*) DESC, r.model LIMIT 1`, optimizer, sinceMS, cutoffMS).Scan(&m)
	if err != nil && err != sql.ErrNoRows {
		return on, off, "", err
	}
	return on, off, m.String, nil
}

// CodeVerdict is a code-output trial's comparison so far. Code figures are estimated (sizes turned into tokens), the
// test figures measured, Limits estimated.
type CodeVerdict struct {
	On  CodeArm `json:"on"`
	Off CodeArm `json:"off"`
	// Tests are the arms' test results (measured).
	TestsOn  TestFailArm `json:"tests_on"`
	TestsOff TestFailArm `json:"tests_off"`
	// Enough is true once both arms have the minimum number of finished sessions.
	Enough bool `json:"enough_data"`
	// SavedPerSession is the off arm's mean code-writing tokens per session minus the on arm's (estimated).
	SavedPerSession float64 `json:"saved_code_tokens_per_session"`
	// Saves and Worse say the difference is beyond chance at every look (the sequential test the rollback checks use).
	Saves bool `json:"saves"`
	Worse bool `json:"worse"`
	// Limits is SavedPerSession as a share of the plan limits per session (estimated), nil without a fit.
	Limits *limitfit.Estimate `json:"limits_per_session,omitempty"`
}

// JudgeCodeTrial compares the arms. minPerArm is the trial policy's minimum sessions per arm.
func JudgeCodeTrial(on, off CodeArm, tOn, tOff TestFailArm, minPerArm int) CodeVerdict {
	v := CodeVerdict{On: on, Off: off, TestsOn: tOn, TestsOff: tOff}
	if on.Sessions < minPerArm || off.Sessions < minPerArm {
		return v
	}
	v.Enough = true
	diff := mean(off.PerSession) - mean(on.PerSession)
	v.SavedPerSession = diff / CodeBytesPerToken
	se := math.Sqrt(sampleVar(on.PerSession)/float64(on.Sessions) + sampleVar(off.PerSession)/float64(off.Sessions))
	// Tuned, as the steer's test is, for an effect of a quarter of the off arm's figure.
	tau := 0.25 * mean(off.PerSession)
	v.Saves = seqMeanExceeds(diff, se, tau)
	v.Worse = seqMeanExceeds(-diff, se, tau)
	return v
}

// CodeLimits expresses code-writing output tokens per session on model as a share of the plan limits. It is
// SteerLimits under the name the code trial reads.
func CodeLimits(dir, model string, tokens float64, now time.Time) *limitfit.Estimate {
	return SteerLimits(dir, model, tokens, now)
}

// Text is the verdict's figures in one line, every number labeled.
func (v CodeVerdict) Text() string {
	tests := fmt.Sprintf("tests passed at the end of %s of sessions that ran them with the rules and %s without (measured, %d and %d sessions)",
		passRate(v.TestsOn), passRate(v.TestsOff), v.TestsOn.Sessions, v.TestsOff.Sessions)
	if !v.Enough {
		return fmt.Sprintf("%d finished sessions with the rules and %d without so far; %s", v.On.Sessions, v.Off.Sessions, tests)
	}
	s := fmt.Sprintf("code written per session %.0f tokens with the rules against %.0f without (estimated from tool-input sizes, %d and %d sessions, %d and %d of them wrote code)",
		v.On.MeanTokens(), v.Off.MeanTokens(), v.On.Sessions, v.Off.Sessions, v.On.Coding, v.Off.Coding)
	if e := v.Limits; e != nil {
		if e.FiveHour != nil {
			s += fmt.Sprintf("; the difference is about %.3f%% of the 5-hour limit per session", e.FiveHour.Point)
		}
		if e.Week != nil {
			s += fmt.Sprintf(" and %.4f%% of the weekly limit", e.Week.Point)
		}
		s += " (estimated)"
	}
	return s + "; " + tests
}

func passRate(a TestFailArm) string {
	if a.Sessions == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.0f%%", (1-a.Rate())*100)
}
