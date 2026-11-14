package optimize

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/usetrove/trove/internal/config"
)

// MaxBody is the largest request body the engine will parse. Larger bodies are
// forwarded untouched, like analyze does.
const MaxBody = 16 << 20

// Options configures an Engine.
type Options struct {
	Config config.Config
	// Pins persists session pins; nil keeps them in memory only.
	Pins PinStore
	// KillFile is the path checked by the kill switch ("" disables the file check).
	KillFile string
	Getenv   func(string) string
	Now      func() time.Time
	Log      *slog.Logger
	// BytesPerToken converts removed bytes to estimated tokens for new sessions.
	BytesPerToken float64
	// Pipeline overrides the optimizer list; tests use it. Default: Pipeline().
	Pipeline []Optimizer
	// Gate lowers states for new sessions (see StateGate); nil changes nothing.
	Gate StateGate
	// Recovery stores the full text of output an optimizer shortens. Without it
	// the output trimmer cannot be "on": it stays in shadow, estimating only.
	Recovery RecoveryStore
	// Tools overrides the tool-level optimizer list; tests use it. Default: ToolPipeline().
	Tools []Base
	// ExtraStates returns the current states ("off" or "on") of third-party
	// tools, keyed by optimizer name, each time a session starts. Nil means none.
	ExtraStates func() map[string]string
	// AutopilotStates returns the states Autopilot chose for optimizers the user left
	// unset (see internal/autopilot). The engine calls it at most every few seconds
	// and applies the answer only to sessions that start afterwards, because a
	// session pins its states on its first request. Nil means no Autopilot. An error
	// must return nil: every failure lowers states, never raises them.
	AutopilotStates func() map[string]string
}

// RecoveryStore keeps full tool output so the agent can fetch it back with
// trove_fetch_full. Put returns the output's ID, the first 16 hex characters of
// the sha256 of the bytes.
type RecoveryStore interface {
	Put(data []byte) (string, error)
}

// Engine runs the optimizer pipeline over requests. It is safe for concurrent use.
type Engine struct {
	cfg      config.Config
	pipeline []Optimizer
	pins     *pinBook
	killFile string
	getenv   func(string) string
	now      func() time.Time
	log      *slog.Logger
	bpt      float64
	budget   time.Duration
	gate     StateGate
	recovery RecoveryStore
	tools    []Base

	apFn  func() map[string]string
	apMu  sync.Mutex
	apAt  time.Time
	apCfg config.Config
}

// New returns an Engine.
func New(o Options) *Engine {
	e := &Engine{
		cfg: o.Config, pipeline: o.Pipeline, pins: newPinBook(o.Pins),
		killFile: o.KillFile, getenv: o.Getenv, now: o.Now, log: o.Log, bpt: o.BytesPerToken, gate: o.Gate, recovery: o.Recovery,
	}
	e.pins.recoverable = o.Recovery != nil
	e.pins.extras = o.ExtraStates
	e.apFn = o.AutopilotStates
	e.tools = o.Tools
	if e.tools == nil {
		e.tools = ToolPipeline()
	}
	if e.pipeline == nil {
		e.pipeline = Pipeline()
	}
	if e.getenv == nil {
		e.getenv = os.Getenv
	}
	if e.now == nil {
		e.now = time.Now
	}
	if e.log == nil {
		e.log = slog.New(slog.DiscardHandler)
	}
	if e.bpt <= 0 {
		e.bpt = DefaultBytesPerToken
	}
	ms := o.Config.Optimize.BudgetMS
	if ms <= 0 {
		ms = 25
	}
	e.budget = time.Duration(ms) * time.Millisecond
	return e
}

// Enabled reports whether any optimizer is configured as shadow or on. When it
// is false the gateway skips the engine entirely, so a default install pays
// nothing for the framework.
func (e *Engine) Enabled() bool {
	cfg := e.current()
	for _, o := range e.pipeline {
		if s := effectiveStateFor(o.Name(), cfg.OptimizerState(o.Name()), e.recovery != nil); s == config.StateShadow || s == config.StateOn {
			return true
		}
	}
	return false
}

// Input is one request as the gateway sees it.
type Input struct {
	Provider string
	Path     string // route without provider prefix or query
	Header   http.Header
	Body     []byte
	// SessionKey must be derived from the original body, before any transform.
	SessionKey string
	Agent      string
}

// Result is what the gateway should forward and record.
type Result struct {
	// Body is what to send upstream: the original bytes unless an optimizer in
	// "on" state changed the request and nothing failed.
	Body     []byte
	Applied  []Report
	Failures []Failure
	// Skipped says why the engine did nothing (kill switch, not applicable, all
	// off). Empty when the pipeline ran.
	Skipped string
	// Changed is true when Body differs from the input body.
	Changed bool
}

// TokensRemoved sums the estimated tokens removed by optimizers in "on" state.
func (r Result) TokensRemoved() int64 {
	var n int64
	for _, a := range r.Applied {
		if a.State == config.StateOn {
			n += a.TokensRemoved
		}
	}
	return n
}

// Run is TransformRequest for callers that still use Input.
func (e *Engine) Run(ctx context.Context, in Input) Result {
	return e.TransformRequest(ctx, RequestIn{Provider: in.Provider, Path: in.Path, Header: in.Header, Body: in.Body},
		Session{Key: in.SessionKey, Agent: in.Agent})
}

// TransformRequest applies the request-level pipeline. It never returns an
// error and never panics: on any problem it returns the original body (fail
// open, CLAUDE.md rule 4). s.Key must be derived from the original body.
func (e *Engine) TransformRequest(ctx context.Context, rq RequestIn, s Session) (res Result) {
	in := Input{Provider: rq.Provider, Path: rq.Path, Header: rq.Header, Body: rq.Body, SessionKey: s.Key, Agent: s.Agent}
	res = Result{Body: in.Body}
	defer func() {
		if p := recover(); p != nil {
			e.log.Error("optimizer engine recovered from a panic; forwarding the original request")
			res = Result{Body: in.Body, Failures: []Failure{{Optimizer: "engine", Class: "panic"}}}
		}
	}()

	if off, why := Disabled(e.killFile, e.getenv); off {
		res.Skipped = "kill switch: " + why
		return res
	}
	if in.Provider != "anthropic" || in.Path != "/v1/messages" {
		res.Skipped = "not an Anthropic Messages request"
		return res
	}
	if in.Header.Get("Content-Encoding") != "" {
		res.Skipped = "request body is compressed"
		return res
	}
	if len(in.Body) == 0 || len(in.Body) > MaxBody {
		res.Skipped = "request body is empty or too large"
		return res
	}
	if in.SessionKey == "" {
		res.Skipped = "no session key"
		return res
	}

	pin, err := e.pins.get(ctx, in.SessionKey, e.current(), e.bpt, e.now(), e.gate)
	if err != nil {
		// The pin still works from memory; only restart-survival is at risk.
		e.log.Warn("could not save the session's optimizer settings; continuing with them in memory", "err", err)
	}
	active := false
	for _, o := range e.pipeline {
		if pin.States[o.Name()] != config.StateOff && pin.States[o.Name()] != "" {
			active = true
			break
		}
	}
	if !active {
		res.Skipped = "all optimizers are off for this session"
		return res
	}

	cur, err := ParseRequest(in.Body)
	if err != nil {
		res.Failures = []Failure{{Optimizer: "engine", Class: "malformed"}}
		return res
	}

	ctx, cancel := context.WithTimeout(ctx, e.budget)
	defer cancel()
	sess := Session{Key: in.SessionKey, Agent: in.Agent, Custom: pin.customFor(in.Agent), Raw: originalSpanText(cur)}

	for _, o := range e.pipeline {
		state := pin.States[o.Name()]
		if state != config.StateShadow && state != config.StateOn {
			continue
		}
		if err := cur.Ensure(); err != nil {
			// An earlier optimizer produced a body we cannot read. Never forward it.
			res.Failures = append(res.Failures, Failure{Optimizer: "engine", Class: "malformed"})
			return Result{Body: in.Body, Failures: res.Failures}
		}
		out, rep, fail := guard(ctx, o, cur, sess)
		if fail != nil {
			res.Failures = append(res.Failures, *fail)
			if state == config.StateOn {
				// Forward the original, untouched. Keep the failure record only.
				return Result{Body: in.Body, Failures: res.Failures}
			}
			continue
		}
		if rep.Name == "" {
			continue // did not apply
		}
		rep.Name, rep.State = o.Name(), state
		originals := rep.Originals
		rep.Originals = nil
		if rep.Changed = len(out.Raw) != len(cur.Raw) || string(out.Raw) != string(cur.Raw); !rep.Changed {
			rep.BytesRemoved, rep.TokensRemoved = 0, 0
		} else if n := len(cur.Raw) - len(out.Raw); n > 0 {
			rep.BytesRemoved = n
			rep.TokensRemoved = int64(float64(n)/pin.BytesPerToken + 0.5)
		}
		if state == config.StateOn && rep.Changed && len(originals) > 0 {
			// Every shortened output must be stored before the request leaves, or
			// the agent could be left with text it cannot get back. Storing is a
			// side effect, so it happens here and not inside the pure Transform.
			if f := e.store(ctx, o.Name(), originals); f != nil {
				res.Failures = append(res.Failures, *f)
				return Result{Body: in.Body, Failures: res.Failures}
			}
		}
		res.Applied = append(res.Applied, rep)
		if state == config.StateOn && rep.Changed {
			cur = out
		}
	}
	if string(cur.Raw) != string(in.Body) {
		// Edits are applied without re-reading the body between optimizers, so
		// check the final result once before it can be forwarded.
		if _, err := ParseRequest(cur.Raw); err != nil {
			return Result{Body: in.Body, Failures: append(res.Failures, Failure{Optimizer: "engine", Class: "malformed"})}
		}
		res.Body, res.Changed = cur.Raw, true
	}
	return res
}

// store saves the full text of shortened output. It reports a failure, so the
// caller forwards the original request, when any of it cannot be saved.
func (e *Engine) store(ctx context.Context, optimizer string, originals []string) *Failure {
	if e.recovery == nil {
		return &Failure{Optimizer: optimizer, Class: "recovery"}
	}
	for _, text := range originals {
		if ctx.Err() != nil {
			return &Failure{Optimizer: optimizer, Class: "timeout"}
		}
		id, err := e.recovery.Put([]byte(text))
		if err != nil || id != recoveryID(text) {
			if err != nil {
				e.log.Warn("could not store output before shortening it; forwarding the request unchanged", "err", err)
			}
			return &Failure{Optimizer: optimizer, Class: "recovery"}
		}
	}
	return nil
}

// guard runs one optimizer behind a recover and the time budget. A zero Report
// name means the optimizer did not apply to this request.
func guard(ctx context.Context, o Optimizer, r *Request, s Session) (*Request, Report, *Failure) {
	type outcome struct {
		req  *Request
		rep  Report
		fail *Failure
	}
	done := make(chan outcome, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				done <- outcome{fail: &Failure{Optimizer: o.Name(), Class: "panic"}}
			}
		}()
		if !o.Applies(r, s) {
			done <- outcome{req: r}
			return
		}
		var out *Request
		var rep Report
		if st, ok := o.(SessionTransformer); ok {
			out, rep = st.TransformSession(ctx, r, s)
		} else {
			out, rep = o.Transform(ctx, r)
		}
		switch {
		case out == nil:
			done <- outcome{fail: &Failure{Optimizer: o.Name(), Class: "error"}}
		case rep.Err != "":
			done <- outcome{fail: &Failure{Optimizer: o.Name(), Class: "error"}}
		default:
			rep.Name = o.Name()
			done <- outcome{req: out, rep: rep}
		}
	}()
	select {
	case oc := <-done:
		return oc.req, oc.rep, oc.fail
	case <-ctx.Done():
		// The goroutine may still be running; Transform is pure, so letting it
		// finish and be discarded is harmless.
		return nil, Report{}, &Failure{Optimizer: o.Name(), Class: "timeout"}
	}
}

// String describes the engine for logs.
func (e *Engine) String() string {
	return fmt.Sprintf("optimize.Engine(%d optimizers)", len(e.pipeline))
}

// autopilotRefresh is how long the engine reuses Autopilot's last answer.
const autopilotRefresh = 5 * time.Second

// current returns the configuration for new sessions: the one the engine was
// built with, plus Autopilot's states when AutopilotStates is set. It never
// changes a session that already has a pin.
func (e *Engine) current() config.Config {
	if e.apFn == nil {
		return e.cfg
	}
	e.apMu.Lock()
	defer e.apMu.Unlock()
	if now := e.now(); e.apAt.IsZero() || now.Sub(e.apAt) >= autopilotRefresh {
		e.apCfg = e.cfg.WithAutopilotStates(e.apFn())
		e.apAt = now
	}
	return e.apCfg
}

// GatewayState returns the effective state of a gateway-level behavior (see
// GatewayNames) for requests arriving now. The kill switch turns it off. It is a
// pure read of configuration, so it is cheap enough to call on every request.
// Today the only such behavior is in-flight dedup, whose "on" runs as shadow.
func (e *Engine) GatewayState(name string) string {
	if e == nil {
		return config.StateOff
	}
	if off, _ := Disabled(e.killFile, e.getenv); off {
		return config.StateOff
	}
	return effectiveStateFor(name, e.current().OptimizerState(name), e.recovery != nil)
}
