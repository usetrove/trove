// Package plugin is the adapter for harness plugins that are not written in Go
// (an OpenCode plugin, for example). The plugin starts `trove engine serve` once
// and talks to it over stdin and stdout, one JSON object per line. The adapter
// holds no optimization logic: it decodes a line, calls the engine, and encodes
// the answer.
//
// Fail open: if a line cannot be understood, or the engine cannot finish, the
// reply has ok=false and the plugin must use its original input. The reply to a
// request never contains anything the plugin did not send.
//
// Credentials: the protocol has no field for headers or keys, so none can reach
// the engine (CLAUDE.md rule 2). A request carries only provider, path, content
// encoding and body. The login is a three-valued label the plugin derives from
// non-secret fields, never a token.
//
// Methods (protocol version 1):
//
//	hello         {}                         -> {protocol, version, capabilities}
//	capabilities  {}                         -> {capabilities}
//	session       {session}                  -> {states}   the session's pinned optimizer states
//	tool_input    {session, call}            -> {call, reports}
//	tool_output   {session, call, output}    -> {output, reports}
//	request       {session, request}         -> {request, reports, failures}
//	observe       {session, observe}         -> {}         usage and outcome signals from the harness
//
// The session carries the key, the agent label, the harness name, the login and,
// optionally, the optimizer states the plugin resolved itself. When states are
// absent the server resolves and pins them for the session (config, guardrail
// rollbacks, the kill switch, the A/B arm), the same way the other adapters do.
// An optimizer missing from the states is off. When a harness is named, the
// session key is hashed before it is used or stored, and the decisions are
// recorded (metadata only) through the Recorder.
//
// Logins that are not api_key or copilot are shadow-only: optimizers count what
// they would do and change nothing, and a request is passed through untouched.
package plugin

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/redact"
)

// ProtocolVersion is bumped when a message changes in a way an older plugin
// cannot ignore. A plugin checks it in the hello reply and does nothing (fails
// open) if it does not support the version.
const ProtocolVersion = 1

// MaxLine is the largest message accepted: a 16 MiB request body as base64 plus
// framing.
const MaxLine = 24 << 20

// maxSessions bounds the per-session memory a long-lived server keeps.
const maxSessions = 256

// Session is what the plugin tells the engine about the session.
type Session struct {
	Key   string `json:"key"`
	Agent string `json:"agent,omitempty"`
	// Harness names the tool the plugin runs in (opencode). When set, the key is
	// hashed and decisions are recorded.
	Harness string `json:"harness,omitempty"`
	// Login is api_key, copilot or subscription_or_unknown. Empty counts as
	// subscription_or_unknown when a harness is named.
	Login string `json:"login,omitempty"`
	// Provider and Model are the harness's own identifiers, kept for labelling.
	Provider string            `json:"provider,omitempty"`
	Model    string            `json:"model,omitempty"`
	States   map[string]string `json:"states,omitempty"`
	TroveRun bool              `json:"trove_run,omitempty"`
	// Prompts is how many distinct user prompts the session has seen, for the
	// repeat-read message. Optional.
	Prompts int `json:"prompts,omitempty"`
	// Cannot lists what the harness is unable to do, so the engine counts instead
	// of acting where the plugin could not carry the decision out (a "blocked"
	// that the harness never enforced would be a wrong number). Known values:
	// deny (refuse a tool call), rewrite_input, rewrite_output, rewrite_request.
	Cannot []string `json:"cannot,omitempty"`
}

// Harness abilities a plugin can say it lacks (Session.Cannot).
const (
	CannotDeny           = "deny"
	CannotRewriteInput   = "rewrite_input"
	CannotRewriteOutput  = "rewrite_output"
	CannotRewriteRequest = "rewrite_request"
)

// needs says which harness ability each tool-level optimizer depends on at the
// point it acts. Request-level optimizers all need rewrite_request.
var needs = map[string]string{
	config.OptimizerHookRepeatRead:    CannotDeny,
	config.OptimizerHookReadLimit:     CannotRewriteInput,
	config.OptimizerHookQuietCommands: CannotRewriteInput,
	config.OptimizerNoiseNormalizer:   CannotRewriteOutput,
	config.OptimizerOutputTrimmer:     CannotRewriteOutput,
}

func cannot(in Session, what string) bool {
	for _, c := range in.Cannot {
		if c == what {
			return true
		}
	}
	return false
}

// RequestIn is a provider request. Body is base64 in JSON so it stays byte for byte.
type RequestIn struct {
	Provider        string `json:"provider"`
	Path            string `json:"path"`
	ContentEncoding string `json:"content_encoding,omitempty"`
	Body            []byte `json:"body"`
}

// RequestOut is the engine's answer for a request.
type RequestOut struct {
	Body     []byte             `json:"body"`
	Changed  bool               `json:"changed"`
	Skipped  string             `json:"skipped,omitempty"`
	Failures []optimize.Failure `json:"failures,omitempty"`
}

// Observe is a signal the harness reports: the usage of one model call, or an
// outcome. All fields are numbers or short enums.
type Observe struct {
	Kind             string   `json:"kind"` // usage | outcome
	InTokens         *int64   `json:"in_tokens,omitempty"`
	OutTokens        *int64   `json:"out_tokens,omitempty"`
	CacheReadTokens  *int64   `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens *int64   `json:"cache_write_tokens,omitempty"`
	CostUSD          *float64 `json:"cost_usd,omitempty"`
	Detail           string   `json:"detail,omitempty"` // outcome: tool_error | retry | abort | session_end | compaction
}

// Message is one line from the plugin.
type Message struct {
	ID      json.RawMessage      `json:"id"`
	Method  string               `json:"method"`
	Session Session              `json:"session"`
	Call    *optimize.ToolCall   `json:"call,omitempty"`
	Output  *optimize.ToolOutput `json:"output,omitempty"`
	Request *RequestIn           `json:"request,omitempty"`
	Observe *Observe             `json:"observe,omitempty"`
}

// Reply is one line back.
type Reply struct {
	ID       json.RawMessage `json:"id"`
	OK       bool            `json:"ok"`
	Error    string          `json:"error,omitempty"`
	Protocol int             `json:"protocol,omitempty"`
	Version  string          `json:"version,omitempty"`
	// Recoverable (hello) says the output trimmer can be on because trimmed text can be fetched back.
	Recoverable bool                 `json:"recoverable,omitempty"`
	States      map[string]string    `json:"states,omitempty"`
	Call        *optimize.ToolCall   `json:"call,omitempty"`
	Output      *optimize.ToolOutput `json:"output,omitempty"`
	Request     *RequestOut          `json:"request,omitempty"`
	Reports     []optimize.Report    `json:"reports,omitempty"`
	Matrix      []optimize.Row       `json:"capabilities,omitempty"`
}

// Resolver returns the optimizer states a new session starts with. It is called
// once per session key; the answer is pinned (CLAUDE.md rule 5). It should only
// lower states for guardrail rollbacks and the A/B arm, never raise them.
type Resolver func(sessionKey string) map[string]string

// Options configure a Server. The zero value is valid.
type Options struct {
	// Version is Trove's version, returned by hello.
	Version string
	// Resolver pins states for sessions whose plugin did not send any.
	Resolver Resolver
	// Recoverable says trimmed output can be stored and fetched back, so the output
	// trimmer may be on. It is reported by hello so the plugin can tell the user.
	Recoverable bool
	// Recorder receives decisions and harness reports. Nil records nothing.
	Recorder Recorder
	// RolledBack returns the optimizers a guardrail has put back to shadow. It is
	// asked on every call (the caller should cache), so a rollback takes effect at
	// once, as it does for hooks. It only ever lowers a state.
	RolledBack func() map[string]bool
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Server answers plugin messages with an engine.
type Server struct {
	eng  *optimize.Engine
	opts Options

	mu       sync.Mutex
	sessions map[string]*sessionState
	order    []string
}

// New returns a Server.
func New(eng *optimize.Engine, opts ...Options) *Server {
	s := &Server{eng: eng, sessions: map[string]*sessionState{}}
	if len(opts) > 0 {
		s.opts = opts[0]
	}
	if s.opts.Now == nil {
		s.opts.Now = time.Now
	}
	return s
}

// Serve reads messages from r until it ends or ctx is done, writing one reply
// per message to w. It returns nil on a clean end of input.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), MaxLine)
	bw := bufio.NewWriter(w)
	for sc.Scan() {
		if ctx.Err() != nil {
			return nil
		}
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		b, _ := json.Marshal(s.Handle(ctx, line))
		if _, err := bw.Write(append(b, '\n')); err != nil {
			return err
		}
		if err := bw.Flush(); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		// Too long or unreadable: tell the plugin, then stop. It must restart us.
		b, _ := json.Marshal(Reply{Error: "message too large or unreadable; the input was left unchanged"})
		_, _ = bw.Write(append(b, '\n'))
		_ = bw.Flush()
		return fmt.Errorf("reading plugin messages: %w", err)
	}
	return nil
}

// Handle answers one message. It never panics.
func (s *Server) Handle(ctx context.Context, line []byte) (rep Reply) {
	var m Message
	if err := json.Unmarshal(line, &m); err != nil {
		return Reply{Error: "not valid JSON; the input was left unchanged"}
	}
	rep.ID = m.ID
	defer func() {
		if p := recover(); p != nil {
			rep = Reply{ID: m.ID, Error: "internal error (" + redact.FormatPanic(p, nil) + "); the input was left unchanged"}
		}
	}()
	switch m.Method {
	case "hello":
		rep.OK, rep.Protocol, rep.Version, rep.Matrix, rep.Recoverable = true, ProtocolVersion, s.opts.Version, optimize.Matrix(), s.opts.Recoverable
	case "capabilities":
		rep.OK, rep.Matrix = true, optimize.Matrix()
	case "session":
		st := s.state(m.Session)
		rep.OK, rep.States = true, s.statesFor(st, m.Session)
	case "tool_input":
		if m.Call == nil {
			return Reply{ID: m.ID, Error: "tool_input needs a call"}
		}
		call, reps := s.eng.TransformToolInput(ctx, *m.Call, s.session(m.Session))
		rep.OK, rep.Call, rep.Reports = true, &call, reps
		s.recordReports(m.Session, reps)
	case "tool_output":
		if m.Call == nil || m.Output == nil {
			return Reply{ID: m.ID, Error: "tool_output needs a call and an output"}
		}
		out, reps := s.eng.TransformToolOutput(ctx, *m.Call, *m.Output, s.session(m.Session))
		rep.OK, rep.Output, rep.Reports = true, &out, reps
		s.recordReports(m.Session, reps)
	case "request":
		if m.Request == nil {
			return Reply{ID: m.ID, Error: "request needs a request"}
		}
		if m.Session.Harness != "" && !Eligible(NormLogin(m.Session.Login)) {
			// A login that may not be changed: hand the body back untouched.
			rep.OK = true
			rep.Request = &RequestOut{Body: m.Request.Body, Skipped: "login_not_eligible"}
			return rep
		}
		if cannot(m.Session, CannotRewriteRequest) {
			rep.OK = true
			rep.Request = &RequestOut{Body: m.Request.Body, Skipped: "harness_cannot_rewrite_request"}
			return rep
		}
		hdr := http.Header{}
		if m.Request.ContentEncoding != "" {
			hdr.Set("Content-Encoding", m.Request.ContentEncoding)
		}
		res := s.eng.TransformRequest(ctx,
			optimize.RequestIn{Provider: m.Request.Provider, Path: m.Request.Path, Header: hdr, Body: m.Request.Body},
			optimize.Session{Key: s.keyOf(m.Session), Agent: agentOf(m.Session)})
		rep.OK, rep.Reports = true, res.Applied
		rep.Request = &RequestOut{Body: res.Body, Changed: res.Changed, Skipped: res.Skipped, Failures: res.Failures}
		s.recordReports(m.Session, res.Applied)
	case "observe":
		if m.Observe == nil {
			return Reply{ID: m.ID, Error: "observe needs an observe object"}
		}
		if err := s.observe(m.Session, *m.Observe); err != nil {
			return Reply{ID: m.ID, Error: err.Error()}
		}
		rep.OK = true
	default:
		return Reply{ID: m.ID, Error: fmt.Sprintf("unknown method %q; known: hello, capabilities, session, tool_input, tool_output, request, observe", m.Method)}
	}
	return rep
}

// keyOf is the key the engine and the store use for a session: hashed when a
// harness is named, so the harness's own id never reaches the database.
func (s *Server) keyOf(in Session) string {
	if in.Harness == "" || in.Key == "" {
		return in.Key
	}
	sum := sha256.Sum256([]byte(in.Harness + "\x00" + in.Key))
	return "h-" + hex.EncodeToString(sum[:])[:24]
}

// agentOf is the agent label the engine sees. A harness plugin that names none gets
// "other", the label the gateway gives a client it does not recognise, so a user's
// custom_agents = ["other"] covers a harness the same on both paths. (Only "sdk"
// and "other" can be marked custom; the harness name is kept separately.)
func agentOf(in Session) string {
	switch {
	case in.Agent != "":
		return in.Agent
	case in.Harness != "":
		return "other"
	}
	return ""
}

// session builds the engine's view of a session.
func (s *Server) session(in Session) optimize.Session {
	st := s.state(in)
	states := s.statesFor(st, in)
	return optimize.Session{
		Key: s.keyOf(in), Agent: agentOf(in), TroveRun: in.TroveRun,
		Ledger: st.ledger(in.Prompts),
		State: func(name string) string {
			v := states[name]
			// The harness said it cannot carry out what this optimizer does: count it
			// in shadow, never act on it.
			if need, ok := needs[name]; ok && v == config.StateOn && cannot(in, need) {
				return config.StateShadow
			}
			return v
		},
	}
}

// sessionState is the server's memory of one session: its pinned states and
// the files it has read.
type sessionState struct {
	mu     sync.Mutex
	pinned map[string]string
	led    *memLedger
}

// state returns the session's memory, creating it (and pinning its states) on
// first use. A session without a key has no memory shared between calls.
func (s *Server) state(in Session) *sessionState {
	key := s.keyOf(in)
	if key == "" {
		return &sessionState{led: nil}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.sessions[key]
	if !ok {
		if len(s.order) >= maxSessions {
			delete(s.sessions, s.order[0])
			s.order = s.order[1:]
		}
		st = &sessionState{led: &memLedger{reads: map[string]optimize.ReadEntry{}}}
		if in.States == nil && s.opts.Resolver != nil {
			st.pinned = s.opts.Resolver(key)
		}
		s.sessions[key], s.order = st, append(s.order, key)
	}
	return st
}

func (st *sessionState) ledger(prompts int) optimize.ReadLedger {
	if st.led == nil {
		return nil
	}
	if prompts > 0 {
		st.led.mu.Lock()
		st.led.prompts = prompts
		st.led.mu.Unlock()
	}
	return st.led
}

// statesFor is what the optimizers see for this call: the states the plugin sent,
// else the pinned ones, lowered to shadow when a guardrail has rolled the
// optimizer back or the login may not be changed. It only ever lowers.
func (s *Server) statesFor(st *sessionState, in Session) map[string]string {
	src := in.States
	if src == nil {
		st.mu.Lock()
		src = st.pinned
		st.mu.Unlock()
	}
	var rolled map[string]bool
	if s.opts.RolledBack != nil {
		rolled = s.opts.RolledBack()
	}
	locked := in.Harness != "" && !Eligible(NormLogin(in.Login))
	if len(rolled) == 0 && !locked {
		return src
	}
	out := make(map[string]string, len(src))
	for k, v := range src {
		if v == config.StateOn && (locked || rolled[k]) {
			v = config.StateShadow
		}
		out[k] = v
	}
	return out
}

// recordReports turns optimizer reports into events.
func (s *Server) recordReports(in Session, reps []optimize.Report) {
	if s.opts.Recorder == nil || in.Harness == "" || !harnessRE.MatchString(in.Harness) || len(reps) == 0 {
		return
	}
	for _, r := range reps {
		kind := reportKind(r)
		if kind == "" {
			continue
		}
		e := s.base(in)
		e.Kind, e.Optimizer, e.State = kind, r.Name, r.State
		e.Bytes, e.Tokens, e.OverheadTokens = int64(r.BytesRemoved), r.TokensRemoved, int64(r.OverheadTokens)
		if r.Decision != nil {
			e.Bytes, e.Tokens = r.Decision.Bytes, r.Decision.Tokens
		}
		s.opts.Recorder.Record(e)
	}
}

// reportKind maps a report to an event kind, or "" when there is nothing to record.
func reportKind(r optimize.Report) string {
	if r.Err != "" {
		return ""
	}
	if r.Decision != nil {
		switch r.Decision.Kind {
		case optimize.KindBlocked, optimize.KindWouldBlock, optimize.KindAdvised, optimize.KindWouldAdvise,
			optimize.KindRereadAnyway, optimize.KindRewritten, optimize.KindWouldRewrite, optimize.KindReissued:
			return r.Decision.Kind
		}
		return ""
	}
	switch {
	case r.State == config.StateOn && r.Changed:
		return optimize.KindRewritten
	case r.State == config.StateShadow && r.TokensRemoved > 0:
		return optimize.KindWouldRewrite
	}
	return ""
}

func (s *Server) base(in Session) Event {
	return Event{TS: s.opts.Now(), SessionKey: s.keyOf(in), Harness: in.Harness, Login: NormLogin(in.Login),
		Provider: cleanName(in.Provider), Model: cleanName(in.Model)}
}

func (s *Server) observe(in Session, o Observe) error {
	if in.Harness == "" || !harnessRE.MatchString(in.Harness) {
		return fmt.Errorf("observe needs session.harness, a short lower-case name such as opencode")
	}
	if in.Key == "" {
		return fmt.Errorf("observe needs session.key")
	}
	e := s.base(in)
	switch o.Kind {
	case "usage":
		e.Kind = "usage"
		e.InTokens, e.OutTokens, e.CacheReadTokens, e.CacheWriteTokens, e.CostUSD = nonNeg(o.InTokens), nonNeg(o.OutTokens), nonNeg(o.CacheReadTokens), nonNeg(o.CacheWriteTokens), nonNegF(o.CostUSD)
	case "outcome":
		if !detailOK[o.Detail] {
			return fmt.Errorf("outcome detail %q is not one of tool_error, retry, abort, session_end, compaction", o.Detail)
		}
		e.Kind, e.Detail = "outcome", o.Detail
	default:
		return fmt.Errorf("observe kind %q is not usage or outcome", o.Kind)
	}
	if s.opts.Recorder != nil {
		s.opts.Recorder.Record(e)
	}
	return nil
}

func nonNeg(p *int64) *int64 {
	if p == nil || *p < 0 {
		return nil
	}
	return p
}

func nonNegF(p *float64) *float64 {
	if p == nil || *p < 0 {
		return nil
	}
	return p
}

// memLedger is a session's read memory, held in the server's memory only.
type memLedger struct {
	mu      sync.Mutex
	reads   map[string]optimize.ReadEntry
	prompts int
}

func (l *memLedger) Get(k string) (optimize.ReadEntry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.reads[k]
	return e, ok
}

func (l *memLedger) Put(k string, e optimize.ReadEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reads[k] = e
}

func (l *memLedger) Delete(k string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.reads, k)
}

func (l *memLedger) Prompts() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.prompts
}
func (l *memLedger) Save() error { return nil }
