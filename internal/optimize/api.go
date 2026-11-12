package optimize

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/usetrove/trove/internal/config"
)

// The engine has three entry points. Every adapter (gateway, hooks, harness
// plugins) calls them and holds no optimization logic of its own:
//
//	TransformToolInput  a tool call before it runs
//	TransformToolOutput a tool's output after it ran
//	TransformRequest    a whole provider request (gateway and plugins only)
//
// The tool-level points work on a neutral representation (ToolCall, ToolOutput).
// The request level keeps a provider codec behind the engine (Anthropic Messages
// today); adapters hand over bytes and never parse them.

// Point is a place in an agent's loop where an optimizer can act.
type Point string

// The points in an agent's loop where an optimizer can act.
const (
	PointToolInput  Point = "tool_input"
	PointToolOutput Point = "tool_output"
	PointRequest    Point = "request"
)

// Adapter names a way of reaching the engine.
type Adapter string

// The adapters that can reach the engine.
const (
	AdapterGateway Adapter = "gateway"
	AdapterHooks   Adapter = "hooks"
	AdapterPlugin  Adapter = "plugin"
)

// Adapters lists every adapter, in the order the capability matrix shows them.
var Adapters = []Adapter{AdapterGateway, AdapterHooks, AdapterPlugin}

// Level says how fully an adapter can run an optimizer.
type Level string

// How fully an adapter can run an optimizer.
const (
	LevelSupported  Level = "supported"
	LevelPartial    Level = "partial"
	LevelImpossible Level = "impossible"
)

// Support is one cell of the capability matrix.
type Support struct {
	Level  Level  `json:"level"`
	Reason string `json:"reason"`
}

// ToolCall is one tool invocation in a neutral form. Input is the tool's JSON
// arguments, byte for byte as the agent produced them.
type ToolCall struct {
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input,omitempty"`
}

// Part is one piece of tool output. Only text exists today.
type Part struct {
	Text string `json:"text"`
}

// ToolOutput is what a tool returned.
type ToolOutput struct {
	Parts   []Part `json:"parts"`
	IsError bool   `json:"is_error,omitempty"`
}

// RequestIn is one provider request as an adapter hands it over. Header carries
// only what the engine needs (Content-Encoding); adapters strip credentials
// first (CLAUDE.md rule 2).
type RequestIn struct {
	Provider string
	Path     string // route without provider prefix or query
	Header   http.Header
	Body     []byte
}

// Decision is what a tool-input optimizer asks the adapter to do besides pass
// the call through. The text is Trove's own wording, never user content.
type Decision struct {
	// Kind is one of the Kind* values below.
	Kind    string `json:"kind"`
	Message string `json:"message,omitempty"`
	// Bytes and Tokens are estimates of what the decision saves.
	Bytes  int64 `json:"bytes,omitempty"`
	Tokens int64 `json:"tokens,omitempty"`
}

// Decision kinds. "would_*" kinds come from shadow mode and change nothing.
const (
	KindWouldBlock   = "would_block"
	KindBlocked      = "blocked"
	KindRereadAnyway = "reread_anyway"
	KindWouldAdvise  = "would_advise"
	KindAdvised      = "advised"
	// KindRewritten means the call's input was changed; KindWouldRewrite is the
	// shadow-mode version. The rewritten input is the returned ToolCall.
	KindRewritten    = "rewritten"
	KindWouldRewrite = "would_rewrite"
	// KindReissued means the agent ran a command again exactly as it first wrote
	// it, after the command had been quieted: the quiet flag did not satisfy it.
	KindReissued = "reissued"
)

// IsDeny reports whether the adapter must refuse to run the call.
func (d Decision) IsDeny() bool { return d.Kind == KindBlocked }

// Base is what every optimizer declares.
type Base interface {
	Name() string
	Tier() Tier
}

// Capable is implemented by every optimizer. It makes the capability matrix
// and overhead accounting data, not documentation.
type Capable interface {
	Base
	// Points lists where the optimizer can act.
	Points() []Point
	// OverheadTokens is what the optimizer itself adds to the model's context on
	// every request it is enabled for: tool definitions and fixed injected text
	// (CLAUDE.md addendum rule 15). Text injected only when it acts is already
	// netted out of BytesRemoved. Zero means none.
	OverheadTokens() int
}

// SupportOverrider lets an optimizer replace the matrix's default answer for an
// adapter, with a reason.
type SupportOverrider interface {
	Support(a Adapter) (Support, bool)
}

// ToolInputOptimizer acts before a tool runs.
type ToolInputOptimizer interface {
	Capable
	TransformInput(ctx context.Context, call ToolCall, s Session) (ToolCall, Report)
}

// ToolOutputOptimizer acts on what a tool returned.
type ToolOutputOptimizer interface {
	Capable
	TransformOutput(ctx context.Context, call ToolCall, out ToolOutput, s Session) (ToolOutput, Report)
}

// ToolPipeline returns the tool-level optimizers in run order. The request-level
// Pipeline is separate because its order is a byte-level cache contract.
func ToolPipeline() []Base {
	return []Base{NoiseNormalizer{}, newOutputTrimmer(), RepeatRead{}, ReadLimit{}, QuietCommands{}, LargeOutputAdvice{}}
}

// TransformToolInput runs the tool-input optimizers on one call. It never
// panics and never returns an error: on any failure the original call comes
// back and the report says what failed. A shadow optimizer reports but leaves
// the call unchanged. Reports are returned only for optimizers that applied.
func (e *Engine) TransformToolInput(ctx context.Context, call ToolCall, s Session) (ToolCall, []Report) {
	cur := call
	var reps []Report
	ctx, cancel := context.WithTimeout(ctx, e.budget)
	defer cancel()
	for _, b := range e.tools {
		o, ok := b.(ToolInputOptimizer)
		if !ok {
			continue
		}
		state := e.toolState(o, s)
		if state != config.StateShadow && state != config.StateOn {
			continue
		}
		so := e.forOptimizer(s, state)
		out, rep, failed := guardTool(ctx, o.Name(), func() (ToolCall, Report) { return o.TransformInput(ctx, cur, so) })
		if failed != "" {
			reps = append(reps, failedReport(o, state, failed))
			continue
		}
		if rep.Name == "" {
			continue
		}
		rep.Name, rep.State = o.Name(), state
		rep.OverheadTokens = overheadFor(o, state)
		if state == config.StateOn && !sameJSON(out.Input, cur.Input) {
			rep.Changed = true
			cur = out
		}
		reps = append(reps, rep)
	}
	return cur, reps
}

// TransformToolOutput is TransformToolInput for a tool's output.
func (e *Engine) TransformToolOutput(ctx context.Context, call ToolCall, out ToolOutput, s Session) (ToolOutput, []Report) {
	cur := out
	// Keep what the tool produced, before any optimizer rewrites it, so a
	// stored original is the real output and not a normalized copy.
	parts := out.Parts
	s.Raw = func(i int) string {
		if i < 0 || i >= len(parts) {
			return ""
		}
		return parts[i].Text
	}
	var reps []Report
	ctx, cancel := context.WithTimeout(ctx, e.budget)
	defer cancel()
	for _, b := range e.tools {
		o, ok := b.(ToolOutputOptimizer)
		if !ok {
			continue
		}
		state := e.toolState(o, s)
		if state != config.StateShadow && state != config.StateOn {
			continue
		}
		so := e.forOptimizer(s, state)
		res, rep, failed := guardTool(ctx, o.Name(), func() (ToolOutput, Report) { return o.TransformOutput(ctx, call, cur, so) })
		if failed != "" {
			reps = append(reps, failedReport(o, state, failed))
			continue
		}
		if rep.Name == "" {
			continue
		}
		rep.Name, rep.State = o.Name(), state
		rep.OverheadTokens = overheadFor(o, state)
		before, after := outputBytes(cur), outputBytes(res)
		rep.Changed = before != after || !sameOutput(cur, res)
		if !rep.Changed {
			rep.BytesRemoved, rep.TokensRemoved = 0, 0
		} else if n := before - after; n > 0 {
			rep.BytesRemoved = n
			rep.TokensRemoved = int64(float64(n)/e.bpt + 0.5)
		}
		if state == config.StateOn && rep.Changed && len(rep.Originals) > 0 {
			// Same rule as the request level: store every original before the
			// shortened text can leave.
			if f := e.store(ctx, o.Name(), rep.Originals); f != nil {
				reps = append(reps, failedReport(o, state, f.Class))
				continue
			}
		}
		rep.Originals = nil
		if state == config.StateOn && rep.Changed {
			cur = res
		}
		reps = append(reps, rep)
	}
	return cur, reps
}

// toolState is the state an optimizer runs in for this call. The caller
// resolves it (pins, guardrail rollbacks and the kill switch differ per
// adapter); the engine only lowers it by the kill switch.
func (e *Engine) toolState(o Base, s Session) string {
	if off, _ := Disabled(e.killFile, e.getenv); off {
		return config.StateOff
	}
	if s.State == nil {
		return config.StateOff
	}
	switch st := effectiveStateFor(o.Name(), s.State(o.Name()), e.recovery != nil); st {
	case config.StateShadow, config.StateOn:
		return st
	}
	return config.StateOff
}

// forOptimizer is the session as one optimizer should see it.
func (e *Engine) forOptimizer(s Session, state string) Session {
	s.Mode = state
	if s.Now.IsZero() {
		s.Now = e.now()
	}
	return s
}

func overheadFor(o Capable, state string) int {
	if state == config.StateOn {
		return o.OverheadTokens()
	}
	return 0
}

func failedReport(o Base, state, class string) Report {
	return Report{Name: o.Name(), State: state, Err: class}
}

// guardTool runs fn behind a recover and the context's deadline. failed is ""
// on success, else one of panic or timeout; a Report with Err set is "error".
func guardTool[T any](ctx context.Context, _ string, fn func() (T, Report)) (T, Report, string) {
	type outcome struct {
		v      T
		rep    Report
		failed string
	}
	done := make(chan outcome, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				done <- outcome{failed: "panic"}
			}
		}()
		v, rep := fn()
		if rep.Err != "" {
			done <- outcome{failed: "error"}
			return
		}
		done <- outcome{v: v, rep: rep}
	}()
	var zero T
	select {
	case oc := <-done:
		return oc.v, oc.rep, oc.failed
	case <-ctx.Done():
		return zero, Report{}, "timeout"
	}
}

func outputBytes(o ToolOutput) int {
	n := 0
	for _, p := range o.Parts {
		n += len(p.Text)
	}
	return n
}

func sameOutput(a, b ToolOutput) bool {
	if len(a.Parts) != len(b.Parts) || a.IsError != b.IsError {
		return false
	}
	for i := range a.Parts {
		if a.Parts[i].Text != b.Parts[i].Text {
			return false
		}
	}
	return true
}

func sameJSON(a, b json.RawMessage) bool { return string(a) == string(b) }
