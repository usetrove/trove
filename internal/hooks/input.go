package hooks

import (
	"encoding/json"
	"github.com/usetrove/trove/internal/optimize"
	"io"
)

// maxInput caps how much of stdin is read. Hook input is small; this only stops
// a runaway writer from holding the hook open.
const maxInput = 1 << 20

// Input is the part of an agent's hook input that Trove uses. Claude Code and
// Codex CLI send the same field names for the events Trove registers
// (code.claude.com/docs/en/hooks and learn.chatgpt.com/docs/hooks, checked
// 2026-10-29/30); fields not listed here are ignored.
type Input struct {
	SessionID string `json:"session_id"`
	PromptID  string `json:"prompt_id"` // Claude Code
	TurnID    string `json:"turn_id"`   // Codex
	Event     string `json:"hook_event_name"`
	Cwd       string `json:"cwd"` // a common input field (code.claude.com/docs/en/hooks, checked 2026-11-05); only hashed, never stored
	ToolName  string `json:"tool_name"`
	// ToolUseID is the call's id, a PreToolUse field (code.claude.com/docs/en/hooks.md, read 2026-11-24). Only
	// its hash is stored.
	ToolUseID string `json:"tool_use_id"`
	// TranscriptPath is the session's transcript file (a common input field, seen live 2026-11-18 on Claude Code
	// 2.1.327). Only the model of the newest main-thread reply is read from it, in memory; it is never stored.
	TranscriptPath string `json:"transcript_path"`
	Reason         string `json:"reason"`
	Source         string `json:"source"`
	// Model is the session's model, sent with SessionStart (code.claude.com/docs/en/hooks, checked 2026-11-17).
	// Only its family is used, to pick the learned bytes-per-token ratio; it is never stored.
	Model string `json:"model"`
	// ToolResponse is the tool's output in PostToolUse events.
	ToolResponse json.RawMessage `json:"tool_response"`
	ToolInput    ToolInput       `json:"tool_input"`
	// Error and IsInterrupt come with PostToolUseFailure. For Bash, a command that ran and exited
	// non-zero has "Exit code N" as the first line of Error; IsInterrupt is true for an abort
	// (code.claude.com/docs/en/hooks, read 2026-11-17). Error is read in memory only, never stored.
	Error       string `json:"error"`
	IsInterrupt bool   `json:"is_interrupt"`
	// SessionStart sends these when source is "resume" or "fork" and the transcript has a response (Claude Code
	// v2.1.251 or later; code.claude.com/docs/en/hooks, read 2026-11-23). Only numbers are kept, for the cold-resume
	// guard.
	ContextTokens            *int64   `json:"context_tokens"`
	PromptCacheLikelyExpired *bool    `json:"prompt_cache_likely_expired"`
	EstimatedCacheWriteUSD   *float64 `json:"estimated_cache_write_usd"`
	// PreModelSwitch and PostModelSwitch (read 2026-11-24): the models, whether the current model's cache is
	// likely warm and its lifetime. context_tokens, estimated_cache_write_usd and source share the fields above.
	FromModel       string `json:"from_model"`
	ToModel         string `json:"to_model"`
	PromptCacheWarm *bool  `json:"prompt_cache_warm"`
	CacheTTL        string `json:"cache_ttl"`
	// Trigger is PreCompact's "manual" (/compact) or "auto".
	Trigger string `json:"trigger"`
	// PromptText is UserPromptSubmit's prompt text. Only hook_routine_steer reads it, in memory, through
	// routine.Classify; it is never stored, logged, hashed or written to the spool (rule 7).
	PromptText string `json:"prompt"`
	// SubagentStart and SubagentStop (code.claude.com/docs/en/hooks, read 2026-11-24). The id is hashed and the
	// type stored only as tasks.AgentTypeLabel; LastAssistantMessage is read in memory for its STATUS line by
	// tasks.ParseStatus and never stored, logged or hashed (rule 7).
	AgentID              string `json:"agent_id"`
	AgentType            string `json:"agent_type"`
	LastAssistantMessage string `json:"last_assistant_message"`
}

// ToolInput is the tool's arguments. The named fields are the ones Trove reads.
// Raw is the whole object byte for byte, because updatedInput replaces the entire
// input and a rewrite must carry every other field over unchanged.
type ToolInput struct {
	FilePath string  `json:"file_path"`
	Offset   lenient `json:"offset"`
	Limit    lenient `json:"limit"`
	Pages    string  `json:"pages"`
	Command  string  `json:"command"`
	// SubagentType names the subagent in an Agent (formerly Task) call; read 2026-11-17 at
	// code.claude.com/docs/en/hooks.
	SubagentType string          `json:"subagent_type"`
	Raw          json.RawMessage `json:"-"`
}

// UnmarshalJSON implements json.Unmarshaler.
func (t *ToolInput) UnmarshalJSON(b []byte) error {
	type plain ToolInput // no methods, so no recursion
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*t = ToolInput(p)
	t.Raw = append(json.RawMessage(nil), b...)
	return nil
}

// Prompt is the id that groups the tool calls of one user prompt.
func (in Input) Prompt() string {
	if in.PromptID != "" {
		return in.PromptID
	}
	return in.TurnID
}

type lenient = optimize.LenientInt

// ParseInput reads one hook input from r.
func ParseInput(r io.Reader) (Input, error) {
	var in Input
	err := json.NewDecoder(io.LimitReader(r, maxInput)).Decode(&in)
	return in, err
}
