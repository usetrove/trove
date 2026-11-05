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
	ToolName  string `json:"tool_name"`
	Reason    string `json:"reason"`
	Source    string `json:"source"`
	// ToolResponse is the tool's output in PostToolUse events.
	ToolResponse json.RawMessage `json:"tool_response"`
	ToolInput    ToolInput       `json:"tool_input"`
}

// ToolInput is the tool's arguments. The named fields are the ones Trove reads.
// Raw is the whole object byte for byte, because updatedInput replaces the entire
// input and a rewrite must carry every other field over unchanged.
type ToolInput struct {
	FilePath string          `json:"file_path"`
	Offset   lenient         `json:"offset"`
	Limit    lenient         `json:"limit"`
	Pages    string          `json:"pages"`
	Command  string          `json:"command"`
	Raw      json.RawMessage `json:"-"`
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
