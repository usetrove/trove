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
//
// The scan is a single descent: each byte of the body is visited about once, and
// strings are skipped with a vectorized search, because this runs on every
// request and again after every optimizer that changes something.

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
	// parsed is false for a Request made by Apply, which is parsed on demand.
	parsed bool
	// cacheControl caches HasCacheControl: 0 unknown, 1 no, 2 yes.
	cacheControl uint8
}

// HasCacheControl reports whether the body mentions cache_control anywhere. It is
// a byte search on purpose: it can only err toward "markers present". It is a
// method, not a field, because the search reads the whole body and only
// cache_breakpoints needs it.
func (r *Request) HasCacheControl() bool {
	if r.cacheControl == 0 {
		r.cacheControl = 1
		if bytes.Contains(r.Raw, []byte(`"cache_control"`)) {
			r.cacheControl = 2
		}
	}
	return r.cacheControl == 2
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
	fields := 0
	end, err := walkObject(body, i, func(key []byte, vs int) (int, error) {
		fields++
		switch string(key) {
		case "model":
			ve, err := scanValue(body, vs)
			if err == nil && body[vs] == '"' {
				r.Model, _ = decodeString(body[vs:ve])
			}
			return ve, err
		case "system":
			ve, err := r.parseSystem(vs)
			r.System = Span{vs, ve}
			return ve, err
		case "tools":
			ve, err := r.parseTools(vs)
			r.Tools = Span{vs, ve}
			return ve, err
		case "messages":
			return r.parseMessages(vs)
		}
		return scanValue(body, vs)
	})
	if err != nil {
		return nil, err
	}
	r.Root = Span{i, end}
	r.RootClose = end - 1
	r.RootEmpty = fields == 0
	r.parsed = true
	return r, nil
}

// Ensure parses a Request returned by Apply, which is left unparsed because the
// next optimizer may not need it, or there may be no next optimizer. It is a
// no-op on a parsed Request. Optimizers call it first; it fails only if an
// earlier edit produced a malformed body.
func (r *Request) Ensure() error {
	if r.parsed {
		return nil
	}
	p, err := ParseRequest(r.Raw)
	if err != nil {
		return err
	}
	*r = *p
	return nil
}

func (r *Request) parseSystem(vs int) (int, error) {
	b := r.Raw
	if b[vs] != '[' {
		return scanValue(b, vs)
	}
	return walkArray(b, vs, func(es int) (int, error) {
		blk := SystemBlock{}
		if b[es] != '{' {
			ee, err := scanValue(b, es)
			blk.Span = Span{es, ee}
			r.SystemBlocks = append(r.SystemBlocks, blk)
			return ee, err
		}
		ee, err := walkObject(b, es, func(key []byte, vs int) (int, error) {
			ve, err := scanValue(b, vs)
			switch string(key) {
			case "type":
				if err == nil && b[vs] == '"' {
					blk.Type = internStr(b[vs:ve])
				}
			case "text":
				if b[vs] == '"' {
					blk.Text = Span{vs, ve}
				}
			}
			return ve, err
		})
		blk.Span = Span{es, ee}
		r.SystemBlocks = append(r.SystemBlocks, blk)
		return ee, err
	})
}

func (r *Request) parseTools(vs int) (int, error) {
	b := r.Raw
	if b[vs] != '[' {
		return scanValue(b, vs)
	}
	return walkArray(b, vs, func(es int) (int, error) {
		ee, err := scanValue(b, es)
		r.ToolSpans = append(r.ToolSpans, Span{es, ee})
		return ee, err
	})
}

func (r *Request) parseMessages(vs int) (int, error) {
	b := r.Raw
	if b[vs] != '[' {
		return scanValue(b, vs)
	}
	r.Messages = make([]Message, 0, 64)
	return walkArray(b, vs, func(es int) (int, error) {
		msg := Message{}
		if b[es] != '{' {
			ee, err := scanValue(b, es)
			msg.Span = Span{es, ee}
			r.Messages = append(r.Messages, msg)
			return ee, err
		}
		ee, err := walkObject(b, es, func(key []byte, vs int) (int, error) {
			switch string(key) {
			case "role":
				ve, err := scanValue(b, vs)
				if err == nil && b[vs] == '"' {
					msg.Role = internStr(b[vs:ve])
				}
				return ve, err
			case "content":
				if b[vs] == '[' {
					return walkArray(b, vs, func(bs int) (int, error) {
						blk, be, err := parseBlock(b, bs)
						msg.Blocks = append(msg.Blocks, blk)
						return be, err
					})
				}
			}
			return scanValue(b, vs)
		})
		msg.Span = Span{es, ee}
		r.Messages = append(r.Messages, msg)
		return ee, err
	})
}

// parseBlock reads one content block starting at b[bs] and returns it with the
// offset one past its end.
func parseBlock(b []byte, bs int) (Block, int, error) {
	blk := Block{}
	if b[bs] != '{' {
		be, err := scanValue(b, bs)
		blk.Span = Span{bs, be}
		return blk, be, err
	}
	be, err := walkObject(b, bs, func(key []byte, vs int) (int, error) {
		ve, err := scanValue(b, vs)
		if err != nil {
			return 0, err
		}
		switch string(key) {
		case "type":
			if b[vs] == '"' {
				blk.Type = internStr(b[vs:ve])
			}
		case "tool_use_id":
			if b[vs] == '"' {
				blk.ToolUseID, _ = decodeString(b[vs:ve])
			}
		case "is_error":
			blk.IsError = bytes.Equal(b[vs:ve], []byte("true"))
		case "content":
			blk.Content = Span{vs, ve}
		}
		return ve, nil
	})
	blk.Span = Span{bs, be}
	if err != nil || blk.Type != "tool_result" || blk.Content.E == 0 {
		return blk, be, err
	}
	switch b[blk.Content.S] {
	case '"':
		blk.Texts = []Span{blk.Content}
		blk.TextOnly = true
	case '[':
		blk.TextOnly = true
		_, err = walkArray(b, blk.Content.S, func(cs int) (int, error) {
			if b[cs] != '{' {
				blk.TextOnly = false
				return scanValue(b, cs)
			}
			var typ string
			var text Span
			ce, err := walkObject(b, cs, func(key []byte, vs int) (int, error) {
				ve, err := scanValue(b, vs)
				if err != nil {
					return 0, err
				}
				switch string(key) {
				case "type":
					if b[vs] == '"' {
						typ = internStr(b[vs:ve])
					}
				case "text":
					text = Span{vs, ve}
				}
				return ve, nil
			})
			if err != nil {
				return 0, err
			}
			if typ == "text" && text.E != 0 && b[text.S] == '"' {
				blk.Texts = append(blk.Texts, text)
			} else {
				blk.TextOnly = false
			}
			return ce, nil
		})
	default:
		// null or an object: not something we rewrite.
	}
	return blk, be, err
}

// Apply returns a new, unparsed Request with edits applied. Edits must not
// overlap. Call Ensure on the result before reading its structure.
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
	return &Request{Raw: out}, nil
}

// DecodeString decodes the JSON string at sp.
func (r *Request) DecodeString(sp Span) (string, bool) { return decodeString(r.Raw[sp.S:sp.E]) }

// textSpans lists every text span in request order. Index i names the same span
// in any version of the request that differs only inside its spans, so the
// engine can find the original text of span i after another optimizer has
// rewritten the request. Listing spans does not decode them.
func (r *Request) textSpans() []Span {
	var out []Span
	for _, m := range r.Messages {
		for _, b := range m.Blocks {
			out = append(out, b.Texts...)
		}
	}
	return out
}

// internStr decodes a short JSON string that is almost always one of a few known
// words (block types, roles), without allocating for those.
func internStr(b []byte) string {
	switch string(b) {
	case `"text"`:
		return "text"
	case `"tool_use"`:
		return "tool_use"
	case `"tool_result"`:
		return "tool_result"
	case `"user"`:
		return "user"
	case `"assistant"`:
		return "assistant"
	case `"image"`:
		return "image"
	case `"thinking"`:
		return "thinking"
	}
	s, _ := decodeString(b)
	return s
}

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

// scanString returns the offset one past the string that opens at b[i]. It jumps
// from quote to quote with a vectorized search and counts the backslashes in
// front of each to tell an escaped quote from the closing one.
func scanString(b []byte, i int) (int, error) {
	j := i + 1
	for {
		k := bytes.IndexByte(b[j:], '"')
		if k < 0 {
			return 0, errMalformed
		}
		q := j + k
		n := 0
		for p := q - 1; p > i && b[p] == '\\'; p-- {
			n++
		}
		if n%2 == 0 {
			return q + 1, nil
		}
		j = q + 1
	}
}

// walkObject walks the object that opens at b[i], calling fn for each field with
// the raw key (between its quotes) and the offset where its value starts. fn
// returns the offset one past the value, by descending into it or skipping it
// with scanValue, so the value is scanned once. walkObject returns the offset one
// past the closing brace.
func walkObject(b []byte, i int, fn func(key []byte, valStart int) (int, error)) (int, error) {
	i = skipWS(b, i+1)
	if i < len(b) && b[i] == '}' {
		return i + 1, nil
	}
	for {
		if i >= len(b) || b[i] != '"' {
			return 0, errMalformed
		}
		kend, err := scanString(b, i)
		if err != nil {
			return 0, err
		}
		key := b[i+1 : kend-1]
		i = skipWS(b, kend)
		if i >= len(b) || b[i] != ':' {
			return 0, errMalformed
		}
		i = skipWS(b, i+1)
		if i >= len(b) {
			return 0, errMalformed
		}
		vend, err := fn(key, i)
		if err != nil {
			return 0, err
		}
		i = skipWS(b, vend)
		if i >= len(b) {
			return 0, errMalformed
		}
		switch b[i] {
		case ',':
			i = skipWS(b, i+1)
		case '}':
			return i + 1, nil
		default:
			return 0, errMalformed
		}
	}
}

// walkArray is walkObject for arrays: fn gets the offset where each element
// starts and returns the offset one past it.
func walkArray(b []byte, i int, fn func(elemStart int) (int, error)) (int, error) {
	i = skipWS(b, i+1)
	if i < len(b) && b[i] == ']' {
		return i + 1, nil
	}
	for {
		if i >= len(b) {
			return 0, errMalformed
		}
		eend, err := fn(i)
		if err != nil {
			return 0, err
		}
		i = skipWS(b, eend)
		if i >= len(b) {
			return 0, errMalformed
		}
		switch b[i] {
		case ',':
			i = skipWS(b, i+1)
		case ']':
			return i + 1, nil
		default:
			return 0, errMalformed
		}
	}
}

// originalSpanText returns Session.Raw for a request: span i decoded from the
// request as it arrived, before any optimizer changed it. Spans are listed once;
// a span is decoded only when an optimizer asks for it.
func originalSpanText(orig *Request) func(int) string {
	spans := orig.textSpans()
	raw := orig.Raw
	return func(i int) string {
		if i < 0 || i >= len(spans) {
			return ""
		}
		s, _ := decodeString(raw[spans[i].S:spans[i].E])
		return s
	}
}
