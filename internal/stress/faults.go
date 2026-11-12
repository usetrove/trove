package stress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/gateway"
	"github.com/usetrove/trove/internal/hooks"
	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/recovery"
	"github.com/usetrove/trove/internal/store"
)

// faults is layer 5. Every case asserts the same thing: the failing part must fail open, and nothing it does
// may stop the agent. A case that cannot run in this build is reported as skipped, never as a pass.
func (e *env) faults() LayerResult {
	lr := LayerResult{Name: LayerFaults}
	type fcase struct {
		name string
		run  func() []Violation
	}
	cases := []fcase{
		{"upstream-http-500", func() []Violation { return e.gatewayFault(faultMode{status: 500}) }},
		{"upstream-http-429-retry-after", func() []Violation { return e.gatewayFault(faultMode{status: 429, retryAfter: "30"}) }},
		{"upstream-http-529-overloaded", func() []Violation { return e.gatewayFault(faultMode{status: 529}) }},
		{"upstream-connection-reset", func() []Violation { return e.gatewayFault(faultMode{err: errors.New("connection reset (injected)")}) }},
		{"upstream-hangs-past-client-timeout", func() []Violation { return e.gatewayFault(faultMode{hang: true}) }},
		{"upstream-partial-stream-cut", func() []Violation { return e.gatewayFault(faultMode{partial: true}) }},
		{"upstream-malformed-sse", func() []Violation { return e.gatewayFault(faultMode{status: 200, body: "data: not json\n\n\x00\xff"}) }},
		{"upstream-gzip-garbage", func() []Violation {
			return e.gatewayFault(faultMode{status: 200, body: "not gzip at all", gzipHeader: true})
		}},
		{"store-full-recorder-drops-everything", func() []Violation { return e.gatewayFault(faultMode{status: 200, full: true}) }},
		{"engine-recovery-store-fails", func() []Violation { return e.engineFault(failingRecovery{}) }},
		{"engine-recovery-returns-wrong-id", func() []Violation { return e.engineFault(wrongIDRecovery{}) }},
		{"engine-optimizer-panics", func() []Violation { return e.panicFault() }},
		{"engine-context-cancelled", func() []Violation { return e.cancelledFault() }},
		{"store-corrupt-database-file", func() []Violation { return e.corruptDBFault() }},
		{"recovery-folder-is-a-file", func() []Violation { return e.fileInsteadOfDirFault() }},
		{"hook-hangs-on-stdin", func() []Violation { return e.hookHangFault() }},
		{"hook-garbage-stdin", func() []Violation { return e.hookGarbageFault() }},
		{"hook-unknown-event", func() []Violation { return e.hookUnknownFault() }},
		{"kill-mid-write-partial-temp-file", func() []Violation { return e.killMidWriteFault() }},
		{"store-corrupted-on-disk", func() []Violation { return e.corruptStoreFault() }},
		{"full-disk-write-failures", func() []Violation { return e.fullDiskFault() }},
		{"read-only-database-handle", func() []Violation { return e.readOnlyFault() }},
		{"dead-mcp-server", func() []Violation { return e.deadMCPFault() }},
	}
	for _, c := range cases {
		e.record(&lr, c.name, c.run(), nil)
	}
	lr.Notes = append(lr.Notes, "every case asserts fail open: the component returns, the gateway still answers its health check")
	finish(&lr)
	return lr
}

// faultMode describes one fake upstream behavior.
type faultMode struct {
	status     int
	retryAfter string
	err        error
	hang       bool
	partial    bool
	body       string
	gzipHeader bool
	full       bool // the recorder drops every row, as a full store would
}

// faultUpstream returns the behavior a faultMode describes. It never dials out.
type faultUpstream struct{ m faultMode }

func (f faultUpstream) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
		_ = req.Body.Close()
	}
	switch {
	case f.m.err != nil:
		return nil, f.m.err
	case f.m.hang:
		<-req.Context().Done() // answers only when the client gives up
		return nil, req.Context().Err()
	case f.m.partial:
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}},
			Body:    &cutReader{first: "event: message_start\ndata: {\"type\":\"message_start\"", then: errors.New("stream cut (injected)")},
			Request: req}, nil
	}
	h := http.Header{"Content-Type": {"application/json"}}
	if f.m.retryAfter != "" {
		h.Set("Retry-After", f.m.retryAfter)
	}
	if f.m.gzipHeader {
		h.Set("Content-Encoding", "gzip")
	}
	body := f.m.body
	if body == "" {
		body = `{"type":"error","error":{"type":"api_error","message":"injected upstream failure"}}`
	}
	return &http.Response{StatusCode: f.m.status, Status: fmt.Sprint(f.m.status), Header: h,
		Body: io.NopCloser(strings.NewReader(body)), Request: req, ContentLength: int64(len(body))}, nil
}

// cutReader returns some bytes and then an error, as a dropped connection does.
type cutReader struct {
	first string
	then  error
	done  bool
}

func (c *cutReader) Read(p []byte) (int, error) {
	if !c.done {
		c.done = true
		return copy(p, c.first), nil
	}
	return 0, c.then
}

func (c *cutReader) Close() error { return nil }

// dropRecorder is a recorder whose buffer is full: every row is refused.
type dropRecorder struct{}

func (dropRecorder) Enqueue(store.Request) bool { return false }

// gatewayFault sends one request through the gateway with the given upstream behavior, then checks that the
// gateway still answers its health check. A client-side timeout is an allowed outcome for a hang; a gateway that
// stops answering health is not.
func (e *env) gatewayFault(m faultMode) []Violation {
	var rec gateway.Recorder = &countingRecorder{}
	if m.full {
		rec = dropRecorder{}
	}
	srv, err := newTestGateway(faultUpstream{m: m}, rec)
	if err != nil {
		return []Violation{{"gateway_start", err.Error()}}
	}
	defer srv.Close()
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	req, _ := messageRequest(srv.URL)
	if resp, err := client.Do(req); err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	if m.full {
		// The store being full must not change what the agent receives: it still gets a 200.
		req2, _ := messageRequest(srv.URL)
		resp, err := client.Do(req2)
		if err != nil || resp.StatusCode != http.StatusOK {
			return []Violation{{InvFailOpenOnError, "a full store stopped the agent getting its response"}}
		}
		_ = resp.Body.Close()
	}
	h, err := client.Get(srv.URL + gateway.HealthPath)
	if err != nil {
		return []Violation{{InvFailOpenOnError, "the gateway stopped answering its health check after the fault"}}
	}
	_ = h.Body.Close()
	if h.StatusCode != http.StatusOK {
		return []Violation{{InvFailOpenOnError, fmt.Sprintf("health check returned %d after the fault", h.StatusCode)}}
	}
	return nil
}

// failingRecovery refuses every store, as a full or broken disk would.
type failingRecovery struct{}

func (failingRecovery) Put([]byte) (string, error) { return "", errors.New("disk full (injected)") }

// wrongIDRecovery stores nothing and returns an ID that does not match the bytes.
type wrongIDRecovery struct{}

func (wrongIDRecovery) Put([]byte) (string, error) { return "0000000000000000", nil }

// bigToolBody is a request whose tool output the trimmer will shorten: at least 60 lines and 6 KB, shaped like a
// test runner's output, followed by enough turns that the output is not the newest one.
func bigToolBody() []byte {
	var b strings.Builder
	// The trimmer shortens only output that looks like a test runner's (it needs signature lines such as
	// "ok   pkg3  0.012s"), so the body is shaped like one.
	for i := 0; i < 600; i++ {
		fmt.Fprintf(&b, "ok   pkg%d  %d.%03ds\n", i%9, i%3, i%997)
	}
	// The output is followed by two more turns: the trimmer leaves the newest output whole, as an agent would
	// still be reading it.
	return []byte(`{"model":"claude-sonnet-5-5","max_tokens":4096,"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"keep the tests green"}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"go test ./..."}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":` + jstr(b.String()) + `}]},` +
		`{"role":"assistant","content":[{"type":"text","text":"reading the output now"}]},` +
		`{"role":"user","content":[{"type":"text","text":"continue"}]}]}`)
}

// engineFault checks that when the recovery store cannot keep the text, the engine forwards the original
// request unchanged, byte for byte. The fault must actually fire; a run where it does not is a failure of the
// test itself, not a pass.
func (e *env) engineFault(rec optimize.RecoveryStore) []Violation {
	body := bigToolBody()
	eng := newEngine(allNames(), rec, PricingDate)
	res := eng.TransformRequest(context.Background(), optimize.RequestIn{
		Provider: "anthropic", Path: "/v1/messages", Header: http.Header{}, Body: body,
	}, optimize.Session{Key: "fault-recovery", Agent: "claude_code", TroveRun: true})
	if !hasRecoveryFailure(res.Failures) {
		return []Violation{{"fault_not_exercised", "the trimmer never tried to store output, so this case tested nothing"}}
	}
	if !bytes.Equal(res.Body, body) {
		return []Violation{{InvFailOpenOnError, "the request was changed even though its output could not be stored"}}
	}
	return nil
}

func hasRecoveryFailure(fs []optimize.Failure) bool {
	for _, f := range fs {
		if f.Class == "recovery" {
			return true
		}
	}
	return false
}

// panicOpt is an optimizer that always panics. The engine must recover and forward the original request.
type panicOpt struct{}

func (panicOpt) Name() string                                     { return "stress_panic" }
func (panicOpt) Tier() optimize.Tier                              { return optimize.TierLossless }
func (panicOpt) Applies(*optimize.Request, optimize.Session) bool { return true }
func (panicOpt) Transform(context.Context, *optimize.Request) (*optimize.Request, optimize.Report) {
	panic("injected optimizer panic")
}

func (e *env) panicFault() []Violation {
	cfg := newEngineConfig([]string{"stress_panic"})
	eng := optimize.New(optimize.Options{Config: cfg, Pipeline: []optimize.Optimizer{panicOpt{}},
		Getenv: func(string) string { return "" }, Now: func() time.Time { return PricingDate }})
	body := bigToolBody()
	res := eng.TransformRequest(context.Background(), optimize.RequestIn{
		Provider: "anthropic", Path: "/v1/messages", Header: http.Header{}, Body: body,
	}, optimize.Session{Key: "fault-panic", Agent: "claude_code", TroveRun: true})
	if !bytes.Equal(res.Body, body) {
		return []Violation{{InvFailOpenOnError, "a panicking optimizer changed the request"}}
	}
	return nil
}

// cancelledFault checks a request whose context is already cancelled: the engine returns the original.
func (e *env) cancelledFault() []Violation {
	body := bigToolBody()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	eng := newEngine(allNames(), &memStore{}, PricingDate)
	res := eng.TransformRequest(ctx, optimize.RequestIn{
		Provider: "anthropic", Path: "/v1/messages", Header: http.Header{}, Body: body,
	}, optimize.Session{Key: "fault-cancel", Agent: "claude_code", TroveRun: true})
	if !bytes.Equal(res.Body, body) {
		return []Violation{{InvFailOpenOnError, "a cancelled request was changed"}}
	}
	return nil
}

// corruptDBFault writes garbage where the database should be. Opening it must return a plain error, not panic
// and not succeed silently.
func (e *env) corruptDBFault() (out []Violation) {
	dir := filepath.Join(e.dir, "corrupt")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return []Violation{{"setup", err.Error()}}
	}
	if err := os.WriteFile(filepath.Join(dir, "trove.db"), []byte("this is not a sqlite database\x00\xff"), 0o600); err != nil {
		return []Violation{{"setup", err.Error()}}
	}
	defer func() {
		if r := recover(); r != nil {
			out = []Violation{{InvFailOpenOnError, fmt.Sprintf("opening a corrupt database panicked: %v", r)}}
		}
	}()
	db, err := store.Open(dir)
	if err == nil {
		_ = db.Close()
		return []Violation{{InvFailOpenOnError, "a corrupt database opened without an error"}}
	}
	if strings.TrimSpace(err.Error()) == "" {
		return []Violation{{InvFailOpenOnError, "a corrupt database gave an empty error message"}}
	}
	return nil
}

// fileInsteadOfDirFault puts a regular file where the recovery folder should be. Opening it must fail with a
// plain error.
func (e *env) fileInsteadOfDirFault() (out []Violation) {
	path := filepath.Join(e.dir, "recovery-as-file")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		return []Violation{{"setup", err.Error()}}
	}
	defer func() {
		if r := recover(); r != nil {
			out = []Violation{{InvFailOpenOnError, fmt.Sprintf("opening the recovery folder panicked: %v", r)}}
		}
	}()
	if _, err := recovery.Open(path, recovery.Options{}); err == nil {
		return []Violation{{InvFailOpenOnError, "the recovery store opened on top of a file"}}
	}
	return nil
}

// blockingReader is a stdin that never delivers and never closes until released.
type blockingReader struct{ release chan struct{} }

func (b blockingReader) Read([]byte) (int, error) { <-b.release; return 0, io.EOF }

// hookHangFault runs a hook whose input never arrives. The hook must return exit code 0 within its deadline,
// so the agent is let through.
func (e *env) hookHangFault() []Violation {
	br := blockingReader{release: make(chan struct{})}
	defer close(br.release)
	var out bytes.Buffer
	start := time.Now()
	code := hooks.Main(hooks.Env{Args: []string{hooks.EventPreRead, "--data-dir", filepath.Join(e.dir, "hooks")},
		Stdin: br, Stdout: &out, Getenv: func(string) string { return "" }})
	took := time.Since(start)
	var vs []Violation
	if code != 0 {
		vs = append(vs, Violation{InvFailOpenOnError, fmt.Sprintf("a hanging hook exited with code %d; only 0 lets the agent continue", code)})
	}
	if took > 3*time.Second {
		vs = append(vs, Violation{InvFailOpenOnError, fmt.Sprintf("a hanging hook held the agent for %s", took.Round(time.Millisecond))})
	}
	return vs
}

// hookGarbageFault sends bytes that are not JSON. The hook must exit 0, and anything it writes must be JSON.
func (e *env) hookGarbageFault() []Violation {
	var out bytes.Buffer
	code := hooks.Main(hooks.Env{Args: []string{hooks.EventPreRead, "--data-dir", filepath.Join(e.dir, "hooks")},
		Stdin: strings.NewReader("\x00\xff{{{ not json"), Stdout: &out, Getenv: func(string) string { return "" }})
	var vs []Violation
	if code != 0 {
		vs = append(vs, Violation{InvFailOpenOnError, fmt.Sprintf("a hook given garbage exited with code %d", code)})
	}
	if out.Len() > 0 && !json.Valid(out.Bytes()) {
		vs = append(vs, Violation{InvFailOpenOnError, "a hook wrote something that is not JSON to the agent"})
	}
	return vs
}

// hookUnknownFault names an event that does not exist. It must exit 0.
func (e *env) hookUnknownFault() []Violation {
	var out bytes.Buffer
	code := hooks.Main(hooks.Env{Args: []string{"no_such_event", "--data-dir", filepath.Join(e.dir, "hooks")},
		Stdin: bytes.NewReader(nil), Stdout: &out, Getenv: func(string) string { return "" }})
	if code != 0 {
		return []Violation{{InvFailOpenOnError, fmt.Sprintf("an unknown hook event exited with code %d", code)}}
	}
	return nil
}

// newEngineConfig returns a config with only the named optimizers on.
func newEngineConfig(names []string) config.Config {
	cfg := config.Default()
	cfg.Optimize.BudgetMS = 60000
	cfg.Optimizers = map[string]string{}
	for _, n := range names {
		cfg.Optimizers[n] = config.StateOn
	}
	return cfg
}
