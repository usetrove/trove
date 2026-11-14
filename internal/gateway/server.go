// Package gateway is Trove's opt-in local gateway. In pass-through mode it
// forwards requests to the configured provider unchanged and records usage
// metadata. It binds to loopback only and handles API-key traffic only.
//
// Pass-through guarantees:
//   - the request body and the response body are forwarded byte for byte;
//   - request and response headers are forwarded minus hop-by-hop headers
//     (Go's HTTP stack may change header order and casing, which HTTP allows);
//   - Accept-Encoding is the client's own and responses are never re-encoded;
//   - response bytes are flushed to the client as they arrive and a copy is read
//     on the side: a slow recorder can only lose usage data, never delay bytes;
//   - recording failures are logged (redacted) and never affect the request.
//
// "ttft_ms" for streamed responses is the time until the first response body
// byte reaches the client (the first SSE bytes), not the first generated token.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/usetrove/trove/internal/analyze"
	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/gateway/auth"
	"github.com/usetrove/trove/internal/gateway/session"
	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/store"
)

// Route prefixes.
const (
	PrefixAnthropic = "/anthropic"
	PrefixOpenAI    = "/openai"
	HealthPath      = "/trove/health"
)

// Recorder receives one metadata row per response. *store.Writer satisfies it.
type Recorder interface {
	Enqueue(store.Request) bool
}

// Options configures a Server.
type Options struct {
	Config    config.Config
	Recorder  Recorder          // required
	Pricing   *pricing.Table    // nil: costs are recorded as unknown
	Analyzer  *analyze.Analyzer // nil: no structure analysis; runs after the response, never on the request path
	Optimizer *optimize.Engine  // nil: requests are forwarded byte for byte
	Logger    *slog.Logger      // must already redact; nil discards
	Version   string
	Now       func() time.Time  // tests only
	Transport http.RoundTripper // tests only; nil uses a pass-through transport
}

// Server is the gateway.
type Server struct {
	opts      Options
	log       *slog.Logger
	sessions  *session.Tracker
	started   time.Time
	requests  atomic.Uint64
	rejected  atomic.Uint64
	routes    []*route
	transport http.RoundTripper

	// inflight and fingerprint back in-flight dedup (shadow only, see inflight.go).
	// A nil fingerprint, when the system has no randomness, turns it off.
	inflight    *inflightBook
	fingerprint *auth.Fingerprinter

	// recording counts the goroutines that turn finished responses into rows, so shutdown
	// can let them reach the recorder before the recorder is closed.
	recording sync.WaitGroup
}

// New builds a Server. The upstream origins come from opts.Config.
func New(opts Options) (*Server, error) {
	if opts.Recorder == nil {
		return nil, errors.New("gateway: a recorder is required")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	routes, err := buildRoutes(opts.Config)
	if err != nil {
		return nil, err
	}
	for _, c := range opts.Config.Upstreams.Compat {
		if config.PlainHTTPUpstream(c.BaseURL) {
			opts.Logger.Warn("an OpenAI-compatible upstream uses plain http, so requests to it cross your network unencrypted", "upstream", c.Name)
		}
	}
	tr := opts.Transport
	if tr == nil {
		tr = passThroughTransport()
	}
	fp, err := auth.NewFingerprinter()
	if err != nil {
		opts.Logger.Warn("in-flight dedup is off because the system could not supply random numbers", "err", err)
		fp = nil
	}
	return &Server{
		opts: opts, log: opts.Logger, started: opts.Now(), routes: routes, transport: tr,
		inflight: newInflightBook(), fingerprint: fp,
		sessions: session.NewTracker(time.Duration(opts.Config.Gateway.IdleMinutes) * time.Minute),
	}, nil
}

// passThroughTransport never injects or removes encodings and sets no overall
// timeout, so it never cuts a response the upstream is still sending.
func passThroughTransport() http.RoundTripper {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DisableCompression = true
	t.ResponseHeaderTimeout = 0
	return t
}

// Handler returns the gateway's http.Handler.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.serve)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			if rec == http.ErrAbortHandler {
				panic(rec) // the proxy's deliberate way to cut the client connection after an upstream failure mid-stream
			}
			s.log.Error("gateway internal error; the request was not forwarded", "panic", fmt.Sprint(rec))
			plainError(w, http.StatusInternalServerError, "Trove hit an internal error before forwarding this request. Retry; if it keeps happening run `trove doctor`.")
		}
	}()

	if !loopbackHost(r.Host) {
		plainError(w, http.StatusForbidden, "Trove only answers requests addressed to localhost, 127.0.0.1 or [::1] (DNS-rebinding protection).")
		return
	}
	if r.Header.Get("Origin") != "" {
		plainError(w, http.StatusForbidden, "Trove refuses requests from web pages (an Origin header was present). Use your coding agent or an API client instead.")
		return
	}
	if r.URL.Path == HealthPath {
		s.health(w)
		return
	}
	rt := s.match(r.URL.Path)
	if rt == nil {
		plainError(w, http.StatusNotFound, "Unknown path. Use /anthropic/... or /openai/... (or a configured /vertex, /foundry, /bedrock or /compat/<name> route) to reach a provider, or /trove/health.")
		return
	}
	s.forward(w, r, rt)
}

func hasPrefix(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

func (s *Server) health(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":   "ok",
		"version":  s.opts.Version,
		"started":  s.started.UTC().Format(time.RFC3339),
		"requests": s.requests.Load(),
		"rejected": s.rejected.Load(),
	})
}

// loopbackHost reports whether a Host header names a loopback host.
func loopbackHost(hostport string) bool {
	h := hostport
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		h = host
	}
	h = strings.Trim(h, "[]")
	switch strings.ToLower(h) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// ValidateListenAddr accepts only loopback addresses ("127.0.0.1:8787",
// "[::1]:8787", "localhost:8787") and returns a plain-English error otherwise.
func ValidateListenAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%q is not a host:port address such as 127.0.0.1:8787", addr)
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("refusing to listen on %q: the gateway only binds to loopback (127.0.0.1) so nothing else on your network can use your API keys through it", addr)
}

// ListenAndServe serves until ctx ends, then shuts down gracefully (in-flight
// streams get a short grace period).
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	if err := ValidateListenAddr(addr); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("cannot listen on %s: %v. Is another process (or another trove gateway) using that port? Change gateway.port in the config to use a different one", addr, err)
	}
	return s.Serve(ctx, ln)
}

// Serve is ListenAndServe on an existing listener (which must be loopback).
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	if err := ValidateListenAddr(ln.Addr().String()); err != nil {
		_ = ln.Close()
		return err
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
		_ = srv.Close()
		s.waitForRecording(2 * time.Second)
		return nil
	}
}

// waitForRecording gives rows from just-finished responses up to d to be queued, so a
// request that completes right before shutdown is not dropped by the recorder closing.
func (s *Server) waitForRecording(d time.Duration) {
	done := make(chan struct{})
	go func() { s.recording.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
	}
}

// goRecord runs fn in the background and counts it for waitForRecording.
func (s *Server) goRecord(fn func()) {
	s.recording.Add(1)
	go func() {
		defer s.recording.Done()
		fn()
	}()
}
