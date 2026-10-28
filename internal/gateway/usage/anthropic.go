package usage

import (
	"encoding/json"
	"strings"
)

// Verified against the Anthropic streaming docs on 2026-10-28
// (platform.claude.com/docs/en/build-with-claude/streaming): usage appears in
// message_start (message.usage) and message_delta (usage, CUMULATIVE); `ping`
// and unknown event types may appear; `error` events can follow a 200.
//
// GATED (waiting on official schema text): the cache_creation per-TTL
// breakdown and the exact non-streamed usage field list. Until then
// CacheWriteByTTL stays nil and writes cannot be priced exactly.

type anthropicStream struct {
	sp       splitter
	model    string
	tier     string
	usage    map[string]json.RawMessage // merged: message_start overlaid by each message_delta
	stopped  bool
	errClass string
}

func newAnthropicStream() *anthropicStream {
	a := &anthropicStream{usage: map[string]json.RawMessage{}}
	a.sp.emit = a.event
	return a
}

func (a *anthropicStream) Write(p []byte) { a.sp.Write(p) }

func (a *anthropicStream) event(e Event) {
	var top map[string]json.RawMessage
	if json.Unmarshal(e.Data, &top) != nil {
		return // malformed event: ignore, keep going
	}
	switch strField(top, "type") {
	case "message_start":
		msg := objField(top, "message")
		a.model = strField(msg, "model")
		a.merge(objField(msg, "usage"))
	case "message_delta":
		a.merge(objField(top, "usage"))
	case "message_stop":
		a.stopped = true
	case "error":
		if a.errClass == "" {
			a.errClass = "stream_error:" + strField(objField(top, "error"), "type")
		}
	}
}

func (a *anthropicStream) merge(u map[string]json.RawMessage) {
	for k, v := range u {
		a.usage[k] = v
	}
	if t := strField(u, "service_tier"); t != "" {
		a.tier = t
	}
}

func (a *anthropicStream) Finish(cleanEnd bool) Result {
	a.sp.Flush()
	r := anthropicResult(a.model, a.usage)
	switch {
	case a.errClass != "":
		r.ErrorClass = a.errClass
	case !cleanEnd || !a.stopped:
		r.ErrorClass = "stream_incomplete"
	}
	return r
}

func anthropicResult(model string, u map[string]json.RawMessage) Result {
	r := Result{Model: model}
	if len(u) == 0 {
		return r
	}
	r.Reported = true
	r.ServiceTier = strField(u, "service_tier")
	r.UncachedInput = intField(u, "input_tokens")
	r.Output = intField(u, "output_tokens")
	r.CacheRead = intField(u, "cache_read_input_tokens")
	r.CacheWriteTotal = intField(u, "cache_creation_input_tokens")
	for k := range objField(u, "server_tool_use") {
		if name, ok := strings.CutSuffix(k, "_requests"); ok {
			if r.ToolCalls == nil {
				r.ToolCalls = map[string]int64{}
			}
			r.ToolCalls[name] = intField(objField(u, "server_tool_use"), k)
		}
	}
	r.Raw, _ = json.Marshal(u) // map keys marshal sorted: stable output
	return r
}

// anthropicBody parses a non-streamed /v1/messages response. An error body
// ({"type":"error",...}) has no usage and yields an error class.
func anthropicBody(b []byte) Result {
	var top map[string]json.RawMessage
	if json.Unmarshal(b, &top) != nil {
		return Result{ErrorClass: "usage_unavailable"}
	}
	if strField(top, "type") == "error" {
		return Result{ErrorClass: "api_error:" + strField(objField(top, "error"), "type")}
	}
	r := anthropicResult(strField(top, "model"), objField(top, "usage"))
	if !r.Reported {
		r.ErrorClass = "usage_unavailable"
	}
	return r
}
