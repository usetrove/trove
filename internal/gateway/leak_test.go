package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/analyze"
	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/redact"
	"github.com/usetrove/trove/internal/store"
)

// This is the key-leakage test CI runs on every change. It drives every request
// path of a gateway wired to the real store with planted credentials and prompt
// text, then looks for them in everything Trove could have emitted: the logs,
// every response the client saw, and every byte of every file in the data
// folder (including the SQLite file and its write-ahead log).
//
// Credentials must appear nowhere. Prompt text may pass through to the provider
// but must never be written to disk (CLAUDE.md rules 2 and 7).
const (
	leakAnthropicKey = "sk-ant-api03-LEAKTESTANTHROPICKEY0123456789abcdef"
	leakOpenAIKey    = "sk-proj-LEAKTESTOPENAIKEY0123456789abcdefghij"
	leakOAuthToken   = "LEAKTESTOAUTHBEARERTOKEN0123456789"
	leakJWTSecret    = "LEAKTESTJWTPAYLOAD0123456789"
	leakPAT          = "pat_LEAKTESTCODEXPERSONALACCESSTOKEN0123456789"
	leakAccountID    = "LEAKTESTCHATGPTACCOUNTID0123456789"
	leakPrompt       = "LEAKTESTPROMPT-please-refactor-/home/alice/secret-project/payroll.go"
)

// leakChatGPTLogin is shaped like what Codex sends after "Sign in with ChatGPT": a JWT
// whose payload carries OpenAI's namespaced claims. The values are made up.
var leakChatGPTLogin = "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(
	[]byte(`{"iss":"https://auth.openai.com","https://api.openai.com/auth":{"chatgpt_user_id":"LEAKTESTCHATGPTUSER0123456789"}}`)) + ".LEAKTESTCHATGPTSIGNATURE0123456789"

var leakCredentials = []string{
	leakAnthropicKey, leakOpenAIKey, leakOAuthToken, leakJWTSecret, leakChatGPTLogin, leakPAT, leakAccountID,
	"LEAKTESTCHATGPTUSER", "LEAKTESTCHATGPTSIGNATURE", "LEAKTESTCODEXPERSONAL",
	"LEAKTESTANTHROPICKEY", "LEAKTESTOPENAIKEY", // a truncated copy must not survive either
}

func TestNoCredentialOrPromptReachesLogsResponsesOrDisk(t *testing.T) {
	defer func(old int64) { maxRequestBody = old }(maxRequestBody)
	maxRequestBody = 1 << 20

	dataDir := filepath.Join(t.TempDir(), "data")
	db, err := store.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	writer := store.NewWriter(db, store.WriterOptions{})

	var calls int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.Copy(io.Discard, r.Body)
		switch {
		case strings.Contains(r.URL.Path, "boom"):
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"api_error","message":"upstream exploded"}}`))
		case strings.HasPrefix(r.URL.Path, "/v1/chat") || strings.HasPrefix(r.URL.Path, "/v1/responses"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(fixture(t, "chat_body.json"))
		case strings.HasSuffix(r.URL.Path, "/stream"):
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write(fixture(t, "anthropic_stream.sse"))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(fixture(t, "anthropic_body.json"))
		}
	}))
	defer up.Close()

	cfg := config.Default()
	cfg.Upstreams.Anthropic, cfg.Upstreams.OpenAI = up.URL, up.URL
	logs := &syncBuffer{}
	tbl, err := pricing.Default()
	if err != nil {
		t.Fatal(err)
	}
	log := redact.NewLogger(logs, true) // debug level: the noisiest the logs ever get
	srv, err := New(Options{Config: cfg, Recorder: writer, Pricing: tbl,
		Analyzer: analyze.New(analyze.Options{Pricing: tbl, Logger: log}), Logger: log, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(srv.Handler())
	defer gw.Close()

	body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":10,"messages":[{"role":"user","content":"` + leakPrompt + `"}]}`)
	anth := map[string]string{"x-api-key": leakAnthropicKey, "anthropic-version": "2023-06-01", "Content-Type": "application/json", "User-Agent": "claude-cli/2.1.300 (external, cli)"}
	oai := map[string]string{"Authorization": "Bearer " + leakOpenAIKey, "Content-Type": "application/json"}

	var seen [][]byte // every response body the client received
	send := func(name, method, path string, hdr map[string]string, b []byte, wantStatus int) {
		t.Helper()
		resp, got := do(t, method, gw.URL+path, hdr, b)
		seen = append(seen, got)
		if wantStatus != 0 && resp.StatusCode != wantStatus {
			t.Errorf("%s: status %d, want %d (%s)", name, resp.StatusCode, wantStatus, got)
		}
		for k, v := range resp.Header {
			seen = append(seen, []byte(k+": "+strings.Join(v, ",")))
		}
	}

	send("anthropic ok", "POST", "/anthropic/v1/messages", anth, body, 200)
	send("anthropic stream", "POST", "/anthropic/v1/messages/stream", anth, body, 200)
	send("anthropic upstream error", "POST", "/anthropic/v1/boom", anth, body, 500)
	send("openai ok", "POST", "/openai/v1/responses", oai, body, 200)
	send("openai error", "POST", "/openai/v1/boom", oai, body, 500)

	forwarded := calls
	// Refused: nothing may be forwarded, and the tokens must not be echoed.
	send("oauth bearer only", "POST", "/anthropic/v1/messages",
		map[string]string{"Authorization": "Bearer " + leakOAuthToken, "anthropic-beta": "oauth-2025-04-20", "Content-Type": "application/json"}, body, 401)
	send("setup-token in x-api-key", "POST", "/anthropic/v1/messages",
		map[string]string{"x-api-key": "sk-ant-oat01-" + leakOAuthToken, "Content-Type": "application/json"}, body, 401)
	send("openai jwt session token", "POST", "/openai/v1/responses",
		map[string]string{"Authorization": "Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiI" + leakJWTSecret + "In0.c2lnbmF0dXJl", "Content-Type": "application/json"}, body, 401)
	// Codex logins other than an API key: refused, never forwarded, never echoed, logged or stored.
	send("chatgpt login token", "POST", "/openai/v1/responses",
		map[string]string{"Authorization": "Bearer " + leakChatGPTLogin, "chatgpt-account-id": leakAccountID, "originator": "codex_cli_rs", "Content-Type": "application/json"}, body, 401)
	send("chatgpt login token, no account header", "POST", "/openai/v1/responses",
		map[string]string{"Authorization": "Bearer " + leakChatGPTLogin, "Content-Type": "application/json"}, body, 401)
	send("codex personal access token", "POST", "/openai/v1/responses",
		map[string]string{"Authorization": "Bearer " + leakPAT, "Content-Type": "application/json"}, body, 401)
	send("unknown credential type with a key-like name", "POST", "/openai/v1/responses",
		map[string]string{"Authorization": "Bearer " + leakPAT, "chatgpt-account-id": leakAccountID, "Content-Type": "application/json"}, body, 401)
	if calls != forwarded {
		t.Errorf("a refused request reached the provider (%d calls, want %d)", calls, forwarded)
	}

	// The provider cannot be reached: the error text is built from the transport error.
	up2 := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	cfgDown := config.Default()
	cfgDown.Upstreams.Anthropic, cfgDown.Upstreams.OpenAI = up2.URL, up2.URL
	up2.Close()
	srvDown, err := New(Options{Config: cfgDown, Recorder: writer, Pricing: tbl, Logger: log, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	gwDown := httptest.NewServer(srvDown.Handler())
	defer gwDown.Close()
	resp, got := do(t, "POST", gwDown.URL+"/anthropic/v1/messages", anth, body)
	seen = append(seen, got)
	if resp.StatusCode != 502 {
		t.Errorf("unreachable upstream: status %d", resp.StatusCode)
	}

	// A body over the limit is refused with a plain message. The limit is lowered
	// for this test so it does not stream 256 MiB (slow under the race detector).
	{
		big := io.MultiReader(strings.NewReader(`{"p":"`+leakPrompt), &zeroReader{n: int(maxRequestBody) + 1024})
		req, _ := http.NewRequest("POST", gw.URL+"/anthropic/v1/messages", big)
		for k, v := range anth {
			req.Header.Set(k, v)
		}
		if resp, err := http.DefaultClient.Do(req); err == nil {
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			seen = append(seen, b)
			if resp.StatusCode != http.StatusRequestEntityTooLarge {
				t.Errorf("oversize body: status %d, want 413", resp.StatusCode)
			}
		} else if !strings.Contains(err.Error(), "reset") && !strings.Contains(err.Error(), "EOF") && !strings.Contains(err.Error(), "broken pipe") && !strings.Contains(err.Error(), "aborted") {
			t.Errorf("oversize body: %v", err)
		}
	}

	// Let the recorder flush, then close everything so SQLite checkpoints its log.
	time.Sleep(300 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := writer.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	scan := func(where string, data []byte, secrets []string) {
		for _, s := range secrets {
			if bytes.Contains(data, []byte(s)) {
				t.Errorf("%q found in %s", s, where)
			}
		}
	}
	scan("the logs", logs.Bytes(), append(leakCredentials, leakPrompt))
	for i, b := range seen {
		scan("a response to the client (#"+itoa(i)+")", b, leakCredentials)
	}
	files := 0
	_ = filepath.WalkDir(dataDir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		files++
		b, _ := os.ReadFile(p)
		rel, _ := filepath.Rel(dataDir, p)
		scan("data file "+rel, b, append(leakCredentials, leakPrompt, "alice", "payroll.go", "secret-project"))
		return nil
	})
	if files == 0 {
		t.Fatal("no data files were written, so the scan proved nothing")
	}

	// And the rows exist: the scan above would also pass if nothing was recorded.
	db2, err := store.OpenReadOnly(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db2.Close() }()
	var n int
	if err := db2.QueryRow(`SELECT COUNT(*) FROM requests`).Scan(&n); err != nil || n < 4 {
		t.Errorf("only %d request rows were recorded (%v); the on-disk scan is not meaningful", n, err)
	}
}

// zeroReader yields n zero bytes, so an oversize body can be streamed without
// holding it in memory.
type zeroReader struct{ n int }

func (z *zeroReader) Read(p []byte) (int, error) {
	if z.n <= 0 {
		return 0, io.EOF
	}
	k := min(len(p), z.n)
	clear(p[:k])
	z.n -= k
	return k, nil
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{byte('0' + i%10)}, b...)
	}
	return string(b)
}
