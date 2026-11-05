package optimize

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
)

// toolStub is a configurable tool-level optimizer for engine tests.
type toolStub struct {
	name     string
	overhead int
	inFn     func(ToolCall) (ToolCall, Report)
	outFn    func(ToolOutput) (ToolOutput, Report)
}

func (t toolStub) Name() string        { return t.name }
func (t toolStub) Tier() Tier          { return TierLossless }
func (t toolStub) Points() []Point     { return []Point{PointToolInput, PointToolOutput} }
func (t toolStub) OverheadTokens() int { return t.overhead }
func (t toolStub) TransformInput(_ context.Context, c ToolCall, _ Session) (ToolCall, Report) {
	return t.inFn(c)
}
func (t toolStub) TransformOutput(_ context.Context, _ ToolCall, o ToolOutput, _ Session) (ToolOutput, Report) {
	return t.outFn(o)
}

func toolEngine(t *testing.T, ms int, tools ...Base) *Engine {
	t.Helper()
	cfg := config.Default()
	cfg.Optimize.BudgetMS = ms
	return New(Options{Config: cfg, Tools: tools, Getenv: func(string) string { return "" }})
}

func stateOf(s string) Session {
	return Session{Key: "s", State: func(string) string { return s }}
}

func shorten(_ ToolOutput) (ToolOutput, Report) {
	return ToolOutput{Parts: []Part{{Text: "short"}}}, Report{Name: "x"}
}

func TestToolOutputShadowReportsButDoesNotChange(t *testing.T) {
	e := toolEngine(t, 100, toolStub{name: "x", outFn: shorten})
	in := ToolOutput{Parts: []Part{{Text: strings.Repeat("a", 100)}}}
	for _, tc := range []struct {
		state   string
		changed bool
	}{{config.StateShadow, false}, {config.StateOn, true}} {
		out, reps := e.TransformToolOutput(context.Background(), ToolCall{Name: "Bash"}, in, stateOf(tc.state))
		if len(reps) != 1 || !reps[0].Changed || reps[0].BytesRemoved != 95 || reps[0].State != tc.state {
			t.Fatalf("%s: reports = %+v", tc.state, reps)
		}
		if got := out.Parts[0].Text != in.Parts[0].Text; got != tc.changed {
			t.Fatalf("%s: output changed = %v, want %v", tc.state, got, tc.changed)
		}
	}
}

func TestToolLevelOffAndKillSwitchDoNothing(t *testing.T) {
	called := false
	e := toolEngine(t, 100, toolStub{name: "x", outFn: func(o ToolOutput) (ToolOutput, Report) { called = true; return shorten(o) }})
	in := ToolOutput{Parts: []Part{{Text: "abc"}}}
	if _, reps := e.TransformToolOutput(context.Background(), ToolCall{}, in, stateOf(config.StateOff)); len(reps) != 0 || called {
		t.Fatalf("off ran the optimizer: %+v", reps)
	}
	if _, reps := e.TransformToolOutput(context.Background(), ToolCall{}, in, Session{Key: "s"}); len(reps) != 0 || called {
		t.Fatal("a session with no state resolver must be off")
	}
	e.getenv = func(k string) string {
		if k == "TROVE_OPTIMIZE" {
			return "off"
		}
		return ""
	}
	if _, reps := e.TransformToolOutput(context.Background(), ToolCall{}, in, stateOf(config.StateOn)); len(reps) != 0 || called {
		t.Fatal("the kill switch must stop tool-level optimizers")
	}
}

func TestToolLevelFailsOpen(t *testing.T) {
	in := ToolOutput{Parts: []Part{{Text: "keep me"}}}
	for name, fn := range map[string]func(ToolOutput) (ToolOutput, Report){
		"panic": func(ToolOutput) (ToolOutput, Report) { panic("boom") },
		"error": func(ToolOutput) (ToolOutput, Report) { return ToolOutput{}, Report{Name: "x", Err: "bad"} },
		"timeout": func(ToolOutput) (ToolOutput, Report) {
			time.Sleep(300 * time.Millisecond)
			return shorten(in)
		},
	} {
		e := toolEngine(t, 20, toolStub{name: "x", outFn: fn})
		out, reps := e.TransformToolOutput(context.Background(), ToolCall{}, in, stateOf(config.StateOn))
		if out.Parts[0].Text != "keep me" {
			t.Fatalf("%s: output was changed: %+v", name, out)
		}
		if len(reps) != 1 || reps[0].Err != name {
			t.Fatalf("%s: reports = %+v", name, reps)
		}
	}
}

func TestToolInputChangeAndOverheadCharge(t *testing.T) {
	rewrite := func(c ToolCall) (ToolCall, Report) {
		return ToolCall{Name: c.Name, Input: json.RawMessage(`{"a":2}`)}, Report{Name: "x"}
	}
	e := toolEngine(t, 100, toolStub{name: "x", overhead: 40, inFn: rewrite})
	call := ToolCall{Name: "Read", Input: json.RawMessage(`{"a":1}`)}
	got, reps := e.TransformToolInput(context.Background(), call, stateOf(config.StateOn))
	if string(got.Input) != `{"a":2}` || len(reps) != 1 || reps[0].OverheadTokens != 40 {
		t.Fatalf("on: %s %+v", got.Input, reps)
	}
	if reps[0].NetTokens() != -40 {
		t.Fatalf("an optimizer that saves nothing must show its overhead as a net cost, got %d", reps[0].NetTokens())
	}
	got, reps = e.TransformToolInput(context.Background(), call, stateOf(config.StateShadow))
	if string(got.Input) != `{"a":1}` || reps[0].OverheadTokens != 0 {
		t.Fatalf("shadow must change nothing and charge nothing: %s %+v", got.Input, reps)
	}
}

func TestTransformRequestMatchesRun(t *testing.T) {
	cfg := config.Default()
	e := New(Options{Config: cfg, Getenv: func(string) string { return "" }})
	body := []byte(`{"model":"claude-sonnet-4-5","messages":[]}`)
	a := e.Run(context.Background(), Input{Provider: "anthropic", Path: "/v1/messages", Body: body, SessionKey: "k"})
	b := e.TransformRequest(context.Background(), RequestIn{Provider: "anthropic", Path: "/v1/messages", Body: body}, Session{Key: "k"})
	if string(a.Body) != string(b.Body) || a.Skipped != b.Skipped {
		t.Fatalf("Run and TransformRequest disagree: %+v vs %+v", a, b)
	}
}
