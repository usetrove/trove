package optimize

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/usetrove/trove/internal/routine"
)

// OptimizerEffortRoutinePrompts is the name of the per-prompt effort optimizer.
const OptimizerEffortRoutinePrompts = "effort_routine_prompts"

// PerMessageEffortBeta is the beta a per-message effort change needs (anthropic-beta header). Without it the API
// answers 400 "messages.N.output_config: Extra inputs are not permitted"
// (platform.claude.com/docs/en/build-with-claude/effort-per-message, verified 2026-11-24).
const PerMessageEffortBeta = "mid-conversation-output-config-2026-07-01"

// RoutinePromptEffort is the level a routine prompt's whole turn runs at. A constant, so the same history always
// gives the same bytes (rule 5).
const RoutinePromptEffort = "low"

// PerMessageEffortModels are the Claude API models documented to support per-message effort (same page and date).
// A model without it answers 400, so every other model is left alone.
var PerMessageEffortModels = map[string]bool{
	"claude-fable-5-2": true, "claude-fable-5-1": true, "claude-mythos-5-2": true, "claude-mythos-5-1": true,
	"claude-opus-5-5": true, "claude-opus-5": true, "claude-sonnet-5-5": true, "claude-haiku-5-5": true,
}

// EffortRoutinePrompts runs the turn after a routine prompt ("continue", "yes", "run the tests") at low effort,
// with a per-message effort change: a system message with empty content and output_config.effort, placed right
// after the user turn it applies to. The top-level effort never changes, so the cached prefix still matches; the
// documented way to keep it matching is to put the message after everything already sent and keep it in place on
// later requests, which is what this does, because it is a pure function of the history: every routine prompt in
// the history gets the low level after it, and the next prompt that is not routine gets the request's own level
// back. A user turn that only returns tool results is not new input and starts nothing, so a level holds for the
// whole turn, tool calls included. This is why it acts per prompt, not per tool turn (effort_tool_turns): a
// per-message change cannot take effect on a tool-result turn.
//
// It acts only where the API documents the shape: a model in PerMessageEffortModels, an explicit top-level effort
// above low, no thinking type the page lists as a 400 case ("between_tools" on Sonnet 5.5, "disabled" on Haiku 5.5;
// both are skipped on every model), and no system message already in the conversation. The prompt is classified in
// memory with internal/routine and never stored.
//
// It changes how hard the model thinks (technique class effort, T3): off by default, shadow first, judged by session
// A/B like effort_tool_turns.
type EffortRoutinePrompts struct{}

// Name implements Optimizer.
func (EffortRoutinePrompts) Name() string { return OptimizerEffortRoutinePrompts }

// Tier implements Optimizer.
func (EffortRoutinePrompts) Tier() Tier { return TierBehavior }

// Points implements Capable: it rewrites the request, which only the gateway sees.
func (EffortRoutinePrompts) Points() []Point { return []Point{PointRequest} }

// OverheadTokens implements Capable: each change it adds is an empty system message of about 20 tokens, sent only
// where the level changes; it adds no text the model reads.
func (EffortRoutinePrompts) OverheadTokens() int { return 0 }

// Caveat implements Caveater.
func (EffortRoutinePrompts) Caveat() string {
	return "API-key gateway traffic only, on models documented to support per-message effort; lowers effort for the whole turn after a routine prompt; judged by session A/B"
}

// Applies implements Optimizer.
func (EffortRoutinePrompts) Applies(r *Request, _ Session) bool {
	if r.EffortSpan.Len() == 0 || !PerMessageEffortModels[r.Model] || effortRank[r.Effort] <= effortRank[RoutinePromptEffort] {
		return false
	}
	if r.ThinkingType == "between_tools" || r.ThinkingType == "disabled" {
		return false
	}
	for _, m := range r.Messages {
		if m.Role == "system" {
			return false
		}
	}
	return true
}

// Transform implements Optimizer.
func (e EffortRoutinePrompts) Transform(_ context.Context, r *Request) (*Request, Report) {
	if err := r.Ensure(); err != nil {
		return r, Report{Err: err.Error()}
	}
	if !e.Applies(r, Session{}) {
		return r, Report{}
	}
	edits := routineEffortEdits(r)
	if len(edits) == 0 {
		return r, Report{}
	}
	out, err := r.Apply(edits)
	if err != nil {
		return r, Report{Err: err.Error()}
	}
	return out, Report{Name: OptimizerEffortRoutinePrompts, Betas: []string{PerMessageEffortBeta}}
}

// routineEffortEdits inserts an effort change after each user turn with new input whose level differs from the one
// before it: low after a routine prompt, the request's own level after the next prompt that is not routine.
func routineEffortEdits(r *Request) []Edit {
	top := r.Effort
	level := top
	var edits []Edit
	for _, m := range r.Messages {
		if m.Role != "user" || toolResultsOnly(m) {
			continue
		}
		want := top
		if text, ok := promptText(r.Raw, m); ok && routine.Classify(text).Routine {
			want = RoutinePromptEffort
		}
		if want == level {
			continue
		}
		level = want
		ins := append([]byte(`,{"role":"system","content":[],"output_config":{"effort":`), EncodeString(want)...)
		ins = append(ins, "}}"...)
		edits = append(edits, Edit{Span: Span{m.Span.E, m.Span.E}, New: ins})
	}
	return edits
}

func toolResultsOnly(m Message) bool {
	if len(m.Blocks) == 0 {
		return false
	}
	for _, b := range m.Blocks {
		if b.Type != "tool_result" {
			return false
		}
	}
	return true
}

// promptText is what the person typed in a user message: its string content, or its one text block that is not a
// <system-reminder> the agent added. Anything else (no text, two texts, an image or a tool result beside it) is
// not a prompt the classifier may judge, so it reads as not routine.
func promptText(raw []byte, m Message) (string, bool) {
	var msg struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw[m.Span.S:m.Span.E], &msg) != nil {
		return "", false
	}
	var s string
	if json.Unmarshal(msg.Content, &s) == nil {
		return s, true
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(msg.Content, &blocks) != nil {
		return "", false
	}
	var texts []string
	for _, b := range blocks {
		if b.Type != "text" {
			return "", false
		}
		if strings.HasPrefix(strings.TrimSpace(b.Text), "<system-reminder>") {
			continue
		}
		texts = append(texts, b.Text)
	}
	if len(texts) != 1 {
		return "", false
	}
	return texts[0], true
}
