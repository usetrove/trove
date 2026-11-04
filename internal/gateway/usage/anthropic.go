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
// Verified against the prompt-caching docs on 2026-11-03
// (platform.claude.com/docs/en/build-with-claude/prompt-caching): usage carries
// cache_creation_input_tokens and a cache_creation object with
// ephemeral_5m_input_tokens and ephemeral_1h_input_tokens, and the docs state
// that cache_creation_input_tokens equals the sum of the object's values.
// CacheWriteByTTL is filled only when the split adds up exactly to that total;
// otherwise it stays nil and the request is left unpriced rather than guessed.

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
	r.CacheWriteByTTL = cacheWriteByTTL(objField(u, "cache_creation"), r.CacheWriteTotal)
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

// cacheWriteByTTL turns the cache_creation object into tokens per TTL. It returns
// nil (no split) when nothing was written, the object is absent, or the parts do
// not add up to total, for example because a TTL this code does not know about
// appeared. Callers then treat the request as unpriceable.
func cacheWriteByTTL(split map[string]json.RawMessage, total int64) map[string]int64 {
	if total <= 0 || len(split) == 0 {
		return nil
	}
	by := map[string]int64{}
	var sum int64
	for ttl, key := range map[string]string{"5m": "ephemeral_5m_input_tokens", "1h": "ephemeral_1h_input_tokens"} {
		if n := intField(split, key); n > 0 {
			by[ttl] = n
			sum += n
		}
	}
	if sum != total {
		return nil
	}
	return by
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
