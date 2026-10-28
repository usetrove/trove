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

	"github.com/usetrove/trove/internal/gateway/auth"
)

// maxRequestBody bounds how much of a request body is held in memory. It is
// above the largest documented provider limit (Anthropic Messages: 128 MB).
const maxRequestBody = 256 << 20

// maxCapture bounds the side copy of a response kept for usage extraction. If
// a response is larger, usage is recorded as unavailable; the client still
// receives every byte.
const maxCapture = 32 << 20

type flightKey struct{}

// flight is the state of one proxied request.
type flight struct {
	provider  string
	path      string // route without the provider prefix, no query
	start     time.Time
	ctx       context.Context // the client request context; ends when the client disconnects
	reqHeader http.Header
	reqBody   []byte

	mu        sync.Mutex
	status    int
	streamed  bool
	encoding  string
	firstByte time.Time
	recorded  bool
}

func (s *Server) forward(w http.ResponseWriter, r *http.Request, provider, prefix string, upstream *url.URL) {
	start := s.opts.Now()
	s.requests.Add(1)

	var d auth.Decision
	if provider == "anthropic" {
		d = auth.Anthropic(r.Header)
	} else {
		d = auth.OpenAI(r.Header)
	}
	if !d.OK {
		s.rejected.Add(1)
		s.log.Info("rejected a request that does not use API-key authentication", "provider", provider, "reason", d.Reason)
		authError(w, provider)
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
	f := &flight{provider: provider, path: path, start: start, ctx: r.Context(), reqHeader: r.Header.Clone(), reqBody: body}

	proxy := &httputil.ReverseProxy{
		Transport:     s.transport,
		ErrorLog:      slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
		FlushInterval: -1, // flush every write immediately: streams are never buffered
		Rewrite: func(pr *httputil.ProxyRequest) {
			out := pr.Out
			out.URL.Scheme, out.URL.Host = upstream.Scheme, upstream.Host
			out.URL.Path, out.URL.RawPath = path, rawPath
			out.URL.RawQuery = pr.In.URL.RawQuery
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
	f.streamed = strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")
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
		gone := t.f.ctx != nil && t.f.ctx.Err() != nil  // read now: the context is always cancelled once the handler returns
		go t.s.record(t.f, buf, over, clean, gone, end) // never on the client's path
	})
}

// upstreamFailed handles a transport error (no response from the provider).
func (s *Server) upstreamFailed(w http.ResponseWriter, r *http.Request, f *flight, err error) {
	class := "upstream_unreachable"
	if r.Context().Err() != nil {
		class = "client_disconnected" // the client left; nothing to send
	} else {
		s.log.Warn("could not reach the provider", "provider", f.provider, "err", err)
		providerError(w, f.provider, http.StatusBadGateway,
			"Trove could not reach "+f.provider+" ("+shortErr(err)+"). Check your internet connection and the upstreams settings in ~/.trove/config.toml.")
	}
	f.mu.Lock()
	f.status = 0
	f.mu.Unlock()
	go s.recordFailure(f, class, s.opts.Now())
}

func shortErr(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	msg := err.Error()
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

// authError tells the agent, in the provider's own error shape so it shows
// the message, that only API keys are accepted.
func authError(w http.ResponseWriter, provider string) {
	providerError(w, provider, http.StatusUnauthorized, auth.Message)
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
