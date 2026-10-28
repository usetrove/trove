package usage

import (
	"bytes"
	"encoding/json"
)

// Verified against the OpenAI Chat Completions streaming reference on
// 2026-10-28 (developers.openai.com/api/reference/resources/chat/...): usage
// arrives only when the request set stream_options.include_usage; it is the
// final chunk (choices may be empty), `usage` is null on all other chunks, and
// it may never arrive if the stream is cut. prompt_tokens INCLUDES cached
// tokens (prompt_tokens_details.cached_tokens, cache_write_tokens), and
// completion_tokens INCLUDES reasoning tokens.

type chatStream struct {
	sp     splitter
	model  string
	tier   string
	usage  map[string]json.RawMessage
	done   bool
	sawErr string
}

func newChatStream() *chatStream {
	c := &chatStream{}
	c.sp.emit = c.event
	return c
}

func (c *chatStream) Write(p []byte) { c.sp.Write(p) }

func (c *chatStream) event(e Event) {
	if bytes.Equal(bytes.TrimSpace(e.Data), []byte("[DONE]")) {
		c.done = true
		return
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(e.Data, &top) != nil {
		return
	}
	if er := objField(top, "error"); er != nil && c.sawErr == "" {
		c.sawErr = "stream_error:" + strField(er, "type")
	}
	if m := strField(top, "model"); m != "" {
		c.model = m
	}
	if t := strField(top, "service_tier"); t != "" {
		c.tier = t
	}
	if u := objField(top, "usage"); len(u) > 0 { // null decodes to nil
		c.usage = u
	}
}

func (c *chatStream) Finish(cleanEnd bool) Result {
	c.sp.Flush()
	r := chatResult(c.model, c.tier, c.usage)
	switch {
	case c.sawErr != "":
		r.ErrorClass = c.sawErr
	case !cleanEnd || !c.done:
		r.ErrorClass = "stream_incomplete"
	case !r.Reported:
		r.ErrorClass = "usage_unavailable" // e.g. include_usage was not requested
	}
	return r
}

func chatResult(model, tier string, u map[string]json.RawMessage) Result {
	r := Result{Model: model, ServiceTier: tier}
	if len(u) == 0 {
		return r
	}
	r.Reported = true
	prompt := intField(u, "prompt_tokens")
	pd := objField(u, "prompt_tokens_details")
	cached := intField(pd, "cached_tokens")
	written := intField(pd, "cache_write_tokens")
	r.UncachedInput = max(prompt-cached-written, 0)
	r.CacheRead = cached
	r.CacheWriteTotal = written
	r.Output = intField(u, "completion_tokens")
	r.Reasoning = intField(objField(u, "completion_tokens_details"), "reasoning_tokens")
	r.Raw, _ = json.Marshal(u)
	return r
}

func chatBody(b []byte) Result {
	var top map[string]json.RawMessage
	if json.Unmarshal(b, &top) != nil {
		return Result{ErrorClass: "usage_unavailable"}
	}
	if er := objField(top, "error"); er != nil {
		return Result{ErrorClass: "api_error:" + strField(er, "type")}
	}
	r := chatResult(strField(top, "model"), strField(top, "service_tier"), objField(top, "usage"))
	if !r.Reported {
		r.ErrorClass = "usage_unavailable"
	}
	return r
}
