package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/usetrove/trove/internal/gateway/agent"
	"github.com/usetrove/trove/internal/gateway/auth"
	"github.com/usetrove/trove/internal/gateway/session"
	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/redact"
)

// maxRequestBody bounds how much of a request body is held in memory. It is
// above the largest documented provider limit (Anthropic Messages: 128 MB).
// It is a variable only so a test can lower it; nothing else changes it.
var maxRequestBody int64 = 256 << 20

// maxCapture bounds the side copy of a response kept for usage extraction. If
// a response is larger, usage is recorded as unavailable; the client still
// receives every byte.
const maxCapture = 32 << 20

type flightKey struct{}

// flight is the state of one proxied request.
type flight struct {
	provider  string // the response dialect and list-price provider: "anthropic" or "openai"
	rt        *route
	path      string // route without the provider prefix, no query
	usagePath string // path as the usage extractor and optimizer understand it
	start     time.Time
	ctx       context.Context // the client request context; ends when the client disconnects
	reqHeader http.Header
	reqBody   []byte // what was forwarded upstream (after any optimizer ran)

	// sessionKey is set only when the optimizer engine ran; it was derived from
	// the original body before any transform. Otherwise record derives it.
	sessionKey string
	keyed      bool
	opt        *optimize.Result

	mu        sync.Mutex
	status    int
	streamed  bool
	encoding  string
	firstByte time.Time
	recorded  bool
}

func (s *Server) forward(w http.ResponseWriter, r *http.Request, rt *route) {
	start := s.opts.Now()
	s.requests.Add(1)
	provider, prefix, upstream := rt.dialect, rt.prefix, rt.upstream

	if d := rt.decide(r.Header); !d.OK {
		s.rejected.Add(1)
		s.log.Info("rejected a request that does not use API-key authentication", "provider", rt.label, "reason", d.Reason)
		msg := auth.Message
		if rt.message != nil {
			msg = rt.message(d)
		}
		providerError(w, rt.shape, http.StatusUnauthorized, msg)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			plainError(w, http.StatusRequestEntityTooLarge, "The request body is larger than Trove will hold in memory (256 MB). Send it directly to the provider.")
			return
		}
		// The client went away or sent a broken body; nothing to forward.
		return
	}

	path := strings.TrimPrefix(r.URL.Path, prefix)
	if path == "" {
		path = "/"
	}
	rawPath := ""
	if r.URL.RawPath != "" {
		rawPath = strings.TrimPrefix(r.URL.RawPath, prefix)
	}
	usagePath := path
	if rt.optPath != nil {
		usagePath = rt.optPath(path)
	}
	f := &flight{provider: provider, rt: rt, path: path, usagePath: usagePath, start: start, ctx: r.Context(), reqHeader: withoutCredentials(r.Header)}
	body = s.optimizeBody(r, f, body)
	f.reqBody = body
	basePath := strings.TrimSuffix(upstream.Path, "/")
	rawBase := strings.TrimSuffix(upstream.EscapedPath(), "/")

	proxy := &httputil.ReverseProxy{
		Transport:     s.transport,
		ErrorLog:      slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
		FlushInterval: -1, // flush every write immediately: streams are never buffered
		Rewrite: func(pr *httputil.ProxyRequest) {
			out := pr.Out
			out.URL.Scheme, out.URL.Host = upstream.Scheme, upstream.Host
			out.URL.Path = basePath + path
			out.URL.RawPath = ""
			if rawPath != "" {
				out.URL.RawPath = rawBase + rawPath
			}
			out.URL.RawQuery = pr.In.URL.RawQuery
			out.Header.Del("X-Trove-Kind") // Trove's own label; the provider never sees it
			out.Host = upstream.Host
			out.Body = io.NopCloser(bytes.NewReader(body))
			out.ContentLength = int64(len(body))
			out.GetBody = nil
		},
		ModifyResponse: func(resp *http.Response) error {
			s.watchResponse(f, resp)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			s.upstreamFailed(w, r, f, err)
		},
	}
	proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), flightKey{}, f)))
}

// watchResponse wraps the response body in a tee. It runs before any byte is
// sent to the client and never alters the response.
func (s *Server) watchResponse(f *flight, resp *http.Response) {
	f.mu.Lock()
	f.status = resp.StatusCode
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	f.streamed = strings.HasPrefix(ct, "text/event-stream") || (f.rt.eventstream && strings.HasPrefix(ct, "application/vnd.amazon.eventstream"))
	f.encoding = strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	f.mu.Unlock()
	resp.Body = &tee{rc: resp.Body, f: f, s: s}
}

// tee copies response bytes to a bounded side buffer as the client reads them.
// The copy is a memcpy under an uncontended lock, so it cannot delay the
// client; parsing happens after the response ends, off the request path.
type tee struct {
	rc io.ReadCloser
	f  *flight
	s  *Server

	mu   sync.Mutex
	buf  []byte
	over bool
	once sync.Once
}

func (t *tee) Read(p []byte) (int, error) {
	n, err := t.rc.Read(p)
	if n > 0 {
		t.f.mu.Lock()
		if t.f.firstByte.IsZero() {
			t.f.firstByte = t.s.opts.Now()
		}
		t.f.mu.Unlock()
		t.mu.Lock()
		if !t.over {
			if len(t.buf)+n > maxCapture {
				t.over, t.buf = true, nil
			} else {
				t.buf = append(t.buf, p[:n]...)
			}
		}
		t.mu.Unlock()
	}
	if err != nil {
		t.end(errors.Is(err, io.EOF))
	}
	return n, err
}

// Close runs when the proxy is done with the body, including when the client
// disconnected early (then the body was not read to EOF).
func (t *tee) Close() error {
	err := t.rc.Close()
	t.end(false)
	return err
}

func (t *tee) end(clean bool) {
	t.once.Do(func() {
		t.mu.Lock()
		buf, over := t.buf, t.over
		t.buf = nil
		t.mu.Unlock()
		end := t.s.opts.Now()
		gone := t.f.ctx != nil && t.f.ctx.Err() != nil                        // read now: the context is always cancelled once the handler returns
		t.s.goRecord(func() { t.s.record(t.f, buf, over, clean, gone, end) }) // never on the client's path
	})
}

// upstreamFailed handles a transport error (no response from the provider).
func (s *Server) upstreamFailed(w http.ResponseWriter, r *http.Request, f *flight, err error) {
	class := "upstream_unreachable"
	if r.Context().Err() != nil {
		class = "client_disconnected" // the client left; nothing to send
	} else {
		s.log.Warn("could not reach the provider", "provider", f.rt.label, "err", err)
		providerError(w, f.rt.shape, http.StatusBadGateway,
			"Trove could not reach "+f.rt.label+" ("+shortErr(err)+"). Check your internet connection and the upstreams settings in ~/.trove/config.toml.")
	}
	f.mu.Lock()
	f.status = 0
	f.mu.Unlock()
	end := s.opts.Now()
	s.goRecord(func() { s.recordFailure(f, class, end) })
}

func shortErr(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	// Redact before truncating: cutting first could split a secret and hide it
	// from the patterns. This text goes to the client and so must be as clean
	// as the logs.
	msg := redact.String(err.Error())
	if len(msg) > 120 {
		msg = msg[:120]
	}
	return msg
}

func plainError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, msg+"\n")
}

func providerError(w http.ResponseWriter, provider string, code int, msg string) {
	var v any
	if provider == "anthropic" {
		t := "api_error"
		if code == http.StatusUnauthorized {
			t = "authentication_error"
		}
		v = map[string]any{"type": "error", "error": map[string]string{"type": t, "message": msg}}
	} else {
		v = map[string]any{"error": map[string]any{"message": msg, "type": "invalid_request_error", "param": nil, "code": nil}}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// optimizeBody runs the optimizer engine, if one is configured and enabled, and
// returns the body to forward. The engine never fails: on any problem it returns
// the original bytes. The session key is derived here, from the original body,
// so a transform can never change which session a request belongs to.
func (s *Server) optimizeBody(r *http.Request, f *flight, body []byte) []byte {
	eng := s.opts.Optimizer
	if eng == nil || !f.rt.optimize || !eng.Enabled() {
		return body
	}
	label, _ := agent.Label(r.Header)
	f.sessionKey, f.keyed = s.sessions.Key(session.DeriveKey(r.Header, body), f.start), true
	res := eng.Run(r.Context(), optimize.Input{
		Provider: f.provider, Path: f.usagePath, Header: withoutCredentials(r.Header), Body: body,
		SessionKey: f.sessionKey, Agent: label,
	})
	f.opt = &res
	for _, fl := range res.Failures {
		s.log.Warn("an optimizer could not finish, so the original request was forwarded unchanged", "optimizer", fl.Optimizer, "reason", fl.Class)
	}
	return res.Body
}

// headersKept are the only request headers anything after the credential check
// may read: labelling the agent, keying the session, and (for the optimizer)
// the encoding. Credentials are not among them, so no later code, including a
// future optimizer, can log, store or return a key by accident.
var headersKept = []string{"X-Trove-Kind", "User-Agent", "X-Claude-Code-Session-Id", "Content-Encoding", "Content-Type", "Anthropic-Version"}

func withoutCredentials(h http.Header) http.Header {
	out := make(http.Header, len(headersKept))
	for _, k := range headersKept {
		if v := h.Values(k); len(v) > 0 {
			out[http.CanonicalHeaderKey(k)] = append([]string(nil), v...)
		}
	}
	return out
}
