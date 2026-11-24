package optimize

import (
	"context"
	"fmt"
)

// OptimizerThinkingClear is the name of the optimizer that clears old thinking in very long sessions.
const OptimizerThinkingClear = "thinking_clear"

// ContextManagementBeta is the beta context editing needs (platform.claude.com/docs/en/build-with-claude/
// context-editing, verified 2026-11-24).
const ContextManagementBeta = "context-management-2025-06-27"

// Placeholders, not set by a benchmark.
const (
	// ThinkingClearKeep is how many of the most recent turns keep their thinking (keep.thinking_turns). The page
	// counts the turn in progress, and a user message holding only tool results continues it.
	ThinkingClearKeep = 2
	// ThinkingClearMinTokens is the estimated request size below which nothing is cleared: only very long
	// sessions carry enough old thinking to repay the cache it costs.
	ThinkingClearMinTokens = 100_000
)

// ThinkingClearModels are the models that keep earlier thinking in context by default and support context
// editing (same page and date: "keep all prior thinking" on Opus 4.5 and later, Sonnet 4.6 and later, Haiku 5.5
// and later, Fable and Mythos). On a model that keeps only the turn in progress there is nothing to clear.
var ThinkingClearModels = map[string]bool{
	"claude-opus-5-5": true, "claude-opus-5": true, "claude-sonnet-5-5": true, "claude-sonnet-5": true,
	"claude-haiku-5-5": true, "claude-fable-5-2": true, "claude-fable-5-1": true, "claude-fable-5": true,
	"claude-mythos-5-2": true, "claude-mythos-5-1": true, "claude-mythos-5": true,
}

// ClearEstimate is thinking_clear's estimate for one request, in tokens (estimated from byte sizes).
type ClearEstimate struct {
	Model string `json:"model"`
	// ClearedTurns are the turns whose thinking would be cleared on this request.
	ClearedTurns int `json:"cleared_turns"`
	// Saved is the thinking those turns carry, which this request would not send (it was read from the cache).
	Saved int64 `json:"saved_tokens"`
	// Lost is, on the first request of a new turn, the part of the cached prefix after the thinking that turn
	// newly clears: it is written to the cache again instead of read. Zero on other requests, whose cleared set
	// is the same as the previous request's.
	Lost  int64  `json:"lost_cache_tokens"`
	Label string `json:"label"`
}

// ThinkingClear adds context editing's clear_thinking_20251015 strategy, keeping the last ThinkingClearKeep turns'
// thinking, to requests of very long sessions on models that keep earlier thinking. Clearing is applied
// server-side; the client still sends the whole history. The page says "the cache is invalidated at the point where
// clearing occurs", so every new turn, as one more turn's thinking is cleared, the context after it is written to
// the cache again. Whether that pays depends on the session, so this optimizer estimates both sides on every
// request (ClearEstimate) and the measure package weighs them with the user's plan-limit weights when there is a
// fit, or API prices otherwise; Autopilot may only promote it when the shadow estimate is net positive.
//
// Not verified: the page also says that on models with preserved thinking "server-side context management never
// invalidates thinking blocks", and it does not say whether clearing removes earlier thinking on those models
// (Opus 5.5, Sonnet 5.5, Haiku 5.5). The response's context_management.applied_edits (cleared_thinking_turns,
// cleared_input_tokens) is what would show it, which is why it stays shadow-first.
//
// It is a pure function of the request: the same history gives the same bytes (rule 5).
type ThinkingClear struct{}

// Name implements Optimizer.
func (ThinkingClear) Name() string { return OptimizerThinkingClear }

// Tier implements Optimizer: it changes what the model sees of its own earlier reasoning.
func (ThinkingClear) Tier() Tier { return TierBehavior }

// Points implements Capable.
func (ThinkingClear) Points() []Point { return []Point{PointRequest} }

// OverheadTokens implements Capable: the context_management field is a request parameter, not text the model reads.
func (ThinkingClear) OverheadTokens() int { return 0 }

// Caveat implements Caveater.
func (ThinkingClear) Caveat() string {
	return "API-key gateway traffic only; very long sessions on models that keep earlier thinking; promoted only when the shadow estimate of context saved beats the cache rewritten"
}

// Applies implements Optimizer.
func (ThinkingClear) Applies(r *Request, s Session) bool {
	return ThinkingClearModels[r.Model] && r.ContextManagement.Len() == 0 && !r.RootEmpty &&
		int64(float64(len(r.Raw))/s.bytesPerToken()) >= ThinkingClearMinTokens
}

// Transform implements Optimizer.
func (t ThinkingClear) Transform(ctx context.Context, r *Request) (*Request, Report) {
	return t.TransformSession(ctx, r, Session{})
}

// TransformSession implements SessionTransformer: the session's bytes-per-token ratio scales the estimate only.
func (t ThinkingClear) TransformSession(_ context.Context, r *Request, s Session) (*Request, Report) {
	if err := r.Ensure(); err != nil {
		return r, Report{Err: err.Error()}
	}
	if !t.Applies(r, s) {
		return r, Report{}
	}
	est := clearEstimate(r, s.bytesPerToken())
	if est.ClearedTurns == 0 {
		return r, Report{}
	}
	field := fmt.Sprintf(`,"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":{"type":"thinking_turns","value":%d}}]}`, ThinkingClearKeep)
	out, err := r.Apply([]Edit{{Span: Span{r.RootClose, r.RootClose}, New: []byte(field)}})
	if err != nil {
		return r, Report{Err: err.Error()}
	}
	return out, Report{Name: OptimizerThinkingClear, Clear: &est, Betas: []string{ContextManagementBeta}}
}

// clearEstimate splits the messages into turns (a user message with new input starts one; a user message holding
// only tool results continues it) and sizes the thinking blocks of every turn but the last ThinkingClearKeep.
func clearEstimate(r *Request, bpt float64) ClearEstimate {
	type turn struct {
		thinking   int64
		firstBlock int // offset of its first thinking block, -1 if none
	}
	var turns []turn
	lastUser := -1
	for i, m := range r.Messages {
		if m.Role == "user" && !toolResultsOnly(m) {
			turns = append(turns, turn{firstBlock: -1})
			lastUser = i
			continue
		}
		if m.Role != "assistant" || len(turns) == 0 {
			continue
		}
		t := &turns[len(turns)-1]
		for _, b := range m.Blocks {
			if b.Type == "thinking" || b.Type == "redacted_thinking" {
				t.thinking += int64(b.Span.Len())
				if t.firstBlock < 0 {
					t.firstBlock = b.Span.S
				}
			}
		}
	}
	est := ClearEstimate{Model: r.Model, Label: "estimated"}
	cut := len(turns) - ThinkingClearKeep
	var saved int64
	for i := 0; i < cut; i++ {
		if turns[i].thinking > 0 {
			est.ClearedTurns++
			saved += turns[i].thinking
		}
	}
	est.Saved = int64(float64(saved)/bpt + 0.5)
	// A new turn started with this request: turn cut-1 has just moved into the cleared set.
	newTurn := lastUser >= 0 && lastUser == len(r.Messages)-1
	if newTurn && cut >= 1 && turns[cut-1].firstBlock >= 0 {
		lost := int64(r.Messages[lastUser].Span.S - turns[cut-1].firstBlock)
		est.Lost = int64(float64(lost)/bpt + 0.5)
	}
	return est
}
