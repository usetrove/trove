package optimize

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// This file is a small, byte-preserving view of an Anthropic Messages request.
//
// Optimizers must not re-serialize a request: that could reorder keys or
// change whitespace in parts they never touched, and a single changed byte in a
// cached prefix is a cache miss. Instead the request is scanned once for the
// byte offsets of the parts optimizers care about, and a change is expressed as
// an Edit that replaces one span. Everything outside the edited spans is copied
// through verbatim.

// Span is a half-open byte range [S, E) of the request body.
type Span struct{ S, E int }

// Len returns the number of bytes in the span.
func (s Span) Len() int { return s.E - s.S }

// Edit replaces the bytes of Span with New.
type Edit struct {
	Span
	New []byte
}

// Block is one content block of a message.
type Block struct {
	Span      Span
	Type      string
	ToolUseID string
	IsError   bool
	// Content is the value of a tool_result's "content" field (zero if absent).
	Content Span
	// Texts are the JSON string values that make up a tool_result's text: the
	// content string itself, or the "text" field of each text block in a
	// content array.
	Texts []Span
	// TextOnly is true when a tool_result's content is a string or an array
	// holding only text blocks, so it can be compared and replaced as text.
	TextOnly bool
}

// SystemBlock is one element of an array-form system prompt.
type SystemBlock struct {
	Span Span
	Type string
	// Text is the JSON string value of a text block (zero otherwise).
	Text Span
}

// Message is one entry of the messages array.
type Message struct {
	Span   Span
	Role   string
	Blocks []Block // empty when content is a plain string
}

// Request is a parsed Anthropic Messages request. Raw is never modified; Apply
// returns a new Request.
type Request struct {
	Raw    []byte
	Model  string
	System Span // zero if absent
	Tools  Span // zero if absent
	// SystemBlocks is set when system is an array; a string system leaves it nil.
	SystemBlocks []SystemBlock
	// ToolSpans are the elements of the tools array.
	ToolSpans []Span
	Messages  []Message
	// Root is the span of the top-level object, [S, E) with E one past '}'.
	Root Span
	// RootClose is the offset of the closing '}' of the root object.
	RootClose int
	// RootEmpty is true when the root object has no fields.
	RootEmpty bool
	// HasCacheControl is true if the body mentions cache_control anywhere. It is
	// a byte search on purpose: it can only err toward "markers present".
	HasCacheControl bool
}

var errMalformed = errors.New("malformed request")

// ParseRequest scans body. It does not validate JSON in full; anything it
// cannot walk returns an error and the caller forwards the original request.
func ParseRequest(body []byte) (*Request, error) {
	r := &Request{Raw: body}
	i := skipWS(body, 0)
	if i >= len(body) || body[i] != '{' {
		return nil, fmt.Errorf("%w: body is not a JSON object", errMalformed)
	}
	end, err := scanValue(body, i)
	if err != nil {
		return nil, err
	}
	r.Root = Span{i, end}
	r.RootClose = end - 1
	r.HasCacheControl = bytes.Contains(body, []byte(`"cache_control"`))

	var msgs Span
	n := 0
	err = eachField(body, r.Root, func(key []byte, val Span) error {
		n++
		switch string(key) {
		case "model":
			if body[val.S] == '"' {
				r.Model, _ = decodeString(body[val.S:val.E])
			}
		case "system":
			r.System = val
		case "tools":
			r.Tools = val
		case "messages":
			msgs = val
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	r.RootEmpty = n == 0
	if r.System.E != 0 && body[r.System.S] == '[' {
		err = eachElem(body, r.System, func(sp Span) error {
			blk := SystemBlock{Span: sp}
			if body[sp.S] == '{' {
				if err := eachField(body, sp, func(key []byte, val Span) error {
					switch string(key) {
					case "type":
						if body[val.S] == '"' {
							blk.Type, _ = decodeString(body[val.S:val.E])
						}
					case "text":
						if body[val.S] == '"' {
							blk.Text = val
						}
					}
					return nil
				}); err != nil {
					return err
				}
			}
			r.SystemBlocks = append(r.SystemBlocks, blk)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if r.Tools.E != 0 && body[r.Tools.S] == '[' {
		if err = eachElem(body, r.Tools, func(sp Span) error {
			r.ToolSpans = append(r.ToolSpans, sp)
			return nil
		}); err != nil {
			return nil, err
		}
	}
	if msgs.E == 0 || body[msgs.S] != '[' {
		return r, nil // no messages array: nothing for message optimizers to do
	}
	err = eachElem(body, msgs, func(m Span) error {
		msg, err := parseMessage(body, m)
		if err != nil {
			return err
		}
		r.Messages = append(r.Messages, msg)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return r, nil
}

func parseMessage(body []byte, sp Span) (Message, error) {
	msg := Message{Span: sp}
	if body[sp.S] != '{' {
		return msg, nil
	}
	var content Span
	err := eachField(body, sp, func(key []byte, val Span) error {
		switch string(key) {
		case "role":
			if body[val.S] == '"' {
				msg.Role, _ = decodeString(body[val.S:val.E])
			}
		case "content":
			content = val
		}
		return nil
	})
	if err != nil {
		return msg, err
	}
	if content.E == 0 || body[content.S] != '[' {
		return msg, nil
	}
	err = eachElem(body, content, func(b Span) error {
		blk, err := parseBlock(body, b)
		if err != nil {
			return err
		}
		msg.Blocks = append(msg.Blocks, blk)
		return nil
	})
	return msg, err
}

func parseBlock(body []byte, sp Span) (Block, error) {
	blk := Block{Span: sp}
	if body[sp.S] != '{' {
		return blk, nil
	}
	err := eachField(body, sp, func(key []byte, val Span) error {
		switch string(key) {
		case "type":
			if body[val.S] == '"' {
				blk.Type, _ = decodeString(body[val.S:val.E])
			}
		case "tool_use_id":
			if body[val.S] == '"' {
				blk.ToolUseID, _ = decodeString(body[val.S:val.E])
			}
		case "is_error":
			blk.IsError = bytes.Equal(body[val.S:val.E], []byte("true"))
		case "content":
			blk.Content = val
		}
		return nil
	})
	if err != nil || blk.Type != "tool_result" || blk.Content.E == 0 {
		return blk, err
	}
	switch body[blk.Content.S] {
	case '"':
		blk.Texts = []Span{blk.Content}
		blk.TextOnly = true
	case '[':
		blk.TextOnly = true
		err = eachElem(body, blk.Content, func(c Span) error {
			if body[c.S] != '{' {
				blk.TextOnly = false
				return nil
			}
			var typ string
			var text Span
			if err := eachField(body, c, func(key []byte, val Span) error {
				switch string(key) {
				case "type":
					if body[val.S] == '"' {
						typ, _ = decodeString(body[val.S:val.E])
					}
				case "text":
					text = val
				}
				return nil
			}); err != nil {
				return err
			}
			if typ == "text" && text.E != 0 && body[text.S] == '"' {
				blk.Texts = append(blk.Texts, text)
			} else {
				blk.TextOnly = false
			}
			return nil
		})
	default:
		// null or an object: not something we rewrite.
	}
	return blk, err
}

// Apply returns a new Request with edits applied. Edits must not overlap.
func (r *Request) Apply(edits []Edit) (*Request, error) {
	if len(edits) == 0 {
		return r, nil
	}
	sorted := append([]Edit(nil), edits...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].S < sorted[j].S })
	size := len(r.Raw)
	prev := 0
	for _, e := range sorted {
		if e.S < prev || e.E < e.S || e.E > len(r.Raw) {
			return nil, errors.New("optimizer produced overlapping or out-of-range edits")
		}
		prev = e.E
		size += len(e.New) - e.Len()
	}
	out := make([]byte, 0, size)
	at := 0
	for _, e := range sorted {
		out = append(out, r.Raw[at:e.S]...)
		out = append(out, e.New...)
		at = e.E
	}
	out = append(out, r.Raw[at:]...)
	return ParseRequest(out)
}

// DecodeString decodes the JSON string at sp.
func (r *Request) DecodeString(sp Span) (string, bool) { return decodeString(r.Raw[sp.S:sp.E]) }

func decodeString(b []byte) (string, bool) {
	// Fast path: no escapes.
	if len(b) >= 2 && b[0] == '"' && bytes.IndexByte(b, '\\') < 0 {
		return string(b[1 : len(b)-1]), true
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return "", false
	}
	return s, true
}

// EncodeString returns s as a JSON string without HTML escaping. Go's encoder
// is deterministic, so equal strings always produce equal bytes.
func EncodeString(s string) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // cannot fail for a string
	return bytes.TrimRight(buf.Bytes(), "\n")
}

// --- minimal JSON scanner ---------------------------------------------------

func skipWS(b []byte, i int) int {
	for i < len(b) {
		switch b[i] {
		case ' ', '\t', '\r', '\n':
			i++
		default:
			return i
		}
	}
	return i
}

// scanValue returns the offset one past the JSON value starting at b[i].
func scanValue(b []byte, i int) (int, error) {
	if i >= len(b) {
		return 0, errMalformed
	}
	switch b[i] {
	case '"':
		return scanString(b, i)
	case '{', '[':
		var buf [64]byte
		stack := buf[:0] // expected closers, so "[}" is rejected
		for j := i; j < len(b); j++ {
			switch b[j] {
			case '"':
				end, err := scanString(b, j)
				if err != nil {
					return 0, err
				}
				j = end - 1
			case '{':
				stack = append(stack, '}')
			case '[':
				stack = append(stack, ']')
			case '}', ']':
				if len(stack) == 0 || stack[len(stack)-1] != b[j] {
					return 0, errMalformed
				}
				stack = stack[:len(stack)-1]
				if len(stack) == 0 {
					return j + 1, nil
				}
			}
		}
		return 0, errMalformed
	default:
		j := i
		for j < len(b) {
			switch b[j] {
			case ',', '}', ']', ' ', '\t', '\r', '\n':
				if j == i {
					return 0, errMalformed
				}
				return j, nil
			}
			j++
		}
		return j, nil
	}
}

func scanString(b []byte, i int) (int, error) {
	for j := i + 1; j < len(b); j++ {
		switch b[j] {
		case '\\':
			j++
		case '"':
			return j + 1, nil
		}
	}
	return 0, errMalformed
}

// eachField calls fn with the raw key (between its quotes) and value span of
// every field of the object at sp.
func eachField(b []byte, sp Span, fn func(key []byte, val Span) error) error {
	i := skipWS(b, sp.S+1)
	for i < sp.E-1 {
		if b[i] == ',' {
			i = skipWS(b, i+1)
			continue
		}
		if b[i] != '"' {
			return errMalformed
		}
		kend, err := scanString(b, i)
		if err != nil {
			return err
		}
		key := b[i+1 : kend-1]
		i = skipWS(b, kend)
		if i >= len(b) || b[i] != ':' {
			return errMalformed
		}
		i = skipWS(b, i+1)
		vend, err := scanValue(b, i)
		if err != nil {
			return err
		}
		if err := fn(key, Span{i, vend}); err != nil {
			return err
		}
		i = skipWS(b, vend)
	}
	return nil
}

// eachElem calls fn with the span of every element of the array at sp.
func eachElem(b []byte, sp Span, fn func(Span) error) error {
	i := skipWS(b, sp.S+1)
	for i < sp.E-1 {
		if b[i] == ',' {
			i = skipWS(b, i+1)
			continue
		}
		vend, err := scanValue(b, i)
		if err != nil {
			return err
		}
		if err := fn(Span{i, vend}); err != nil {
			return err
		}
		i = skipWS(b, vend)
	}
	return nil
}
