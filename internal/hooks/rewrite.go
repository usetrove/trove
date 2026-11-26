package hooks

import (
	"encoding/json"
	"strconv"
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
// an empty key. An MCP result given as a list of content blocks, bare or under
// "content", yields one text per block, keyed "#i" or "content#i". ok is false for
// a shape this adapter does not know, and for a block list with anything but text
// in it (an image must reach the model as it is).
func responseTexts(raw json.RawMessage) (keys, texts []string, ok bool) {
	if len(raw) == 0 {
		return nil, nil, false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []string{""}, []string{s}, true
	}
	if t, ok := blockTexts(raw); ok {
		for i, x := range t {
			keys, texts = append(keys, blockKey("", i)), append(texts, x)
		}
		return keys, texts, len(keys) > 0
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil, nil, false
	}
	if _, has := m["structuredContent"]; has {
		return nil, nil, false // the model may see the structured copy too; leave both alone
	}
	if v, has := m["content"]; has {
		if t, ok := blockTexts(v); ok {
			for i, x := range t {
				keys, texts = append(keys, blockKey("content", i)), append(texts, x)
			}
			return keys, texts, len(keys) > 0
		}
	}
	for _, k := range responseKeys {
		var t string
		if v, has := m[k]; has && json.Unmarshal(v, &t) == nil && t != "" {
			keys, texts = append(keys, k), append(texts, t)
		}
	}
	return keys, texts, len(keys) > 0
}

// mcpBlock is one MCP content block. Only "text" blocks are ever rewritten.
type mcpBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// blockTexts reads a JSON array of MCP content blocks. ok is false unless it is a
// non-empty array of text blocks only.
func blockTexts(raw json.RawMessage) ([]string, bool) {
	var blocks []json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil || len(blocks) == 0 {
		return nil, false
	}
	out := make([]string, len(blocks))
	for i, b := range blocks {
		var c mcpBlock
		if json.Unmarshal(b, &c) != nil || c.Type != "text" {
			return nil, false
		}
		out[i] = c.Text
	}
	return out, true
}

// blockKey names block i of the block list at field (or of the bare list when field is "").
func blockKey(field string, i int) string { return field + "#" + strconv.Itoa(i) }

// splitBlockKey reverses blockKey. ok is false for a plain field key.
func splitBlockKey(key string) (field string, i int, ok bool) {
	at := strings.LastIndexByte(key, '#')
	if at < 0 {
		return key, 0, false
	}
	n, err := strconv.Atoi(key[at+1:])
	return key[:at], n, err == nil && n >= 0
}

// withBlockTexts sets the text of the listed blocks in a block list, keeping every
// other field of each block.
func withBlockTexts(raw json.RawMessage, at map[int]string) (json.RawMessage, bool) {
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return raw, false
	}
	for i, t := range at {
		if i >= len(blocks) {
			return raw, false
		}
		b, err := json.Marshal(t)
		if err != nil {
			return raw, false
		}
		blocks[i]["text"] = b
	}
	out, err := json.Marshal(blocks)
	return out, err == nil
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
	if field, _, isBlock := splitBlockKey(keys[0]); isBlock {
		at := map[int]string{}
		for i, k := range keys {
			f, n, ok := splitBlockKey(k)
			if !ok || f != field {
				return raw, false
			}
			at[n] = texts[i]
		}
		if field == "" {
			return withBlockTexts(raw, at)
		}
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) != nil {
			return raw, false
		}
		list, ok := withBlockTexts(m[field], at)
		if !ok {
			return raw, false
		}
		m[field] = list
		out, err := json.Marshal(m)
		return out, err == nil
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

// withReadContent returns a Read tool_response with file.content replaced and
// every other field, inside file and out, unchanged. ok is false for a response
// that is not in the Read shape.
func withReadContent(raw json.RawMessage, content string) (json.RawMessage, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || m["file"] == nil {
		return raw, false
	}
	var f map[string]json.RawMessage
	if json.Unmarshal(m["file"], &f) != nil || f["content"] == nil {
		return raw, false
	}
	var old string
	if json.Unmarshal(f["content"], &old) != nil {
		return raw, false
	}
	c, err := json.Marshal(content)
	if err != nil {
		return raw, false
	}
	f["content"] = c
	fb, err := json.Marshal(f)
	if err != nil {
		return raw, false
	}
	m["file"] = fb
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
