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
// encoding and body.
//
// Methods:
//
//	capabilities  the capability matrix
//	tool_input    {session, call}           -> {call, reports}
//	tool_output   {session, call, output}   -> {output, reports}
//	request       {session, request}        -> {request, reports, failures}
//
// The session carries the key, the agent label and the optimizer states the
// plugin resolved for it (pins and rollbacks differ per harness). An optimizer
// missing from states is off.
package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/redact"
)

// MaxLine is the largest message accepted: a 16 MiB request body as base64 plus
// framing.
const MaxLine = 24 << 20

// maxLedgers bounds the per-session read memory a long-lived server keeps.
const maxLedgers = 256

// Session is what the plugin tells the engine about the session.
type Session struct {
	Key      string            `json:"key"`
	Agent    string            `json:"agent,omitempty"`
	States   map[string]string `json:"states,omitempty"`
	TroveRun bool              `json:"trove_run,omitempty"`
	// Prompts is how many distinct user prompts the session has seen, for the
	// repeat-read message. Optional.
	Prompts int `json:"prompts,omitempty"`
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

// Message is one line from the plugin.
type Message struct {
	ID      json.RawMessage      `json:"id"`
	Method  string               `json:"method"`
	Session Session              `json:"session"`
	Call    *optimize.ToolCall   `json:"call,omitempty"`
	Output  *optimize.ToolOutput `json:"output,omitempty"`
	Request *RequestIn           `json:"request,omitempty"`
}

// Reply is one line back.
type Reply struct {
	ID      json.RawMessage      `json:"id"`
	OK      bool                 `json:"ok"`
	Error   string               `json:"error,omitempty"`
	Call    *optimize.ToolCall   `json:"call,omitempty"`
	Output  *optimize.ToolOutput `json:"output,omitempty"`
	Request *RequestOut          `json:"request,omitempty"`
	Reports []optimize.Report    `json:"reports,omitempty"`
	Matrix  []optimize.Row       `json:"capabilities,omitempty"`
}

// Server answers plugin messages with an engine.
type Server struct {
	eng *optimize.Engine

	mu      sync.Mutex
	ledgers map[string]*memLedger
	order   []string
}

// New returns a Server.
func New(eng *optimize.Engine) *Server {
	return &Server{eng: eng, ledgers: map[string]*memLedger{}}
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
	case "capabilities":
		rep.OK, rep.Matrix = true, optimize.Matrix()
	case "tool_input":
		if m.Call == nil {
			return Reply{ID: m.ID, Error: "tool_input needs a call"}
		}
		call, reps := s.eng.TransformToolInput(ctx, *m.Call, s.session(m.Session))
		rep.OK, rep.Call, rep.Reports = true, &call, reps
	case "tool_output":
		if m.Call == nil || m.Output == nil {
			return Reply{ID: m.ID, Error: "tool_output needs a call and an output"}
		}
		out, reps := s.eng.TransformToolOutput(ctx, *m.Call, *m.Output, s.session(m.Session))
		rep.OK, rep.Output, rep.Reports = true, &out, reps
	case "request":
		if m.Request == nil {
			return Reply{ID: m.ID, Error: "request needs a request"}
		}
		hdr := http.Header{}
		if m.Request.ContentEncoding != "" {
			hdr.Set("Content-Encoding", m.Request.ContentEncoding)
		}
		res := s.eng.TransformRequest(ctx,
			optimize.RequestIn{Provider: m.Request.Provider, Path: m.Request.Path, Header: hdr, Body: m.Request.Body},
			optimize.Session{Key: m.Session.Key, Agent: m.Session.Agent})
		rep.OK, rep.Reports = true, res.Applied
		rep.Request = &RequestOut{Body: res.Body, Changed: res.Changed, Skipped: res.Skipped, Failures: res.Failures}
	default:
		return Reply{ID: m.ID, Error: fmt.Sprintf("unknown method %q; known: capabilities, tool_input, tool_output, request", m.Method)}
	}
	return rep
}

func (s *Server) session(in Session) optimize.Session {
	states := in.States
	return optimize.Session{
		Key: in.Key, Agent: in.Agent, TroveRun: in.TroveRun,
		Ledger: s.ledger(in.Key, in.Prompts),
		State:  func(name string) string { return states[name] },
	}
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

func (l *memLedger) Prompts() int { return l.prompts }
func (l *memLedger) Save() error  { return nil }

func (s *Server) ledger(key string, prompts int) optimize.ReadLedger {
	if key == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.ledgers[key]
	if !ok {
		if len(s.order) >= maxLedgers {
			delete(s.ledgers, s.order[0])
			s.order = s.order[1:]
		}
		l = &memLedger{reads: map[string]optimize.ReadEntry{}}
		s.ledgers[key], s.order = l, append(s.order, key)
	}
	if prompts > 0 {
		l.prompts = prompts
	}
	return l
}
