package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/store"
)

// writeHistory puts synthetic Claude Code and Codex history in the harness's
// fake home. Nothing here is a real log.
func writeHistory(t *testing.T, h *harness, claudeReqs, codexReqs int) {
	t.Helper()
	home, _ := h.app.HomeDir()
	var b strings.Builder
	base := time.Now().Add(-30 * time.Minute)
	for i := 0; i < claudeReqs; i++ {
		ts := base.Add(time.Duration(i) * time.Second).UTC().Format("2006-01-02T15:04:05.000Z")
		fmt.Fprintf(&b, `{"type":"assistant","sessionId":"sess-%d","requestId":"req_%d","timestamp":%q,"version":"2.1.302","message":{"id":"m","model":"claude-sonnet-5-5","content":[{"type":"text","text":"SECRET-ANSWER"}],"usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":5000,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0}}}}`+"\n", i%3, i, ts)
	}
	p := filepath.Join(home, ".claude", "projects", "C--proj")
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "sess.jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if codexReqs == 0 {
		return
	}
	b.Reset()
	fmt.Fprintf(&b, `{"timestamp":%q,"type":"session_meta","payload":{"id":"cx","cli_version":"0.99.0"}}`+"\n", base.UTC().Format(time.RFC3339Nano))
	b.WriteString(`{"timestamp":"` + base.UTC().Format(time.RFC3339Nano) + `","type":"turn_context","payload":{"model":"gpt-5"}}` + "\n")
	for i := 1; i <= codexReqs; i++ {
		ts := base.Add(time.Duration(i) * time.Second).UTC().Format(time.RFC3339Nano)
		fmt.Fprintf(&b, `{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":%d},"last_token_usage":{"input_tokens":1000,"cached_input_tokens":800,"output_tokens":50,"total_tokens":1050}}}}`+"\n", ts, i*1050)
	}
	cp := filepath.Join(home, ".codex", "sessions", "2026", "10", "20")
	_ = os.MkdirAll(cp, 0o755)
	if err := os.WriteFile(filepath.Join(cp, "rollout-a.jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBareTroveReportsFromHistoryWithoutTheGateway(t *testing.T) {
	h := newHarness(t, map[string]string{"ANTHROPIC_API_KEY": "sk-ant-must-never-appear"})
	writeHistory(t, h, 25, 5)
	if code := h.run("--json"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	var r struct {
		State    string `json:"state"`
		Requests int    `json:"requests"`
		History  *struct {
			Requests int    `json:"requests"`
			Plan     string `json:"plan"`
		} `json:"history"`
		Headline struct {
			Spend struct{ Label string } `json:"spend_usd"`
		} `json:"headline"`
	}
	if err := json.Unmarshal(h.out.Bytes(), &r); err != nil {
		t.Fatalf("%v: %s", err, h.out)
	}
	if r.State != "ok" || r.Requests != 30 || r.History == nil || r.History.Requests != 30 {
		t.Fatalf("want 30 requests from history: %+v\n%s", r, h.out)
	}
	if r.Headline.Spend.Label != "estimated" {
		t.Errorf("history spend must be labelled estimated: %+v", r.Headline)
	}
	for _, leaked := range []string{"sk-ant-must-never-appear", "SECRET-ANSWER"} {
		if strings.Contains(h.out.String(), leaked) || strings.Contains(h.err.String(), leaked) {
			t.Errorf("output contains %q", leaked)
		}
	}
	if h.err.Len() != 0 {
		t.Errorf("--json must keep stderr quiet: %q", h.err)
	}
}

func TestFirstRunSaysItIsReadingHistory(t *testing.T) {
	h := newHarness(t, nil)
	writeHistory(t, h, 25, 0)
	if code := h.run("--no-color"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if !strings.Contains(h.err.String(), "first run only") || !strings.Contains(h.out.String(), "From history") {
		t.Errorf("stderr %q\nstdout %s", h.err, h.out)
	}
	h.err.Reset()
	if code := h.run("--no-color"); code != 0 || strings.Contains(h.err.String(), "first run only") {
		t.Errorf("second run should not repeat the notice: %q", h.err)
	}
}

func TestNoHistoryFlagReadsNothingAndCreatesNothing(t *testing.T) {
	h := newHarness(t, nil)
	writeHistory(t, h, 25, 0)
	if code := h.run("--no-history", "--json"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if _, err := os.Stat(filepath.Join(h.app.Getenv("TROVE_DATA_DIR"), store.DBFileName)); err == nil {
		t.Error("--no-history still created the database")
	}
}

func TestSecondRunIsIncrementalAndStatusExplainsLimits(t *testing.T) {
	h := newHarness(t, nil)
	writeHistory(t, h, 25, 0)
	if code := h.run("logs", "scan", "--json"); code != 0 {
		t.Fatalf("scan: exit %d: %s", code, h.err)
	}
	h.out.Reset()
	if code := h.run("logs", "scan", "--json"); code != 0 {
		t.Fatalf("rescan: exit %d: %s", code, h.err)
	}
	var s struct {
		Scan struct {
			Unchanged int `json:"files_unchanged"`
			Read      int `json:"files_read"`
		} `json:"scan"`
	}
	if err := json.Unmarshal(h.out.Bytes(), &s); err != nil || s.Scan.Unchanged != 1 || s.Scan.Read != 0 {
		t.Errorf("second scan should not reopen the file: %+v (%v) %s", s, err, h.out)
	}
	h.out.Reset()
	if code := h.run("logs", "status"); code != 0 {
		t.Fatalf("status: exit %d: %s", code, h.err)
	}
	for _, want := range []string{"25 requests", "Not available from history", "cache_miss", "never from credentials"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("status lacks %q:\n%s", want, h.out)
		}
	}
}

func TestHistoryPlanOverrideInConfig(t *testing.T) {
	h := newHarness(t, nil)
	writeHistory(t, h, 25, 0)
	dir := h.app.Getenv("TROVE_DATA_DIR")
	_ = os.MkdirAll(dir, 0o700)
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[history]\nenabled = true\nplan = \"subscription\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := h.run("--json"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	var r struct {
		Headline struct {
			API *struct{ Value float64 } `json:"api_equivalent_usd"`
		} `json:"headline"`
		History struct{ Plan string } `json:"history"`
	}
	if err := json.Unmarshal(h.out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.History.Plan != "subscription" || r.Headline.API == nil || r.Headline.API.Value <= 0 {
		t.Errorf("subscription plan should show API-equivalent value, not spend: %+v", r)
	}
}
