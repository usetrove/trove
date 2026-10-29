package optimize

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
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
}

// New returns an Engine.
func New(o Options) *Engine {
	e := &Engine{
		cfg: o.Config, pipeline: o.Pipeline, pins: newPinBook(o.Pins),
		killFile: o.KillFile, getenv: o.Getenv, now: o.Now, log: o.Log, bpt: o.BytesPerToken,
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
	for _, o := range e.pipeline {
		if s := effectiveState(o.Name(), e.cfg.OptimizerState(o.Name())); s == config.StateShadow || s == config.StateOn {
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

// Run applies the pipeline. It never returns an error and never panics: on any
// problem it returns the original body (fail open, CLAUDE.md rule 4).
func (e *Engine) Run(ctx context.Context, in Input) (res Result) {
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

	pin, err := e.pins.get(ctx, in.SessionKey, e.cfg, e.bpt, e.now())
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
	sess := Session{Key: in.SessionKey, Agent: in.Agent, Custom: pin.customFor(in.Agent)}

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
		if rep.Changed = len(out.Raw) != len(cur.Raw) || string(out.Raw) != string(cur.Raw); !rep.Changed {
			rep.BytesRemoved, rep.TokensRemoved = 0, 0
		} else if n := len(cur.Raw) - len(out.Raw); n > 0 {
			rep.BytesRemoved = n
			rep.TokensRemoved = int64(float64(n)/pin.BytesPerToken + 0.5)
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
		out, rep := o.Transform(ctx, r)
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
