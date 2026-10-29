package optimize

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/config"
)

var custom = Session{Key: "s", Agent: "sdk", Custom: true}

// pad returns n bytes of stable, non-volatile instruction text.
func pad(n int) string {
	var sb strings.Builder
	for i := 0; sb.Len() < n; i++ {
		fmt.Fprintf(&sb, "Rule %d: always explain the reason for a change before making it.\n", i)
	}
	return sb.String()
}

func sortedLines(s string) []string {
	l := strings.Split(s, "\n")
	sort.Strings(l)
	return l
}

// --- volatile_prefix_mover ---------------------------------------------------

func TestVolatilePrefixMoverProperties(t *testing.T) {
	stringSys := "You are a coding agent.\nToday's date: 2026-10-29\nBe brief.\nSession 3f2504e0-4f89-41d3-9a0c-0305e82c3301\n"
	mixed := []blk{{"type": "text", "text": "Stable intro\nCurrent time 2026-10-29T10:00:00Z\nMore stable"}, {"type": "text", "text": "Tail rules"}}
	msgs := []msg{user(text("hi"))}
	applies := [][]byte{
		req{system: stringSys, msgs: msgs}.body(),
		req{system: mixed, tools: []string{"bash"}, msgs: msgs}.body(),
	}
	noops := [][]byte{
		req{system: "no volatile text here at all", msgs: msgs}.body(),
		req{msgs: msgs}.body(), // no system prompt
		req{system: []blk{{"type": "text", "text": "Today's date: 2026-10-29"}}, msgs: msgs}.body(), // volatile-only block stays put
		req{system: "Today's date: 2026-10-29", msgs: msgs}.body(),                                  // nothing stable to keep in front
	}
	checkProperties(t, VolatilePrefixMover{}, custom, applies, noops)
}

func TestVolatilePrefixMoverPreservesEveryLine(t *testing.T) {
	sys := "Intro line\nToday's date: 2026-10-29\nSecond rule\nrequest 3f2504e0-4f89-41d3-9a0c-0305e82c3301\n\nLast rule"
	r, _ := ParseRequest(req{system: sys, msgs: []msg{user(text("hi"))}}.body())
	out, rep := VolatilePrefixMover{}.Transform(context.Background(), r)
	if rep.Err != "" || len(out.SystemBlocks) != 2 {
		t.Fatalf("expected two system blocks, got %d (%s)", len(out.SystemBlocks), rep.Err)
	}
	var all []string
	for _, b := range out.SystemBlocks {
		s, _ := out.DecodeString(b.Text)
		all = append(all, s)
	}
	stable, volatile := all[0], all[1]
	if strings.Contains(stable, "2026-10-29") || strings.Contains(stable, "3f2504e0") {
		t.Errorf("volatile text left in the stable block: %q", stable)
	}
	if got, want := sortedLines(stable+"\n"+volatile), sortedLines(sys); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("lines were lost or added\n got %q\nwant %q", got, want)
	}
}

func TestVolatilePrefixMoverIsGatedOnCustomAgents(t *testing.T) {
	body := req{system: "Intro\nToday's date: 2026-10-29\nMore", msgs: []msg{user(text("hi"))}}.body()
	for agent, wantChange := range map[string]bool{"sdk": true, "other": false, "claude_code": false, "codex": false} {
		cfg := config.Default()
		cfg.Optimizers["volatile_prefix_mover"] = "on"
		cfg.Optimize.CustomAgents = []string{"sdk"} // "other" is not listed
		e := New(Options{Config: cfg, Getenv: func(string) string { return "" }})
		in := input(body)
		in.Agent = agent
		if res := e.Run(context.Background(), in); res.Changed != wantChange {
			t.Errorf("agent %s: changed=%v, want %v", agent, res.Changed, wantChange)
		}
	}
	cfg := config.Default()
	cfg.Optimize.CustomAgents = []string{"sdk"}
	cfg.Optimizers["volatile_prefix_mover"] = "on"
	in := input(body)
	in.Agent = "claude_code"
	if New(Options{Config: cfg, Getenv: func(string) string { return "" }}).Run(context.Background(), in).Changed {
		t.Error("an agent that builds its own system prompt must never be touched")
	}
}

// --- cache_breakpoints -------------------------------------------------------

func countMarkers(b []byte) int { return bytes.Count(b, []byte(`"cache_control"`)) }

func TestCacheBreakpointsProperties(t *testing.T) {
	big := pad(6000)
	msgs := []msg{user(text("hi")), assistant(text("hello")), user(text("go"))}
	applies := [][]byte{
		req{system: big, tools: []string{"bash", "read"}, msgs: msgs}.body(),
		req{system: []blk{{"type": "text", "text": big}}, tools: []string{"bash"}, msgs: msgs}.body(),
		req{system: big, msgs: msgs}.body(),                               // no tools
		req{tools: []string{"bash"}, msgs: []msg{user(text(big))}}.body(), // no system prompt
	}
	noops := [][]byte{
		req{system: "short", tools: []string{"bash"}, msgs: msgs}.body(),                                                              // below the model minimum
		req{system: big, msgs: msgs, extra: `"cache_control":{"type":"ephemeral"}`}.body(),                                            // already automatic
		req{system: []blk{{"type": "text", "text": big, "cache_control": map[string]string{"type": "ephemeral"}}}, msgs: msgs}.body(), // already explicit
		req{system: big, msgs: []msg{user(blk{"type": "text", "text": "x", "cache_control": map[string]any{"type": "ephemeral", "ttl": "1h"}})}}.body(),
		req{model: "claude-opus-4-9", system: big, msgs: msgs}.body(), // unknown model: no guessing
		req{system: big}.body(), // no messages
	}
	checkProperties(t, CacheBreakpoints{}, custom, applies, noops)
}

func TestCacheBreakpointsPlacementAndLimit(t *testing.T) {
	body := req{system: pad(6000), tools: []string{"bash", "read"}, msgs: []msg{user(text("hi"))}}.body()
	r, _ := ParseRequest(body)
	out, rep := CacheBreakpoints{}.Transform(context.Background(), r)
	if rep.Err != "" {
		t.Fatal(rep.Err)
	}
	if n := countMarkers(out.Raw); n != 3 {
		t.Errorf("want 3 markers (top level, last tool, system), got %d", n)
	}
	if !bytes.HasPrefix(out.Raw, []byte(`{"cache_control":{"type":"ephemeral"},"model"`)) {
		t.Errorf("top-level marker should lead the object: %.80s", out.Raw)
	}
	if len(out.ToolSpans) != 2 || bytes.Contains(out.Raw[out.ToolSpans[0].S:out.ToolSpans[0].E], []byte("cache_control")) ||
		!bytes.Contains(out.Raw[out.ToolSpans[1].S:out.ToolSpans[1].E], []byte("cache_control")) {
		t.Error("only the last tool definition should carry a marker")
	}
	if len(out.SystemBlocks) != 1 || !bytes.Contains(out.Raw[out.SystemBlocks[0].Span.S:out.SystemBlocks[0].Span.E], []byte("cache_control")) {
		t.Error("the system block should carry a marker")
	}
	// Messages are untouched, byte for byte.
	mi := bytes.Index(body, []byte(`"messages"`))
	if !bytes.HasSuffix(out.Raw, body[mi:]) {
		t.Error("message bytes changed")
	}
}

func TestCacheBreakpointsKeepsVolatileTextOutOfTheCachedPrefix(t *testing.T) {
	sys := pad(6000) + "Today's date: 2026-10-29\n"
	r, _ := ParseRequest(req{system: sys, tools: []string{"bash"}, msgs: []msg{user(text("hi"))}}.body())
	out, _ := CacheBreakpoints{}.Transform(context.Background(), r)
	if n := countMarkers(out.Raw); n != 2 { // top level + tool; the system block has a volatile line
		t.Errorf("want 2 markers, got %d", n)
	}
	// With the mover run first, the stable block is marked and the volatile one is not.
	moved, _ := VolatilePrefixMover{}.Transform(context.Background(), r)
	out, _ = CacheBreakpoints{}.Transform(context.Background(), moved)
	if n := countMarkers(out.Raw); n != 3 {
		t.Fatalf("want 3 markers after moving the volatile text, got %d", n)
	}
	if len(out.SystemBlocks) != 2 || !bytes.Contains(out.Raw[out.SystemBlocks[0].Span.S:out.SystemBlocks[0].Span.E], []byte("cache_control")) ||
		bytes.Contains(out.Raw[out.SystemBlocks[1].Span.S:out.SystemBlocks[1].Span.E], []byte("cache_control")) {
		t.Error("the marker belongs on the stable block, not the volatile one")
	}
}

func TestCacheBreakpointsOnlyForCustomAgents(t *testing.T) {
	r, _ := ParseRequest(req{system: pad(6000), msgs: []msg{user(text("hi"))}}.body())
	for _, s := range []Session{{Agent: "claude_code"}, {Agent: "codex"}, {Agent: "other"}, {Agent: "sdk"}} {
		if (CacheBreakpoints{}).Applies(r, s) {
			t.Errorf("agent %s was not marked custom but would be changed", s.Agent)
		}
	}
	if !(CacheBreakpoints{}).Applies(r, custom) {
		t.Error("a custom agent should be eligible")
	}
}

func TestCacheMinimum(t *testing.T) {
	tests := []struct {
		model string
		want  int
		ok    bool
	}{
		{"claude-opus-5-5", 512, true},
		{"claude-opus-5", 512, true},
		{"claude-sonnet-5", 1024, true}, // not confused with claude-sonnet-5-5
		{"claude-sonnet-5-5", 512, true},
		{"claude-opus-4-8", 1024, true},
		{"claude-opus-4-8-20260101", 1024, true},
		{"claude-opus-4-7", 2048, true},
		{"claude-haiku-4-5-20251001", 4096, true},
		{"claude-opus-4-6", 4096, true},
		{"claude-opus-4-9", 0, false}, // unknown future model: do not borrow claude-opus-4
		{"claude-opus-4-8-preview", 0, false},
		{"gpt-5", 0, false},
		{"", 0, false},
	}
	for _, tt := range tests {
		if got, ok := cacheMinimum(tt.model); got != tt.want || ok != tt.ok {
			t.Errorf("cacheMinimum(%q) = %d,%v want %d,%v", tt.model, got, ok, tt.want, tt.ok)
		}
	}
}

// --- cache stability with the layout optimizers on ---------------------------

func TestCacheStability50TurnsCustomAgent(t *testing.T) {
	states := map[string]string{}
	for _, n := range PipelineNames {
		states[n] = config.StateOn
	}
	states[config.OptimizerOutputTrimmer] = config.StateShadow
	cfg := config.Default()
	for k, v := range states {
		cfg.Optimizers[k] = v
	}
	cfg.Optimize.CustomAgents = []string{"sdk"}
	cfg.Optimize.BudgetMS = 1000
	newEngine := func() *Engine { return New(Options{Config: cfg, Getenv: func(string) string { return "" }}) }

	// The system prompt is large from turn 1, so the size check passes on every
	// turn and the decision to add markers never flips mid-session.
	system := pad(5000) + "Today's date: 2026-10-29\n" + pad(200)
	var prev []byte
	e := newEngine()
	for turn := 1; turn <= 50; turn++ {
		if turn%9 == 0 {
			e = newEngine() // gateway restart: no memo, no in-memory pins
		}
		body := req{system: system, tools: []string{"bash", "read"}, msgs: sessionTurns(turn)}.body()
		res := e.Run(context.Background(), input(body))
		if len(res.Failures) != 0 || !res.Changed {
			t.Fatalf("turn %d: failures=%+v changed=%v", turn, res.Failures, res.Changed)
		}
		if n := countMarkers(res.Body); n < 1 || n > 4 {
			t.Fatalf("turn %d: %d cache markers; the provider allows at most 4", turn, n)
		}
		if prev != nil && !bytes.HasPrefix(res.Body, prefixOf(t, prev)) {
			t.Fatalf("turn %d: transformed prefix differs from the previous turn", turn)
		}
		prev = res.Body
	}
}
