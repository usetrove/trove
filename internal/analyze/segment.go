package analyze

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"regexp"
	"strings"
)

// Categories token estimates are grouped into. Tool results are further split
// by tool name as "tool_results:<name>".
const (
	CatSystem      = "system"
	CatContextFile = "context_files"
	CatToolSchemas = "tool_schemas"
	CatToolResults = "tool_results"
	CatHistory     = "history"
)

// Segment kinds.
const (
	KindToolDef       = "tool_def"
	KindSystem        = "system"
	KindUserText      = "user_text"
	KindAssistantText = "assistant_text"
	KindToolCall      = "tool_call"
	KindToolResult    = "tool_result"
	KindOther         = "other" // images, thinking blocks, anything not recognized
)

type hash [16]byte

func sum(parts ...[]byte) hash {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
		h.Write([]byte{0})
	}
	var out hash
	copy(out[:], h.Sum(nil))
	return out
}

// Segment is one ordered piece of a request. text is in memory only: the
// detectors read it and it is dropped with the Parsed value; it is never
// stored and never copied into session state.
type Segment struct {
	Kind     string
	Category string
	Tool     string // tool name for tool defs, calls and results
	Hash     hash   // identity in the cache prefix (includes call ids)
	Content  hash   // identity of the content alone (ignores call ids), for duplicates
	Bytes    int
	Msg      int // message index, -1 for system and tool definitions
	IsError  bool

	text string
}

// Parsed is a request split into segments in cache order: tools, system, messages.
type Parsed struct {
	Provider     string
	Model        string
	Segments     []Segment
	CacheMarkers bool // the request asks for caching explicitly (Anthropic cache_control)
	OneHourTTL   bool
	Messages     int
}

// contextFileRe finds agent context files (project instructions) in system
// prompts and the first user messages. Detection is by marker, so it is a
// best effort and the estimate built on it is labeled as such. It deliberately
// does not match a bare mention of CLAUDE.md, which agents' own system
// prompts contain.
var contextFileRe = regexp.MustCompile(`(?i)contents of [^\n]{0,200}(claude|agents|gemini)\.md|# claudemd|<project_instructions>|<user_instructions>|<agents_md>|\.cursorrules|copilot-instructions\.md`)

func isContextFile(text string) bool {
	if len(text) < 200 {
		return false
	}
	head := text
	if len(head) > 4096 {
		head = head[:4096]
	}
	return contextFileRe.MatchString(head)
}

// Parse splits a request body. It returns ok=false for endpoints and shapes it
// does not understand; callers then skip analysis.
func Parse(provider, path string, body []byte) (*Parsed, bool) {
	p := strings.TrimSuffix(path, "/")
	switch {
	case provider == "anthropic" && strings.HasSuffix(p, "/v1/messages"):
		return parseAnthropic(body)
	case provider == "openai" && strings.HasSuffix(p, "/chat/completions"):
		return parseChat(body)
	case provider == "openai" && strings.HasSuffix(p, "/responses"):
		return parseResponses(body)
	}
	return nil, false
}

type builder struct {
	p        *Parsed
	toolName map[string]string // call id -> tool name
}

func (b *builder) add(s Segment) {
	s.Bytes = len(s.text)
	b.p.Segments = append(b.p.Segments, s)
}

func (b *builder) text(kind, role string, msg int, text string) {
	s := Segment{Kind: kind, Msg: msg, text: text, Category: CatHistory}
	if kind == KindSystem {
		s.Category = CatSystem
	}
	if (kind == KindSystem || (msg <= 1 && (role == "user" || role == "developer"))) && isContextFile(text) {
		s.Category = CatContextFile
	}
	s.Hash = sum([]byte(kind), []byte(role), []byte(text))
	s.Content = s.Hash
	b.add(s)
}

func (b *builder) toolCall(msg int, id, name string, args []byte) {
	if id != "" {
		b.toolName[id] = name
	}
	s := Segment{Kind: KindToolCall, Category: CatHistory, Tool: name, Msg: msg, text: string(args)}
	s.Hash = sum([]byte(KindToolCall), []byte(id), []byte(name), args)
	s.Content = sum([]byte(KindToolCall), []byte(name), args)
	b.add(s)
}

func (b *builder) toolResult(msg int, id, text string, isErr bool) {
	name := b.toolName[id]
	s := Segment{Kind: KindToolResult, Category: CatToolResults, Tool: name, Msg: msg, text: text, IsError: isErr}
	s.Hash = sum([]byte(KindToolResult), []byte(id), []byte(text))
	s.Content = sum([]byte(KindToolResult), []byte(text))
	b.add(s)
}

func (b *builder) other(msg int, raw []byte) {
	s := Segment{Kind: KindOther, Category: CatHistory, Msg: msg, Bytes: len(raw)}
	s.Hash = sum([]byte(KindOther), raw)
	s.Content = s.Hash
	b.p.Segments = append(b.p.Segments, s)
}

// toolDef adds a tool definition, ignoring cache_control so moving a
// breakpoint does not look like a changed tool.
func (b *builder) toolDef(raw json.RawMessage, name string) {
	norm := stripKey(raw, "cache_control")
	s := Segment{Kind: KindToolDef, Category: CatToolSchemas, Tool: name, Msg: -1, Bytes: len(norm)}
	s.Hash = sum([]byte(KindToolDef), norm)
	s.Content = s.Hash
	b.p.Segments = append(b.p.Segments, s)
}

// stripKey removes a top-level key and re-encodes with sorted keys, so the
// result is a deterministic function of the content.
func stripKey(raw []byte, key string) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return raw
	}
	if _, ok := m[key]; !ok {
		return raw
	}
	delete(m, key)
	out, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return out
}

// textOf returns the text of a content value that is a string or an array of
// {type:"text", text} blocks.
func textOf(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var sb strings.Builder
	for _, bl := range blocks {
		sb.WriteString(bl.Text)
	}
	return sb.String()
}

func parseAnthropic(body []byte) (*Parsed, bool) {
	var req struct {
		Model    string            `json:"model"`
		System   json.RawMessage   `json:"system"`
		Tools    []json.RawMessage `json:"tools"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &req) != nil || req.Model == "" || len(req.Messages) == 0 {
		return nil, false
	}
	p := &Parsed{Provider: "anthropic", Model: req.Model, Messages: len(req.Messages)}
	p.CacheMarkers = bytes.Contains(body, []byte(`"cache_control"`))
	p.OneHourTTL = bytes.Contains(body, []byte(`"ttl":"1h"`)) || bytes.Contains(body, []byte(`"ttl": "1h"`))
	b := &builder{p: p, toolName: map[string]string{}}

	for _, t := range req.Tools {
		var h struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(t, &h)
		b.toolDef(t, h.Name)
	}
	var sysBlocks []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(req.System, &sysBlocks) == nil && len(sysBlocks) > 0 {
		for _, sb := range sysBlocks {
			b.text(KindSystem, "system", -1, sb.Text)
		}
	} else if s := textOf(req.System); s != "" {
		b.text(KindSystem, "system", -1, s)
	}

	for i, m := range req.Messages {
		kind := KindUserText
		if m.Role == "assistant" {
			kind = KindAssistantText
		}
		var s string
		if json.Unmarshal(m.Content, &s) == nil {
			b.text(kind, m.Role, i, s)
			continue
		}
		var blocks []json.RawMessage
		if json.Unmarshal(m.Content, &blocks) != nil {
			continue
		}
		for _, raw := range blocks {
			var bl struct {
				Type      string          `json:"type"`
				Text      string          `json:"text"`
				ID        string          `json:"id"`
				Name      string          `json:"name"`
				Input     json.RawMessage `json:"input"`
				ToolUseID string          `json:"tool_use_id"`
				Content   json.RawMessage `json:"content"`
				IsError   bool            `json:"is_error"`
			}
			if json.Unmarshal(raw, &bl) != nil {
				continue
			}
			switch bl.Type {
			case "text":
				b.text(kind, m.Role, i, bl.Text)
			case "tool_use":
				b.toolCall(i, bl.ID, bl.Name, bl.Input)
			case "tool_result":
				b.toolResult(i, bl.ToolUseID, textOf(bl.Content), bl.IsError)
			default:
				b.other(i, stripKey(raw, "cache_control"))
			}
		}
	}
	return p, true
}

func parseChat(body []byte) (*Parsed, bool) {
	var req struct {
		Model    string            `json:"model"`
		Tools    []json.RawMessage `json:"tools"`
		Messages []struct {
			Role       string          `json:"role"`
			Content    json.RawMessage `json:"content"`
			ToolCallID string          `json:"tool_call_id"`
			ToolCalls  []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &req) != nil || req.Model == "" || len(req.Messages) == 0 {
		return nil, false
	}
	p := &Parsed{Provider: "openai", Model: req.Model, Messages: len(req.Messages)}
	b := &builder{p: p, toolName: map[string]string{}}
	for _, t := range req.Tools {
		var h struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		_ = json.Unmarshal(t, &h)
		b.toolDef(t, h.Function.Name)
	}
	for i, m := range req.Messages {
		switch m.Role {
		case "system", "developer":
			b.text(KindSystem, "system", -1, textOf(m.Content))
		case "tool":
			b.toolResult(i, m.ToolCallID, textOf(m.Content), false)
		case "assistant":
			if t := textOf(m.Content); t != "" {
				b.text(KindAssistantText, m.Role, i, t)
			}
			for _, tc := range m.ToolCalls {
				b.toolCall(i, tc.ID, tc.Function.Name, []byte(tc.Function.Arguments))
			}
		default:
			b.text(KindUserText, m.Role, i, textOf(m.Content))
		}
	}
	return p, true
}

func parseResponses(body []byte) (*Parsed, bool) {
	var req struct {
		Model        string            `json:"model"`
		Instructions string            `json:"instructions"`
		Tools        []json.RawMessage `json:"tools"`
		Input        json.RawMessage   `json:"input"`
	}
	if json.Unmarshal(body, &req) != nil || req.Model == "" || len(req.Input) == 0 {
		return nil, false
	}
	p := &Parsed{Provider: "openai", Model: req.Model}
	b := &builder{p: p, toolName: map[string]string{}}
	for _, t := range req.Tools {
		var h struct {
			Name string `json:"name"`
			Type string `json:"type"`
		}
		_ = json.Unmarshal(t, &h)
		if h.Name == "" {
			h.Name = h.Type
		}
		b.toolDef(t, h.Name)
	}
	if req.Instructions != "" {
		b.text(KindSystem, "system", -1, req.Instructions)
	}
	var s string
	if json.Unmarshal(req.Input, &s) == nil {
		p.Messages = 1
		b.text(KindUserText, "user", 0, s)
		return p, true
	}
	var items []json.RawMessage
	if json.Unmarshal(req.Input, &items) != nil {
		return nil, false
	}
	p.Messages = len(items)
	for i, raw := range items {
		var it struct {
			Type      string          `json:"type"`
			Role      string          `json:"role"`
			Content   json.RawMessage `json:"content"`
			Name      string          `json:"name"`
			CallID    string          `json:"call_id"`
			Arguments string          `json:"arguments"`
			Output    json.RawMessage `json:"output"`
		}
		if json.Unmarshal(raw, &it) != nil {
			continue
		}
		switch {
		case it.Type == "function_call":
			b.toolCall(i, it.CallID, it.Name, []byte(it.Arguments))
		case it.Type == "function_call_output":
			b.toolResult(i, it.CallID, textOf(it.Output), false)
		case it.Role == "system" || it.Role == "developer":
			b.text(KindSystem, "system", -1, textOf(it.Content))
		case it.Role == "assistant":
			b.text(KindAssistantText, "assistant", i, textOf(it.Content))
		case it.Role != "":
			b.text(KindUserText, it.Role, i, textOf(it.Content))
		default:
			b.other(i, raw)
		}
	}
	return p, true
}
