package gateway

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/analyze"
	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/redact"
	"github.com/usetrove/trove/internal/store"
)

const fakeKey = "FAKE-KEY-DO-NOT-LEAK-123"

type memRecorder struct {
	mu   sync.Mutex
	rows []store.Request
	ch   chan struct{}
}

func newMemRecorder() *memRecorder { return &memRecorder{ch: make(chan struct{}, 64)} }

func (m *memRecorder) Enqueue(r store.Request) bool {
	m.mu.Lock()
	m.rows = append(m.rows, r)
	m.mu.Unlock()
	m.ch <- struct{}{}
	return true
}

func (m *memRecorder) wait(t *testing.T) store.Request {
	t.Helper()
	select {
	case <-m.ch:
	case <-time.After(5 * time.Second):
		t.Fatal("no usage row was recorded")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rows[len(m.rows)-1]
}

type rig struct {
	up      *httptest.Server
	gw      *httptest.Server
	rec     *memRecorder
	logs    *bytes.Buffer
	handler http.Handler
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("usage", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newRig(t *testing.T, upstream http.Handler) *rig {
	t.Helper()
	up := httptest.NewServer(upstream)
	t.Cleanup(up.Close)
	cfg := config.Default()
	cfg.Upstreams.Anthropic, cfg.Upstreams.OpenAI = up.URL, up.URL
	rec := newMemRecorder()
	logs := &bytes.Buffer{}
	tbl, err := pricing.Default()
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{Config: cfg, Recorder: rec, Pricing: tbl, Analyzer: analyze.New(analyze.Options{Pricing: tbl}), Logger: redact.NewLogger(logs, true), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)
	return &rig{up: up, gw: gw, rec: rec, logs: logs, handler: s.Handler()}
}

// reply is the part of a response the tests inspect; do closes the body itself.
type reply struct {
	StatusCode int
	Header     http.Header
}

func do(t *testing.T, method, url string, hdr map[string]string, body []byte) (reply, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return reply{resp.StatusCode, resp.Header}, b
}

var anthropicHdr = map[string]string{"x-api-key": fakeKey, "anthropic-version": "2023-06-01", "Content-Type": "application/json", "User-Agent": "claude-cli/2.1.300 (external, cli)"}

func TestResponseBytesIdenticalWithAndWithoutGateway(t *testing.T) {
	stream := fixture(t, "anthropic_stream.sse")
	body := fixture(t, "anthropic_body.json")
	errBody := []byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"},"request_id":"req_X"}`)
	var gotPath, gotQuery, gotKey, gotBody string
	up := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotKey = r.URL.Path, r.URL.RawQuery, r.Header.Get("x-api-key")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		switch r.URL.Path {
		case "/v1/messages/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write(stream)
		case "/v1/messages/overloaded":
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(529)
			_, _ = w.Write(errBody)
		case "/v1/future/feature": // unknown path: forwarded verbatim
			w.WriteHeader(201)
			_, _ = w.Write([]byte("future"))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		}
	})
	g := newRig(t, up)
	for _, path := range []string{"/v1/messages?beta=true", "/v1/messages/stream", "/v1/messages/overloaded", "/v1/future/feature?x=1&y=%2F"} {
		directResp, direct := do(t, "POST", g.up.URL+path, anthropicHdr, []byte(`{"model":"m"}`))
		viaResp, via := do(t, "POST", g.gw.URL+"/anthropic"+path, anthropicHdr, []byte(`{"model":"m"}`))
		if !bytes.Equal(direct, via) {
			t.Errorf("%s: body differs\n direct %q\n via    %q", path, direct, via)
		}
		if directResp.StatusCode != viaResp.StatusCode || directResp.Header.Get("Retry-After") != viaResp.Header.Get("Retry-After") ||
			directResp.Header.Get("Content-Type") != viaResp.Header.Get("Content-Type") {
			t.Errorf("%s: status/headers differ: %d vs %d", path, directResp.StatusCode, viaResp.StatusCode)
		}
		if gotKey != fakeKey || gotBody != `{"model":"m"}` {
			t.Errorf("%s: upstream saw key %q body %q", path, gotKey, gotBody)
		}
		_ = g.rec.wait(t)
	}
	if gotPath != "/v1/future/feature" || gotQuery != "x=1&y=%2F" {
		t.Errorf("path/query not forwarded verbatim: %q ? %q", gotPath, gotQuery)
	}
}

func TestRecordsUsageAndCost(t *testing.T) {
	g := newRig(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(fixture(t, "anthropic_stream.sse"))
	}))
	do(t, "POST", g.gw.URL+"/anthropic/v1/messages", anthropicHdr, []byte(`{"model":"claude-opus-5-5","system":"s","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	row := g.rec.wait(t)
	if row.Provider != "anthropic" || row.Model != "claude-opus-5-5" || row.Endpoint != "/v1/messages" || !row.Streamed || row.StatusCode != 200 {
		t.Errorf("row identity wrong: %+v", row)
	}
	if row.InputUncachedTokens != 2679 || row.CacheReadTokens != 1500 || row.OutputTokens != 510 {
		t.Errorf("tokens differ from the provider's usage: %+v", row)
	}
	if row.Agent != "claude_code" || row.AgentVersion != "2.1.300" {
		t.Errorf("agent: %q %q", row.Agent, row.AgentVersion)
	}
	if row.TTFTMS == nil || row.SessionKey == "" || row.ErrorClass != "" {
		t.Errorf("ttft/session/error: %+v", row)
	}
	if !strings.Contains(string(row.UsageRaw), `"server_tool_use":{"web_search_requests":1}`) {
		t.Errorf("usage_raw: %s", row.UsageRaw)
	}
	assertNoSecrets(t, g)
}

func TestNonAPIKeyAuthIsRejected(t *testing.T) {
	called := false
	g := newRig(t, http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true }))
	cases := []struct {
		name, path string
		hdr        map[string]string
	}{
		{"anthropic bearer only", "/anthropic/v1/messages", map[string]string{"Authorization": "Bearer " + fakeKey}},
		{"anthropic oauth beta", "/anthropic/v1/messages", map[string]string{"x-api-key": fakeKey, "anthropic-beta": "oauth-2025-04-20"}},
		{"openai jwt", "/openai/v1/responses", map[string]string{"Authorization": "Bearer eyJhbGciOi.eyJzdWIi" + fakeKey + ".c2ln"}},
		{"openai none", "/openai/v1/responses", nil},
	}
	for _, tc := range cases {
		resp, body := do(t, "POST", g.gw.URL+tc.path, tc.hdr, []byte(`{}`))
		if resp.StatusCode != 401 || !strings.Contains(string(body), "API-key authentication only") || !strings.Contains(string(body), "remove the custom endpoint") {
			t.Errorf("%s: %d %s", tc.name, resp.StatusCode, body)
		}
		if strings.Contains(string(body), fakeKey) {
			t.Errorf("%s: the response echoed a credential", tc.name)
		}
	}
	if called {
		t.Error("a rejected request must never reach the upstream")
	}
	assertNoSecrets(t, g)
}

func TestGuards(t *testing.T) {
	g := newRig(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	call := func(host, origin, path string) int {
		req := httptest.NewRequest("GET", path, nil)
		req.Host = host
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		req.Header.Set("x-api-key", fakeKey)
		rr := httptest.NewRecorder()
		g.handler.ServeHTTP(rr, req)
		return rr.Code
	}
	for host, want := range map[string]int{"127.0.0.1:8787": 200, "localhost:8787": 200, "[::1]:8787": 200, "evil.example:8787": 403, "127.0.0.1.evil.com": 403, "10.0.0.5:8787": 403} {
		if got := call(host, "", "/trove/health"); got != want {
			t.Errorf("host %q: got %d want %d", host, got, want)
		}
	}
	if got := call("127.0.0.1:8787", "https://evil.example", "/trove/health"); got != 403 {
		t.Errorf("an Origin header must be refused, got %d", got)
	}
	if got := call("127.0.0.1:8787", "", "/nope"); got != 404 {
		t.Errorf("unknown route: %d", got)
	}
}

func TestListenAddressMustBeLoopback(t *testing.T) {
	for addr, ok := range map[string]bool{"127.0.0.1:8787": true, "[::1]:8787": true, "localhost:1": true, "0.0.0.0:8787": false, "192.168.1.5:8787": false, ":8787": false, "example.com:80": false, "garbage": false} {
		if err := ValidateListenAddr(addr); (err == nil) != ok {
			t.Errorf("%q: err=%v want ok=%v", addr, err, ok)
		}
	}
}

func TestStreamIsNotBuffered(t *testing.T) {
	release := make(chan struct{})
	g := newRig(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: ping\ndata: {\"type\":\"ping\"}\n\n"))
		w.(http.Flusher).Flush()
		<-release // the rest is withheld until the client has seen the first chunk
		_, _ = w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	req, _ := http.NewRequest("POST", g.gw.URL+"/anthropic/v1/messages", strings.NewReader(`{}`))
	for k, v := range anthropicHdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	first := make([]byte, 64)
	got := make(chan int, 1)
	go func() { n, _ := resp.Body.Read(first); got <- n }()
	select {
	case n := <-got:
		if !strings.HasPrefix(string(first[:n]), "event: ping") {
			t.Errorf("first chunk: %q", first[:n])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the first chunk did not arrive while the upstream was still streaming: the gateway is buffering")
	}
	close(release)
	rest, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(rest), "message_stop") {
		t.Errorf("rest: %q", rest)
	}
}

func TestClientDisconnectCancelsUpstream(t *testing.T) {
	upCancelled := make(chan struct{})
	g := newRig(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: ping\ndata: {\"type\":\"ping\"}\n\n"))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			close(upCancelled)
		case <-time.After(10 * time.Second):
		}
	}))
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", g.gw.URL+"/anthropic/v1/messages", strings.NewReader(`{}`))
	for k, v := range anthropicHdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = resp.Body.Read(make([]byte, 16))
	defer func() { _ = resp.Body.Close() }()
	cancel()
	select {
	case <-upCancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the upstream request was not cancelled after the client disconnected")
	}
	row := g.rec.wait(t)
	if row.ErrorClass != "client_disconnected" {
		t.Errorf("error class: %q", row.ErrorClass)
	}
}

func TestUpstreamAbortMidStreamStillRecords(t *testing.T) {
	g := newRig(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-opus-5-5\",\"usage\":{\"input_tokens\":9,\"output_tokens\":1}}}\n\n"))
		w.(http.Flusher).Flush()
		conn, _, _ := w.(http.Hijacker).Hijack()
		_ = conn.Close() // connection dies mid-stream
	}))
	req, _ := http.NewRequest("POST", g.gw.URL+"/anthropic/v1/messages", strings.NewReader(`{}`))
	for k, v := range anthropicHdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
	}
	row := g.rec.wait(t)
	if row.ErrorClass != "stream_incomplete" || row.InputUncachedTokens != 9 {
		t.Errorf("row: %+v", row)
	}
}

func TestMalformedEventsAndUnknownEventsDoNotBreakTheStream(t *testing.T) {
	payload := "event: x\ndata: {broken\n\nevent: ping\ndata: {\"type\":\"ping\"}\n\n" + string(fixture(t, "anthropic_stream.sse"))
	g := newRig(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(payload))
	}))
	_, body := do(t, "POST", g.gw.URL+"/anthropic/v1/messages", anthropicHdr, []byte(`{}`))
	if string(body) != payload {
		t.Error("bytes must pass through untouched, malformed events included")
	}
	if row := g.rec.wait(t); row.OutputTokens != 510 {
		t.Errorf("row: %+v", row)
	}
}

func TestSlowUpstreamIsNotCut(t *testing.T) {
	g := newRig(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(1500 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture(t, "anthropic_body.json"))
	}))
	resp, body := do(t, "POST", g.gw.URL+"/anthropic/v1/messages", anthropicHdr, []byte(`{}`))
	if resp.StatusCode != 200 || !bytes.Equal(body, fixture(t, "anthropic_body.json")) {
		t.Errorf("slow upstream: %d", resp.StatusCode)
	}
	if row := g.rec.wait(t); row.LatencyMS < 1400 {
		t.Errorf("latency %d ms should include the upstream delay", row.LatencyMS)
	}
}

func TestUpstreamUnreachableGivesPlainError(t *testing.T) {
	g := newRig(t, http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	g.up.Close()
	resp, body := do(t, "POST", g.gw.URL+"/anthropic/v1/messages", anthropicHdr, []byte(`{}`))
	if resp.StatusCode != 502 || !strings.Contains(string(body), "could not reach") {
		t.Errorf("%d %s", resp.StatusCode, body)
	}
	assertNoSecrets(t, g)
}

type panicRecorder struct{}

func (panicRecorder) Enqueue(store.Request) bool { panic("recorder exploded") }

func TestRecordingFailureNeverAffectsTheRequest(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture(t, "anthropic_body.json"))
	}))
	defer up.Close()
	cfg := config.Default()
	cfg.Upstreams.Anthropic = up.URL
	logs := &bytes.Buffer{}
	s, _ := New(Options{Config: cfg, Recorder: panicRecorder{}, Logger: redact.NewLogger(logs, true)})
	gw := httptest.NewServer(s.Handler())
	defer gw.Close()
	resp, body := do(t, "POST", gw.URL+"/anthropic/v1/messages", anthropicHdr, []byte(`{}`))
	if resp.StatusCode != 200 || !bytes.Equal(body, fixture(t, "anthropic_body.json")) {
		t.Errorf("request affected by recorder panic: %d", resp.StatusCode)
	}
	time.Sleep(200 * time.Millisecond)
	if !strings.Contains(logs.String(), "recording a usage row failed") {
		t.Errorf("the failure should be logged: %q", logs.String())
	}
	if strings.Contains(logs.String(), fakeKey) {
		t.Error("log leaked a credential")
	}
}

func TestGzipResponsePassesThroughAndIsStillParsed(t *testing.T) {
	var gz bytes.Buffer
	w := gzipWriter(&gz)
	_, _ = w.Write(fixture(t, "anthropic_body.json"))
	_ = w.Close()
	g := newRig(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(gz.Bytes())
	}))
	req, _ := http.NewRequest("POST", g.gw.URL+"/anthropic/v1/messages", strings.NewReader(`{}`))
	for k, v := range anthropicHdr {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept-Encoding", "gzip")
	tr := &http.Transport{DisableCompression: true}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(b, gz.Bytes()) || resp.Header.Get("Content-Encoding") != "gzip" {
		t.Error("compressed bytes must reach the client unchanged")
	}
	if row := g.rec.wait(t); row.OutputTokens != 20 || row.CacheReadTokens != 7 {
		t.Errorf("row: %+v", row)
	}
}

// assertNoSecrets fails if the planted credential appears in any log output.
func assertNoSecrets(t *testing.T, g *rig) {
	t.Helper()
	time.Sleep(50 * time.Millisecond)
	if strings.Contains(g.logs.String(), fakeKey) {
		t.Errorf("a credential appeared in the logs:\n%s", g.logs.String())
	}
}
