package optimize

import (
	"context"
	"regexp"
	"strings"
)

// minCacheTokens is the shortest prompt the Anthropic API will cache, by model.
// Source: https://platform.claude.com/docs/en/build-with-claude/prompt-caching
// ("Minimum cacheable prompt length"), verified 2026-10-29. Shorter prompts are
// processed without caching and without an error. A model that is not listed is
// skipped: guessing a minimum could add markers that do nothing.
var minCacheTokens = map[string]int{
	// 512 tokens
	"claude-fable-5-1": 512, "claude-mythos-5-1": 512, "claude-opus-5-5": 512, "claude-opus-5": 512,
	"claude-sonnet-5-5": 512, "claude-fable-5": 512, "claude-mythos-5": 512, "claude-haiku-5-5": 512,
	// 1,024 tokens
	"claude-opus-4-8": 1024, "claude-sonnet-5": 1024, "claude-sonnet-4-6": 1024, "claude-sonnet-4-5": 1024,
	"claude-opus-4-1": 1024, "claude-opus-4": 1024, "claude-sonnet-4": 1024,
	// 2,048 tokens
	"claude-mythos-preview": 2048, "claude-opus-4-7": 2048, "claude-3-5-haiku": 2048,
	// 4,096 tokens
	"claude-opus-4-6": 4096, "claude-opus-4-5": 4096, "claude-haiku-4-5": 4096,
}

// dateSuffix matches the optional snapshot suffix of a model id.
var dateSuffix = regexp.MustCompile(`^[-@](\d{8}|latest)$`)

// cacheMinimum returns the minimum cacheable tokens for model. A model id must
// equal a known id, optionally followed by a date or "latest", so a future
// "claude-opus-4-9" is not mistaken for "claude-opus-4".
func cacheMinimum(model string) (int, bool) {
	best, n := "", 0
	for id, floor := range minCacheTokens {
		if !strings.HasPrefix(model, id) || len(id) <= len(best) {
			continue
		}
		if rest := model[len(id):]; rest == "" || dateSuffix.MatchString(rest) {
			best, n = id, floor
		}
	}
	return n, best != ""
}

// approxBytesPerToken is deliberately generous (fewer bytes per token than
// real text) so the size check errs toward not adding markers.
const approxBytesPerToken = 4

const ephemeral = `{"type":"ephemeral"}`

// CacheBreakpoints is the cache_breakpoints optimizer. For custom agents whose
// requests carry no caching markers at all, on a model that caches only when
// asked, it adds:
//
//   - a top-level cache_control (automatic caching), which the provider moves to
//     the last cacheable block as the conversation grows, so no message block is
//     ever edited and the history prefix stays byte-identical turn to turn;
//   - a stable marker on the last tool definition;
//   - a stable marker on the last system block that holds no volatile line.
//
// That is at most 3 of the provider's 4 allowed breakpoints. Markers use the
// default 5-minute lifetime. A request that already has any cache_control is
// never touched: the agent manages its own caching, and the mix of automatic
// and explicit markers has provider rules (such as a limit of 4 and TTL
// ordering) that Trove must not disturb.
//
// The decision to add markers depends on prompt size, which only grows within a
// session, so once markers appear they stay.
type CacheBreakpoints struct{}

// Name implements Optimizer.
func (CacheBreakpoints) Name() string { return "cache_breakpoints" }

// Tier implements Optimizer.
func (CacheBreakpoints) Tier() Tier { return TierLossless }

// Applies implements Optimizer.
func (CacheBreakpoints) Applies(r *Request, s Session) bool {
	if !s.Custom || r.RootEmpty || len(r.Messages) == 0 {
		return false
	}
	floor, ok := cacheMinimum(r.Model)
	return ok && len(r.Raw)/approxBytesPerToken >= floor && !r.HasCacheControl()
}

// Transform implements Optimizer.
func (CacheBreakpoints) Transform(_ context.Context, r *Request) (*Request, Report) {
	if err := r.Ensure(); err != nil {
		return r, Report{Err: err.Error()}
	}
	// Top-level automatic caching goes first in the object so "messages" stays
	// last and a growing history only ever appends to the end of the body.
	edits := []Edit{{Span: Span{r.Root.S + 1, r.Root.S + 1}, New: []byte(`"cache_control":` + ephemeral + `,`)}}

	if n := len(r.ToolSpans); n > 0 {
		if e, ok := markObject(r, r.ToolSpans[n-1]); ok {
			edits = append(edits, e)
		}
	}
	if e, ok := markSystem(r); ok {
		edits = append(edits, e)
	}
	out, err := r.Apply(edits)
	if err != nil {
		return r, Report{Err: err.Error()}
	}
	return out, Report{}
}

// markObject inserts a cache_control field before the closing brace of the
// object at sp.
func markObject(r *Request, sp Span) (Edit, bool) {
	if r.Raw[sp.S] != '{' {
		return Edit{}, false
	}
	end := sp.E - 1
	i := end - 1
	for i > sp.S && (r.Raw[i] == ' ' || r.Raw[i] == '\n' || r.Raw[i] == '\t' || r.Raw[i] == '\r') {
		i--
	}
	comma := ","
	if i == sp.S { // "{}"
		comma = ""
	}
	return Edit{Span: Span{end, end}, New: []byte(comma + `"cache_control":` + ephemeral)}, true
}

// markSystem marks the last stable system text. A string system prompt is
// converted to a one-block array so the block can carry the marker.
func markSystem(r *Request) (Edit, bool) {
	if r.System.E == 0 {
		return Edit{}, false
	}
	if r.Raw[r.System.S] == '"' {
		text, ok := r.DecodeString(r.System)
		if !ok || strings.TrimSpace(text) == "" || hasVolatileLine(text) {
			return Edit{}, false
		}
		blk := `[{"type":"text","text":` + string(EncodeString(text)) + `,"cache_control":` + ephemeral + `}]`
		return Edit{Span: r.System, New: []byte(blk)}, true
	}
	for i := len(r.SystemBlocks) - 1; i >= 0; i-- {
		b := r.SystemBlocks[i]
		if b.Type != "text" || b.Text.E == 0 {
			continue
		}
		text, ok := r.DecodeString(b.Text)
		if !ok || strings.TrimSpace(text) == "" {
			continue
		}
		if hasVolatileLine(text) {
			continue // the stable prefix ends before this block
		}
		return markObject(r, b.Span)
	}
	return Edit{}, false
}
