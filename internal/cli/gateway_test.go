package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/store"
)

const claudeSettings = "{\n  \"model\": \"opus\"\n}\n"

// agentHome lays out fake Claude Code and Codex installs under the harness home.
func agentHome(t *testing.T, h *harness) (claudeSettingsPath, codexConfigPath string) {
	t.Helper()
	home, _ := h.app.HomeDir()
	claudeSettingsPath = filepath.Join(home, ".claude", "settings.json")
	codexConfigPath = filepath.Join(home, ".codex", "config.toml")
	for p, body := range map[string]string{claudeSettingsPath: claudeSettings, codexConfigPath: "model = \"gpt-5\"\n"} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return
}

func TestSetupNeedsConsentThenUndoRestoresEverything(t *testing.T) {
	data := t.TempDir()
	h := newHarness(t, map[string]string{"TROVE_DATA_DIR": data}, "claude", "codex")
	cPath, xPath := agentHome(t, h)
	before := map[string]string{cPath: claudeSettings, xPath: "model = \"gpt-5\"\n"}

	// No terminal and no --yes: show the plan, change nothing.
	if code := h.run("setup", "--gateway"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	for _, want := range []string{"ANTHROPIC_BASE_URL", "model_providers.trove", "No changes made"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("plan output lacks %q:\n%s", want, h.out)
		}
	}
	for p, body := range before {
		if got, _ := os.ReadFile(p); string(got) != body {
			t.Fatalf("%s changed without consent", p)
		}
	}

	// An interactive "n" also changes nothing; "y" applies.
	h = newHarness(t, map[string]string{"TROVE_DATA_DIR": data}, "claude", "codex")
	h.app.HomeDir = func() (string, error) { return filepath.Dir(filepath.Dir(cPath)), nil }
	h.app.In = strings.NewReader("n\n")
	h.run("setup", "--gateway")
	if got, _ := os.ReadFile(cPath); string(got) != claudeSettings {
		t.Fatal("answering n must not change anything")
	}
	h = newHarness(t, map[string]string{"TROVE_DATA_DIR": data}, "claude", "codex")
	h.app.HomeDir = func() (string, error) { return filepath.Dir(filepath.Dir(cPath)), nil }
	h.app.In = strings.NewReader("y\n")
	if code := h.run("setup", "--gateway"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if got, _ := os.ReadFile(cPath); !strings.Contains(string(got), "http://127.0.0.1:8787/anthropic") {
		t.Errorf("claude settings not updated: %s", got)
	}
	if got, _ := os.ReadFile(xPath); !strings.Contains(string(got), "supports_websockets = false") {
		t.Errorf("codex config not updated: %s", got)
	}
	for _, want := range []string{"trove undo", "/trove/health", "trove gateway status"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("verify/turn-off guidance lacks %q", want)
		}
	}
	if _, err := os.Stat(filepath.Join(data, "config.toml")); err != nil {
		t.Error("setup should write the default config file")
	}

	h = newHarness(t, map[string]string{"TROVE_DATA_DIR": data}, "claude", "codex")
	if code := h.run("undo", "--json"); code != 0 {
		t.Fatalf("undo exit %d: %s", code, h.err)
	}
	for p, body := range before {
		if got, _ := os.ReadFile(p); string(got) != body {
			t.Errorf("%s not restored exactly:\n%s", p, got)
		}
	}
	h = newHarness(t, map[string]string{"TROVE_DATA_DIR": data}, "claude", "codex")
	h.run("undo")
	if !strings.Contains(h.out.String(), "Nothing to undo") {
		t.Errorf("second undo: %s", h.out)
	}
}

func TestSetupJSONWithoutYesAppliesNothing(t *testing.T) {
	data := t.TempDir()
	h := newHarness(t, map[string]string{"TROVE_DATA_DIR": data}, "claude")
	cPath, _ := agentHome(t, h)
	h.app.In = strings.NewReader("y\n") // even a waiting "y" is ignored in --json mode
	if code := h.run("setup", "--gateway", "--json"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	var rep setupReport
	if err := json.Unmarshal(h.out.Bytes(), &rep); err != nil || rep.Applied || len(rep.Plans) == 0 {
		t.Errorf("%v %+v", err, rep)
	}
	if got, _ := os.ReadFile(cPath); string(got) != claudeSettings {
		t.Error("--json without --yes must not apply")
	}
}

func TestGatewayRefusesNonLoopbackListen(t *testing.T) {
	h := newHarness(t, map[string]string{"TROVE_DATA_DIR": t.TempDir()})
	if code := h.run("gateway", "start", "--listen", "0.0.0.0:8787"); code != 1 || !strings.Contains(h.err.String(), "only binds to loopback") {
		t.Errorf("exit %d: %s", code, h.err)
	}
}

func TestGatewayStatusAndStopWhenNotRunning(t *testing.T) {
	h := newHarness(t, map[string]string{"TROVE_DATA_DIR": t.TempDir()})
	if code := h.run("gateway", "status", "--json"); code != 0 {
		t.Fatal(h.err.String())
	}
	var s statusReport
	if json.Unmarshal(h.out.Bytes(), &s) != nil || s.Running {
		t.Errorf("status: %s", h.out)
	}
	h = newHarness(t, map[string]string{"TROVE_DATA_DIR": t.TempDir()})
	if code := h.run("gateway", "stop"); code != 0 || !strings.Contains(h.out.String(), "not running") {
		t.Errorf("stop: %d %s", code, h.out)
	}
}

// TestGatewayEndToEnd runs the real command against a fake provider and checks
// the usage row that lands in SQLite equals what the provider reported.
func TestGatewayEndToEnd(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_X","type":"message","model":"claude-opus-5-5","content":[],"usage":{"input_tokens":11,"cache_read_input_tokens":22,"output_tokens":33}}`))
	}))
	defer up.Close()
	data := t.TempDir()
	h := newHarness(t, map[string]string{"TROVE_DATA_DIR": data, "TROVE_UPSTREAMS_ANTHROPIC": up.URL})
	ctx, cancel := context.WithCancel(context.Background())
	h.app.Ctx = ctx
	h.out, h.err = &bytes.Buffer{}, &bytes.Buffer{}
	h.app.Out, h.app.Err = h.out, h.err
	done := make(chan int, 1)
	go func() { done <- h.run("gateway", "start", "--listen", "127.0.0.1:0") }()

	var addr string
	for i := 0; i < 100 && addr == ""; i++ {
		if p, ok := readPID(data); ok {
			if _, up := probe(p.Addr); up {
				addr = p.Addr
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if addr == "" {
		cancel()
		t.Fatal("gateway did not come up")
	}
	req, _ := http.NewRequest("POST", "http://"+addr+"/anthropic/v1/messages", strings.NewReader(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("x-api-key", "sk-ant-api03-FAKEKEYDONOTLEAK0123456789abcdefgh")
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		cancel()
		t.Fatalf("request failed: %v", err)
	}
	_ = resp.Body.Close()

	sh := newHarness(t, map[string]string{"TROVE_DATA_DIR": data})
	sh.run("gateway", "status")
	if !strings.Contains(sh.out.String(), "is running at") {
		t.Errorf("status while running: %s", sh.out)
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("gateway exit %d: %s", code, h.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("gateway did not shut down")
	}
	if _, ok := readPID(data); ok {
		t.Error("the pid record should be removed on exit")
	}
	if strings.Contains(h.err.String(), "sk-ant-api03-FAKEKEYDONOTLEAK0123456789abcdefgh") || strings.Contains(h.out.String(), "sk-ant-api03-FAKEKEYDONOTLEAK0123456789abcdefgh") {
		t.Error("a credential reached the command output")
	}

	db, err := store.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var in, cr, out int64
	var model, agent string
	if err := db.QueryRow(`SELECT input_uncached_tokens, cache_read_tokens, output_tokens, model, provider FROM requests`).Scan(&in, &cr, &out, &model, &agent); err != nil {
		t.Fatalf("no usage row saved: %v", err)
	}
	if in != 11 || cr != 22 || out != 33 || model != "claude-opus-5-5" || agent != "anthropic" {
		t.Errorf("row: %d %d %d %s %s", in, cr, out, model, agent)
	}
}
