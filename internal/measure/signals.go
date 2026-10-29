package measure

import (
	"crypto/sha256"
	"encoding/json"
	"strings"

	"github.com/usetrove/trove/internal/optimize"
)

// Signals are outcome counts for one request's conversation so far. Because an
// agent resends its whole history each turn, the values of a session's last
// request describe the whole session. Only integers are kept: tool names,
// inputs, results and their hashes are used in memory and never stored.
type Signals struct {
	Messages      int `json:"messages"`       // entries in the conversation
	ToolCalls     int `json:"tool_calls"`     // tool_use blocks
	DistinctCalls int `json:"distinct_calls"` // distinct (tool, input) pairs
	ToolErrors    int `json:"tool_errors"`    // tool_result blocks marked is_error
	Collapsed     int `json:"collapsed"`      // results an optimizer replaced with a note
	Refetches     int `json:"refetches"`      // calls repeated after their result was collapsed
}

// Repeats is how many tool calls repeated an identical earlier call.
func (s Signals) Repeats() int { return s.ToolCalls - s.DistinctCalls }

// JSON encodes s for the requests.signals column.
func (s Signals) JSON() []byte {
	b, err := json.Marshal(s)
	if err != nil {
		return nil
	}
	return b
}

type convBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
}

type convMessage struct {
	Content json.RawMessage `json:"content"`
}

// Extract reads an Anthropic Messages request body (the one forwarded upstream,
// so collapsed results are visible) and counts outcome signals. ok is false for
// a body it cannot read, in which case nothing is recorded. It is meant for the
// recording goroutine, never the request path.
func Extract(body []byte) (s Signals, ok bool) {
	if len(body) == 0 || len(body) > optimize.MaxBody {
		return s, false
	}
	var top struct {
		Messages []convMessage `json:"messages"`
	}
	if json.Unmarshal(body, &top) != nil || top.Messages == nil {
		return s, false
	}
	s.Messages = len(top.Messages)
	hashOf := map[string][32]byte{} // tool_use id -> call hash
	seen := map[[32]byte]bool{}
	collapsed := map[[32]byte]bool{}
	for _, m := range top.Messages {
		var blocks []convBlock
		if json.Unmarshal(m.Content, &blocks) != nil {
			continue // plain-string content holds no tool blocks
		}
		for _, b := range blocks {
			switch b.Type {
			case "tool_use":
				h := sha256.Sum256(append([]byte(b.Name+"\x00"), compact(b.Input)...))
				if collapsed[h] {
					s.Refetches++
				}
				hashOf[b.ID] = h
				seen[h] = true
				s.ToolCalls++
			case "tool_result":
				if b.IsError {
					s.ToolErrors++
				}
				if isCollapsed(b.Content) {
					s.Collapsed++
					if h, found := hashOf[b.ToolUseID]; found {
						collapsed[h] = true
					}
				}
			}
		}
	}
	s.DistinctCalls = len(seen)
	return s, true
}

func compact(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return raw
	}
	// Re-marshal sorts map keys, so equal inputs hash equally whatever their key order.
	b, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return b
}

// isCollapsed reports whether a tool_result's content is the note the
// duplicate-result collapser leaves behind.
func isCollapsed(content json.RawMessage) bool {
	var s string
	if json.Unmarshal(content, &s) == nil {
		return strings.HasPrefix(s, optimize.CollapsedPrefix)
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &parts) == nil && len(parts) > 0 {
		return strings.HasPrefix(parts[0].Text, optimize.CollapsedPrefix)
	}
	return false
}
