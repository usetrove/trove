package stress

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/optimize"
)

// The cache-breakpoint optimizer declares 0 tokens of overhead. Its output adds cache_control markers, which
// are protocol markup and not text the model reads. The provider docs say breakpoints add no cost (checked
// 2026-11-08 at platform.claude.com, prompt-caching page); whether the markup bytes are billed is not stated
// there. So the cost rule counts visible text, and this test pins both halves of that: the markers change the
// bytes, and they add no visible tokens.
func TestCacheBreakpointsOverheadIsZeroVisibleTokens(t *testing.T) {
	if got := (optimize.CacheBreakpoints{}).OverheadTokens(); got != 0 {
		t.Fatalf("cache_breakpoints declares %d overhead tokens; the test below assumes 0", got)
	}
	spec := SessionSpec{Name: "breakpoint", PrefixTokens: 8000, Turns: 4, Model: "claude-sonnet-5-5",
		Mix: "read-heavy", OutMedianBytes: 300}
	bodies := buildBodies(spec, genTurns(spec, 99))
	orig := bodies[len(bodies)-1]

	eng := newCustomEngine([]string{"cache_breakpoints"}, nil, PricingDate)
	res := eng.TransformRequest(context.Background(), optimize.RequestIn{
		Provider: "anthropic", Path: "/v1/messages", Header: http.Header{}, Body: orig,
	}, optimize.Session{Key: "breakpoint-test", Agent: customAgent, TroveRun: true})
	if !res.Changed {
		t.Fatal("cache_breakpoints did not apply to this request, so the test measured nothing; raise PrefixTokens")
	}
	out := res.Body

	if len(out) <= len(orig) {
		t.Fatalf("expected the markers to add bytes; got %d -> %d", len(orig), len(out))
	}
	// The system prompt moves from a string into a one-block list, which a marker needs. The text in that block
	// is the same, so the visible text, in order, must be identical.
	wantText, ok1 := visibleText(orig)
	gotText, ok2 := visibleText(out)
	if !ok1 || !ok2 {
		t.Fatal("a request did not decode")
	}
	if !sameStrings(wantText, gotText) {
		t.Fatal("cache_breakpoints changed the visible text, not just its markup")
	}
	if got, want := visibleTokens(out), visibleTokens(orig); got != want {
		t.Fatalf("markers changed visible tokens: %d -> %d", want, got)
	}
	if !bytes.Contains(out, []byte(`"cache_control"`)) {
		t.Fatal("no marker was written, so the test measured nothing")
	}
	if vs := CheckRequest(orig, out, declaredOverheadTokens(res)); hasInvariant(vs, InvCostBound) {
		t.Fatalf("the cost rule flagged a breakpoint-only change: %+v", vs)
	}
}

// Visible tokens count text and skip markup, so a marker's bytes never count as tokens.
func TestVisibleTokensSkipsMarkup(t *testing.T) {
	plain := []byte(`{"messages":[{"role":"user","content":"hello there"}]}`)
	marked := []byte(`{"messages":[{"role":"user","content":"hello there","cache_control":{"type":"ephemeral"}}]}`)
	if visibleTokens(plain) != visibleTokens(marked) {
		t.Fatalf("a cache_control marker changed the visible-token count: %d vs %d", visibleTokens(plain), visibleTokens(marked))
	}
	text := strings.Repeat("a", 350) // 350 bytes = 100 tokens at 3.5 bytes per token
	withText := []byte(`{"messages":[{"role":"user","content":"` + text + `"}]}`)
	if got := visibleTokens(withText); got != 100 {
		t.Fatalf("350 bytes of text gave %d tokens, want 100", got)
	}
}

// A body that cannot be decoded is counted at full size, never as zero.
func TestVisibleTokensFailsConservatively(t *testing.T) {
	junk := []byte(`{"messages":[` + strings.Repeat("x", 700))
	if got := visibleTokens(junk); got != tokensOf(len(junk)) {
		t.Fatalf("undecodable body counted as %d tokens, want the full %d", got, tokensOf(len(junk)))
	}
}
