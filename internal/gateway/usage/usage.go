// Package usage extracts token usage from provider responses. It reads a copy
// of the bytes the client already received and never modifies anything. Only
// numbers, model names and error classes are kept, never response text.
package usage

import (
	"encoding/json"
	"strings"
)

// Provider names, matching internal/pricing.
const (
	Anthropic = "anthropic"
	OpenAI    = "openai"
)

// maxBody caps how much of a non-streamed response is kept for parsing.
const maxBody = 32 << 20

// Result is what one response reported. Zero values mean "not reported"
// unless Reported is true.
type Result struct {
	Model       string
	ServiceTier string

	// Reported is true when the provider sent a usage object.
	Reported bool

	// Disjoint buckets, ready for internal/pricing.
	UncachedInput int64
	CacheRead     int64
	Output        int64
	Reasoning     int64 // informational for Anthropic and OpenAI (already inside Output)

	// CacheWriteTotal is all tokens written to cache. CacheWriteByTTL splits
	// them by TTL ("5m", "1h"); it is nil when the provider gave no split,
	// and then the request cannot be priced exactly if CacheWriteTotal > 0.
	CacheWriteTotal int64
	CacheWriteByTTL map[string]int64

	// ToolCalls counts provider-side tool calls by name (Anthropic
	// server_tool_use.<name>_requests), for pricing.
	ToolCalls map[string]int64

	// Raw is the usage object exactly as reported (numbers only).
	Raw json.RawMessage

	// ErrorClass is a short label such as "stream_error:overloaded_error",
	// "stream_incomplete" or "usage_unavailable"; "" when all is well.
	ErrorClass string
}

// Extractor consumes response bytes incrementally.
type Extractor interface {
	Write(p []byte)
	// Finish is called once when the response body ends (cleanly or not).
	// cleanEnd is false if the connection broke before the body completed.
	Finish(cleanEnd bool) Result
}

// New picks an extractor from the provider, the request path and whether the
// response is an event stream. It returns nil if the endpoint carries no usage
// we know how to read; the request is then recorded without token counts.
func New(provider, path string, streamed bool) Extractor {
	p := strings.TrimSuffix(path, "/")
	switch provider {
	case Anthropic:
		if strings.HasSuffix(p, "/v1/messages") {
			if streamed {
				return newAnthropicStream()
			}
			return newBodyExtractor(anthropicBody)
		}
	case OpenAI:
		switch {
		case strings.HasSuffix(p, "/chat/completions"):
			if streamed {
				return newChatStream()
			}
			return newBodyExtractor(chatBody)
		case strings.HasSuffix(p, "/responses"):
			// GATED: Responses usage field names are not yet verified against
			// official docs (see plan). Record the request without tokens.
			return nil
		}
	}
	return nil
}

// bodyExtractor buffers a non-streamed JSON body (bounded) and parses it once.
type bodyExtractor struct {
	buf   []byte
	over  bool
	parse func([]byte) Result
}

func newBodyExtractor(parse func([]byte) Result) *bodyExtractor { return &bodyExtractor{parse: parse} }

func (b *bodyExtractor) Write(p []byte) {
	if b.over {
		return
	}
	if len(b.buf)+len(p) > maxBody {
		b.over, b.buf = true, nil
		return
	}
	b.buf = append(b.buf, p...)
}

func (b *bodyExtractor) Finish(cleanEnd bool) Result {
	if b.over || !cleanEnd {
		return Result{ErrorClass: "usage_unavailable"}
	}
	return b.parse(b.buf)
}

// intField reads an integer from a decoded JSON object; absent or non-numeric is 0.
func intField(m map[string]json.RawMessage, key string) int64 {
	raw, ok := m[key]
	if !ok {
		return 0
	}
	var n json.Number
	if json.Unmarshal(raw, &n) != nil {
		return 0
	}
	v, err := n.Int64()
	if err != nil {
		return 0
	}
	return v
}

func strField(m map[string]json.RawMessage, key string) string {
	var s string
	if raw, ok := m[key]; ok {
		_ = json.Unmarshal(raw, &s)
	}
	return s
}

func objField(m map[string]json.RawMessage, key string) map[string]json.RawMessage {
	var o map[string]json.RawMessage
	if raw, ok := m[key]; ok {
		_ = json.Unmarshal(raw, &o)
	}
	return o
}
