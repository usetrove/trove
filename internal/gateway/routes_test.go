package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/usetrove/trove/internal/analyze"
	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/gateway/auth"
	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/redact"
	"github.com/usetrove/trove/internal/store"
)

// seen records what the fake provider received.
type seen struct {
	mu    sync.Mutex
	calls int
	path  string
	hdr   http.Header
	body  string
}

func (s *seen) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// jsonUpstream answers every request with a fixture body and records the request.
func jsonUpstream(t *testing.T, s *seen, name string) http.Handler {
	t.Helper()
	body := fixture(t, name)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.calls++
		s.path, s.hdr, s.body = r.URL.Path, r.Header.Clone(), string(b)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})
}

// routeRig starts a fake provider and a gateway whose config the test adjusts
// once the provider's URL is known.
func routeRig(t *testing.T, h http.Handler, set func(cfg *config.Config, upURL string)) *rig {
	t.Helper()
	up := httptest.NewServer(h)
	t.Cleanup(up.Close)
	cfg := config.Default()
	cfg.Upstreams.Anthropic, cfg.Upstreams.OpenAI = up.URL, up.URL
	set(&cfg, up.URL)
	rec := newMemRecorder()
	logs := &syncBuffer{}
	tbl, err := pricing.Default()
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{Config: cfg, Recorder: rec, Pricing: tbl, Analyzer: analyze.New(analyze.Options{Pricing: tbl}),
		Logger: redact.NewLogger(logs, true), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)
	return &rig{up: up, gw: gw, rec: rec, logs: logs, handler: s.Handler()}
}

func TestVertexRouteForwardsAndRecords(t *testing.T) {
	var up seen
	g := routeRig(t, jsonUpstream(t, &up, "anthropic_body.json"), func(c *config.Config, u string) { c.Upstreams.Vertex = u })
	resp, _ := do(t, "POST", g.gw.URL+"/vertex/v1/projects/p/locations/global/publishers/anthropic/models/claude-opus-5-5:rawPredict",
		map[string]string{"Authorization": "Bearer ya29.FAKE-GOOGLE-TOKEN", "Content-Type": "application/json", "X-Trove-Kind": "automated"},
		[]byte(`{"anthropic_version":"vertex-2023-10-16","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`))
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	row := g.rec.wait(t)
	if row.Provider != "vertex" || row.Kind != store.KindAutomated {
		t.Errorf("provider %q kind %q", row.Provider, row.Kind)
	}
	if row.InputUncachedTokens != 10 || row.OutputTokens != 20 {
		t.Errorf("tokens %d/%d", row.InputUncachedTokens, row.OutputTokens)
	}
	if row.CostUSD != nil {
		t.Errorf("vertex has no list price on file; cost must stay unknown, got %v", *row.CostUSD)
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	if up.path != "/v1/projects/p/locations/global/publishers/anthropic/models/claude-opus-5-5:rawPredict" {
		t.Errorf("upstream path %q", up.path)
	}
	if up.hdr.Get("Authorization") != "Bearer ya29.FAKE-GOOGLE-TOKEN" {
		t.Error("the cloud credential was not forwarded")
	}
	if up.hdr.Get("X-Trove-Kind") != "" {
		t.Error("X-Trove-Kind reached the provider")
	}
}

func TestFoundryRouteKeepsTheBasePath(t *testing.T) {
	var up seen
	g := routeRig(t, jsonUpstream(t, &up, "anthropic_body.json"), func(c *config.Config, u string) { c.Upstreams.Foundry = u + "/anthropic" })
	// An Entra ID bearer token is a JWT: normal for Foundry, refused as a session token elsewhere.
	resp, _ := do(t, "POST", g.gw.URL+"/foundry/v1/messages",
		map[string]string{"Authorization": "Bearer eyJhbGciOi.eyJzdWIiOi.c2ln", "Content-Type": "application/json"},
		[]byte(`{"model":"claude-opus-5-5","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`))
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	row := g.rec.wait(t)
	if row.Provider != "foundry" || row.OutputTokens != 20 {
		t.Errorf("row %+v", row)
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	if up.path != "/anthropic/v1/messages" {
		t.Errorf("upstream path %q", up.path)
	}
}

func TestBedrockSigV4IsRefusedAndBearerKeyForwards(t *testing.T) {
	var up seen
	g := routeRig(t, jsonUpstream(t, &up, "anthropic_body.json"), func(c *config.Config, u string) { c.Upstreams.Bedrock = u })
	path := g.gw.URL + "/bedrock/model/us.anthropic.claude-opus-5-5/invoke"
	body := []byte(`{"anthropic_version":"bedrock-2023-05-31","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`)

	resp, got := do(t, "POST", path, map[string]string{
		"Authorization": "AWS4-HMAC-SHA256 Credential=FAKESIGKEY/20261104/us-east-1/bedrock/aws4_request, SignedHeaders=host, Signature=00",
		"Content-Type":  "application/json"}, body)
	if resp.StatusCode != 401 || !strings.Contains(string(got), "SigV4") || !strings.Contains(string(got), "Bedrock API key") {
		t.Errorf("sigv4: status %d body %s", resp.StatusCode, got)
	}
	if strings.Contains(string(got), "FAKESIGKEY") {
		t.Error("the signature credential was echoed")
	}
	if up.count() != 0 {
		t.Error("a SigV4 request reached the provider")
	}

	resp, _ = do(t, "POST", path, map[string]string{"Authorization": "Bearer FAKE-BEDROCK-KEY", "Content-Type": "application/json"}, body)
	if resp.StatusCode != 200 {
		t.Fatalf("bearer key: status %d", resp.StatusCode)
	}
	row := g.rec.wait(t)
	if row.Provider != "bedrock" || row.OutputTokens != 20 {
		t.Errorf("row %+v", row)
	}
	if row.CostUSD != nil {
		t.Error("bedrock has no list price on file; cost must stay unknown")
	}
}

func TestClaudeLoginIsRefusedOnEveryRoute(t *testing.T) {
	var up seen
	g := routeRig(t, jsonUpstream(t, &up, "anthropic_body.json"), func(c *config.Config, u string) {
		c.Upstreams.Vertex, c.Upstreams.Foundry, c.Upstreams.Bedrock = u, u+"/anthropic", u
		c.Upstreams.Compat = []config.CompatUpstream{{Name: "vllm", BaseURL: u + "/v1"}}
	})
	login := map[string]string{"Authorization": "Bearer sk-ant-oat01-FAKELOGIN", "anthropic-beta": "oauth-2025-04-20", "Content-Type": "application/json"}
	for _, p := range []string{"/anthropic/v1/messages", "/vertex/v1/x:rawPredict", "/foundry/v1/messages", "/bedrock/model/m/invoke", "/compat/vllm/chat/completions"} {
		resp, got := do(t, "POST", g.gw.URL+p, login, []byte(`{}`))
		if resp.StatusCode != 401 {
			t.Errorf("%s: status %d", p, resp.StatusCode)
		}
		for _, want := range []string{"API-key authentication only", "Trove plugin"} {
			if !strings.Contains(string(got), want) {
				t.Errorf("%s: message lacks %q: %s", p, want, got)
			}
		}
		if strings.Contains(string(got), "FAKELOGIN") {
			t.Errorf("%s: the token was echoed", p)
		}
	}
	if up.count() != 0 {
		t.Error("a claude.ai login reached a provider")
	}
	if !strings.Contains(auth.Message, "Enterprise") {
		t.Error("message should name Enterprise")
	}
	if strings.Contains(g.logs.String(), "FAKELOGIN") {
		t.Error("the token reached the logs")
	}
}

func TestCompatRouteWithCustomRates(t *testing.T) {
	var up seen
	g := routeRig(t, jsonUpstream(t, &up, "chat_body.json"), func(c *config.Config, u string) {
		c.Upstreams.Compat = []config.CompatUpstream{{
			Name: "vllm", BaseURL: u + "/v1", Auth: "none",
			InputPerMTok: "1.00", OutputPerMTok: "2.00", CacheReadPerMTok: "0.10",
		}}
	})
	// Auth "none": an open server needs no key.
	resp, _ := do(t, "POST", g.gw.URL+"/compat/vllm/chat/completions", map[string]string{"Content-Type": "application/json"},
		[]byte(`{"model":"gpt-x","messages":[{"role":"user","content":"hi"}]}`))
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	row := g.rec.wait(t)
	if row.Provider != "compat" || row.Upstream != "vllm" {
		t.Errorf("provider %q upstream %q", row.Provider, row.Upstream)
	}
	// 50 prompt tokens of which 10 cached: 40 uncached, 10 cache read, 9 out.
	if row.InputUncachedTokens != 40 || row.CacheReadTokens != 10 || row.OutputTokens != 9 {
		t.Errorf("tokens %d/%d/%d", row.InputUncachedTokens, row.CacheReadTokens, row.OutputTokens)
	}
	// 40*1.00 + 10*0.10 + 9*2.00 = 59 dollars per million tokens, so 0.000059.
	if row.CostUSD == nil || math.Abs(*row.CostUSD-0.000059) > 1e-12 {
		t.Errorf("cost %v", row.CostUSD)
	}
	if !strings.HasPrefix(row.PricingVersion, "custom:") {
		t.Errorf("pricing version %q should say the rates are the user's own", row.PricingVersion)
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	if up.path != "/v1/chat/completions" {
		t.Errorf("upstream path %q", up.path)
	}
}

func TestCompatRouteWithoutRatesLeavesCostUnknown(t *testing.T) {
	var up seen
	g := routeRig(t, jsonUpstream(t, &up, "chat_body.json"), func(c *config.Config, u string) {
		c.Upstreams.Compat = []config.CompatUpstream{{Name: "deepseek", BaseURL: u}}
	})
	resp, _ := do(t, "POST", g.gw.URL+"/compat/deepseek/v1/chat/completions",
		map[string]string{"Authorization": "Bearer FAKE-OPEN-WEIGHT-KEY", "Content-Type": "application/json"}, []byte(`{"model":"m"}`))
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	row := g.rec.wait(t)
	if row.CostUSD != nil || row.OutputTokens != 9 {
		t.Errorf("row %+v", row)
	}
	// With auth = bearer a request without a key is refused.
	resp, _ = do(t, "POST", g.gw.URL+"/compat/deepseek/v1/chat/completions", map[string]string{"Content-Type": "application/json"}, []byte(`{}`))
	if resp.StatusCode != 401 {
		t.Errorf("no key: status %d", resp.StatusCode)
	}
}

func TestInvalidRouteConfigIsRefused(t *testing.T) {
	cfg := config.Default()
	cfg.Upstreams.Compat = []config.CompatUpstream{{Name: "a", BaseURL: "http://gpu.example.com:8000"}}
	if _, err := New(Options{Config: cfg, Recorder: newMemRecorder()}); err == nil || !strings.Contains(err.Error(), "https://") {
		t.Errorf("public http upstream accepted: %v", err)
	}
	cfg.Upstreams.Compat = []config.CompatUpstream{{Name: "a", BaseURL: "http://10.0.0.5:8000"}, {Name: "a", BaseURL: "http://10.0.0.6:8000"}}
	if _, err := New(Options{Config: cfg, Recorder: newMemRecorder()}); err == nil || !strings.Contains(err.Error(), "unique") {
		t.Errorf("duplicate names accepted: %v", err)
	}
}

func TestKindIsOnlyWhatTheUserDeclared(t *testing.T) {
	for in, want := range map[string]string{
		"": store.KindUnknown, "automated": store.KindAutomated, "Interactive": store.KindInteractive, "ci": store.KindUnknown,
	} {
		h := http.Header{}
		if in != "" {
			h.Set("X-Trove-Kind", in)
		}
		if got := kindOf(h); got != want {
			t.Errorf("kindOf(%q) = %q, want %q", in, got, want)
		}
	}
	// No User-Agent ever implies a kind.
	h := http.Header{"User-Agent": {"claude-cli/2.1.300 (external, sdk-cli)"}}
	if kindOf(h) != store.KindUnknown {
		t.Error("kind was guessed from the User-Agent")
	}
}

// bedrockFrames rebuilds the SSE fixture as an AWS event stream, the way Bedrock sends it.
func bedrockFrames(t *testing.T) []byte {
	t.Helper()
	str := func(name, val string) []byte {
		b := append([]byte{byte(len(name))}, name...)
		b = append(b, 7, byte(len(val)>>8), byte(len(val)))
		return append(b, val...)
	}
	var out []byte
	for _, line := range strings.Split(string(fixture(t, "anthropic_stream.sse")), "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		payload := []byte(`{"bytes":"` + base64.StdEncoding.EncodeToString([]byte(data)) + `"}`)
		h := append(str(":event-type", "chunk"), str(":message-type", "event")...)
		total := 12 + len(h) + len(payload) + 4
		f := binary.BigEndian.AppendUint32(nil, uint32(total))
		f = binary.BigEndian.AppendUint32(f, uint32(len(h)))
		f = binary.BigEndian.AppendUint32(f, crc32.ChecksumIEEE(f))
		f = append(append(f, h...), payload...)
		out = append(out, binary.BigEndian.AppendUint32(f, crc32.ChecksumIEEE(f))...)
	}
	return out
}

func TestBedrockStreamedUsageIsRecordedAndBytesPassThrough(t *testing.T) {
	frames := bedrockFrames(t)
	var up seen
	g := routeRig(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		up.mu.Lock()
		up.calls++
		up.mu.Unlock()
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		_, _ = w.Write(frames)
	}), func(c *config.Config, u string) { c.Upstreams.Bedrock = u })
	resp, got := do(t, "POST", g.gw.URL+"/bedrock/model/us.anthropic.claude-opus-5-5/invoke-with-response-stream",
		map[string]string{"Authorization": "Bearer FAKE-BEDROCK-KEY", "Content-Type": "application/json"}, []byte(`{"anthropic_version":"bedrock-2023-05-31"}`))
	if resp.StatusCode != 200 || !bytes.Equal(got, frames) {
		t.Fatalf("status %d; the stream was altered (%d bytes, want %d)", resp.StatusCode, len(got), len(frames))
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/vnd.amazon.eventstream" {
		t.Errorf("content type rewritten to %q", ct)
	}
	row := g.rec.wait(t)
	if !row.Streamed || row.TTFTMS == nil {
		t.Errorf("streamed %v ttft %v", row.Streamed, row.TTFTMS)
	}
	if row.OutputTokens != 510 || row.CacheReadTokens != 1500 || row.ErrorClass != "" {
		t.Errorf("row %+v", row)
	}
}
