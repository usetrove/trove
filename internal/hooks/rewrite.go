package hooks

import (
	"encoding/json"
	"strings"

	"github.com/usetrove/trove/internal/optimize"
)

// updatedInputJSON is PreToolUse output that replaces the tool's input. The
// whole input object is sent, because updatedInput replaces it entirely
// (code.claude.com/docs/en/hooks, read 2026-11-04). No permissionDecision is
// set: the user's normal permission flow still applies to the rewritten call, and
// a hook must never approve what the user would have been asked about. note, if
// any, is shown to the model as additional context.
func updatedInputJSON(input json.RawMessage, note string) []byte {
	out := map[string]any{"hookEventName": "PreToolUse", "updatedInput": input}
	if note != "" {
		out["additionalContext"] = note
	}
	b, _ := json.Marshal(map[string]any{"hookSpecificOutput": out})
	return b
}

// updatedOutputJSON is PostToolUse output that replaces the tool's output with
// response, which must have the tool's own output shape.
func updatedOutputJSON(response json.RawMessage) []byte {
	b, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": "PostToolUse", "updatedToolOutput": response}})
	return b
}

// responseKeys are the tool_response fields that carry text the output
// optimizers may shorten. Anything else in the response is carried over as is.
var responseKeys = []string{"stdout", "stderr", "content"}

// responseTexts returns the text fields of a tool_response in a fixed order and
// whether the response is an object. A bare string response yields one text with
// an empty key. ok is false for a shape this adapter does not know.
func responseTexts(raw json.RawMessage) (keys, texts []string, ok bool) {
	if len(raw) == 0 {
		return nil, nil, false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []string{""}, []string{s}, true
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil, nil, false
	}
	for _, k := range responseKeys {
		var t string
		if v, has := m[k]; has && json.Unmarshal(v, &t) == nil && t != "" {
			keys, texts = append(keys, k), append(texts, t)
		}
	}
	return keys, texts, len(keys) > 0
}

// withResponseTexts returns raw with the text fields named by keys replaced by
// texts and every other field unchanged, so the result keeps the tool's shape.
func withResponseTexts(raw json.RawMessage, keys, texts []string) (json.RawMessage, bool) {
	if len(keys) != len(texts) || len(keys) == 0 {
		return raw, false
	}
	if keys[0] == "" {
		b, err := json.Marshal(texts[0])
		return b, err == nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return raw, false
	}
	for i, k := range keys {
		b, err := json.Marshal(texts[i])
		if err != nil {
			return raw, false
		}
		m[k] = b
	}
	out, err := json.Marshal(m)
	return out, err == nil
}

// outputForEngine is the response as neutral parts, one per text field, so the
// engine's result can be mapped back by position.
func outputForEngine(texts []string) optimize.ToolOutput {
	var out optimize.ToolOutput
	for _, t := range texts {
		out.Parts = append(out.Parts, optimize.Part{Text: t})
	}
	return out
}

// interrupted reports whether a Bash response says the command was interrupted;
// such output is never rewritten.
func interrupted(raw json.RawMessage) bool {
	var m struct {
		Interrupted bool `json:"interrupted"`
	}
	return json.Unmarshal(raw, &m) == nil && m.Interrupted
}

func joinNotes(notes []string) string { return strings.Join(notes, " ") }
