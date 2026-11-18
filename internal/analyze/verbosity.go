package analyze

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Verbosity levels LearnVerbosity can choose; they match config.VerbosityConcise and
// config.VerbosityTerse (analyze does not import config).
const (
	VerbosityLevelConcise = "concise"
	VerbosityLevelTerse   = "terse"
)

// VerbosityThresholds are the rules LearnVerbosity applies. They are reported with
// every result so the evidence can be checked against them.
type VerbosityThresholds struct {
	MinSessions int `json:"min_sessions"`
	MinReplies  int `json:"min_replies"`
	// LongReplyTokens is the output size from which a reply counts: a fast answer to a
	// short reply says nothing about verbosity.
	LongReplyTokens int64 `json:"long_reply_tokens"`
	// FastFollowUpMS is how soon after a reply ends a user message counts as cutting it short.
	FastFollowUpMS int64 `json:"fast_follow_up_ms"`
	// TerseRate is the share of counted replies with a signal from which terse is chosen.
	TerseRate float64 `json:"terse_rate"`
}

// DefaultVerbosityThresholds are the thresholds `trove optimize verbosity` uses.
func DefaultVerbosityThresholds() VerbosityThresholds {
	return VerbosityThresholds{MinSessions: 10, MinReplies: 50, LongReplyTokens: 200, FastFollowUpMS: 15_000, TerseRate: 0.15}
}

// VerbosityEvidence is what a level was chosen from. All counts are estimated: a user
// turn is inferred from metadata, never read.
type VerbosityEvidence struct {
	Label    string    `json:"label"` // always "estimated"
	From     time.Time `json:"from"`
	To       time.Time `json:"to"`
	Sessions int       `json:"sessions"`
	// Replies is the counted replies: long ones, plus any the client cut off.
	Replies int `json:"replies"`
	// Interrupted is replies whose stream the client closed before the end.
	Interrupted int `json:"interrupted"`
	// FastFollowUps is long replies followed, within FastFollowUpMS of their end, by a
	// request on the same model that adds no tool result (so a user message, not a tool loop).
	FastFollowUps int `json:"fast_follow_ups"`
	// Signals is replies with either signal; a reply counts once.
	Signals int     `json:"signals"`
	Rate    float64 `json:"rate"`
}

// VerbosityChoice is LearnVerbosity's answer. Level is empty when there is not enough
// evidence, and then nothing should change.
type VerbosityChoice struct {
	Level      string              `json:"level"`
	Reason     string              `json:"reason"`
	Evidence   VerbosityEvidence   `json:"evidence"`
	Thresholds VerbosityThresholds `json:"thresholds"`
}

type verbosityRow struct {
	ts, latency, output int64
	model, errClass     string
	streamed            bool
	status              int
	tools, history      int64
	cats                bool // segment estimates were recorded
}

// LearnVerbosity picks the verbosity_steer level from recorded sessions in [from, to):
// terse when users often cut long replies short (an interrupted stream, or a user
// message within seconds of the reply ending), concise otherwise. It reads metadata
// only and changes nothing.
func LearnVerbosity(ctx context.Context, db Querier, from, to time.Time, th VerbosityThresholds) (VerbosityChoice, error) {
	ch := VerbosityChoice{Thresholds: th, Evidence: VerbosityEvidence{Label: "estimated", From: from.UTC(), To: to.UTC()}}
	rows, err := db.QueryContext(ctx, `SELECT session_key, ts, latency_ms, output_tokens, model, error_class, streamed, status_code,
		COALESCE(segment_token_estimates, '') FROM requests WHERE ts >= ? AND ts < ? AND session_key != '' ORDER BY session_key, ts, id`,
		from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return ch, err
	}
	defer func() { _ = rows.Close() }()
	bySession := map[string][]verbosityRow{}
	var order []string
	for rows.Next() {
		var key, segs string
		var r verbosityRow
		var streamed int
		if err := rows.Scan(&key, &r.ts, &r.latency, &r.output, &r.model, &r.errClass, &streamed, &r.status, &segs); err != nil {
			return ch, err
		}
		r.streamed = streamed != 0
		r.tools, r.history, r.cats = toolAndHistoryTokens(segs)
		if _, ok := bySession[key]; !ok {
			order = append(order, key)
		}
		bySession[key] = append(bySession[key], r)
	}
	if err := rows.Err(); err != nil {
		return ch, err
	}
	e := &ch.Evidence
	for _, key := range order {
		rs := bySession[key]
		counted := false
		for i, r := range rs {
			interrupted := r.streamed && r.errClass == "client_disconnected" && r.output > 0
			if !interrupted && (r.status != 200 || r.output < th.LongReplyTokens) {
				continue
			}
			e.Replies++
			counted = true
			if interrupted {
				e.Interrupted++
				e.Signals++
				continue
			}
			if fastUserFollowUp(r, rs[i+1:], th.FastFollowUpMS) {
				e.FastFollowUps++
				e.Signals++
			}
		}
		if counted {
			e.Sessions++
		}
	}
	if e.Replies > 0 {
		e.Rate = float64(e.Signals) / float64(e.Replies)
	}
	switch {
	case e.Sessions < th.MinSessions || e.Replies < th.MinReplies:
		ch.Reason = fmt.Sprintf("not enough evidence: %d sessions and %d long replies recorded; at least %d sessions and %d replies are needed, so the level stays as it is",
			e.Sessions, e.Replies, th.MinSessions, th.MinReplies)
	case e.Rate >= th.TerseRate:
		ch.Level = VerbosityLevelTerse
		ch.Reason = fmt.Sprintf("%.0f%% of long replies were cut short (%d interrupted, %d followed by a user message within %ds), at or above the %.0f%% threshold",
			100*e.Rate, e.Interrupted, e.FastFollowUps, th.FastFollowUpMS/1000, 100*th.TerseRate)
	default:
		ch.Level = VerbosityLevelConcise
		ch.Reason = fmt.Sprintf("%.0f%% of long replies were cut short (%d interrupted, %d followed by a user message within %ds), below the %.0f%% threshold",
			100*e.Rate, e.Interrupted, e.FastFollowUps, th.FastFollowUpMS/1000, 100*th.TerseRate)
	}
	return ch, nil
}

// fastUserFollowUp reports whether the next request on r's model came within window ms
// of r's end and added history but no tool result: the user, not the agent, spoke next.
// Without recorded segment estimates on both sides it is unknown, so it is not counted.
func fastUserFollowUp(r verbosityRow, rest []verbosityRow, window int64) bool {
	for _, n := range rest {
		if n.model != r.model {
			continue
		}
		if !r.cats || !n.cats {
			return false
		}
		gap := n.ts - (r.ts + r.latency)
		return gap >= 0 && gap <= window && n.tools <= r.tools && n.history > r.history
	}
	return false
}

// toolAndHistoryTokens sums the tool_results categories and the history category of a
// stored estimate.
func toolAndHistoryTokens(raw string) (tools, history int64, ok bool) {
	if raw == "" {
		return 0, 0, false
	}
	var e struct {
		Categories map[string]int64 `json:"categories"`
	}
	if json.Unmarshal([]byte(raw), &e) != nil || e.Categories == nil {
		return 0, 0, false
	}
	for k, v := range e.Categories {
		if k == CatToolResults || strings.HasPrefix(k, CatToolResults+":") {
			tools += v
		}
	}
	return tools, e.Categories[CatHistory], true
}
