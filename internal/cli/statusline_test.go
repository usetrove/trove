package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/gateway/session"
	"github.com/usetrove/trove/internal/store"
)

func TestStatusLineWithNoDataIsShortAndSucceeds(t *testing.T) {
	for _, format := range []string{"shell", "tmux", "claude"} {
		h := newHarness(t, nil)
		if code := h.run("status-line", "--format", format); code != 0 {
			t.Fatalf("%s: exit %d: %s", format, code, h.err)
		}
		if got := strings.TrimSpace(h.out.String()); got != "Trove: no usage yet" {
			t.Errorf("%s: %q", format, got)
		}
	}
	h := newHarness(t, nil)
	if code := h.run("status-line", "--format", "html"); code != 1 {
		t.Errorf("a bad format should fail, exit %d", code)
	}
}

// A broken setup must never break the agent's status bar: print a hint, exit 0.
func TestStatusLineSurvivesABrokenConfig(t *testing.T) {
	h := newHarness(t, map[string]string{"TROVE_GATEWAY_PORT": "banana"})
	if code := h.run("status-line"); code != 0 || !strings.Contains(h.out.String(), "trove doctor") {
		t.Errorf("exit %d, out %q", code, h.out)
	}
}

func seedForStatus(t *testing.T, dir string, rows int) {
	t.Helper()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	key := session.ClaudeCodeKey("sess-abc")
	for i := 0; i < rows; i++ {
		ts := now.Add(-time.Duration(i) * time.Minute).UnixMilli()
		if _, err := tx.Exec(`INSERT INTO requests(ts, session_key, agent, model, status_code, input_uncached_tokens, cost_usd)
			VALUES(?,?,?,?,200,100,0.01)`, ts, fmt.Sprintf("s%d", i%500), "claude_code", "m"); err != nil {
			t.Fatal(err)
		}
	}
	// The per-day summary the gateway keeps; the status line reads only this.
	for d := 0; d < 31; d++ {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO usage_daily(day, requests, unpriced, spend_usd, saved_usd) VALUES(?,?,?,?,?)`,
			store.DayKey(now.AddDate(0, 0, -d)), 100, 0, 1.0, 0.1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO sessions(session_key, agent, started_at, ended_at, request_count, total_cost_usd) VALUES(?,?,?,?,4,3.2)`,
		key, "claude_code", now.Add(-time.Hour).UnixMilli(), now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestStatusLineReadsClaudeCodeInputAndIsFast(t *testing.T) {
	h := newHarness(t, nil)
	seedForStatus(t, h.app.Getenv("TROVE_DATA_DIR"), 50000)
	if code := h.run("budget", "set", "1500", "--period", "month"); code != 0 {
		t.Fatalf("budget: exit %d: %s", code, h.err)
	}

	run := func() (string, time.Duration) {
		h.out.Reset()
		h.app.In = strings.NewReader(`{"session_id":"sess-abc","model":{"display_name":"Opus"}}`)
		start := time.Now()
		code := h.run("status-line", "--format", "claude")
		d := time.Since(start)
		if code != 0 {
			t.Fatalf("exit %d: %s", code, h.err)
		}
		return strings.TrimSpace(h.out.String()), d
	}
	line, _ := run()
	for _, want := range []string{"/ $1,500", "% used", "of month", "session ~$3.20", "saved ~$"} {
		if !strings.Contains(line, want) {
			t.Errorf("status line lacks %q: %q", want, line)
		}
	}
	if strings.ContainsAny(line, "\n\r") {
		t.Errorf("status line must be one line: %q", line)
	}

	// Timing: 50,000 recorded requests, 50 runs; the 95th percentile must stay
	// under the 50 ms budget (in-process, so it excludes process start-up).
	var ds []time.Duration
	for i := 0; i < 50; i++ {
		_, d := run()
		ds = append(ds, d)
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	p95 := ds[len(ds)*95/100]
	t.Logf("status-line over 50,000 requests: median %v, p95 %v", ds[len(ds)/2], p95)
	if p95 > 50*time.Millisecond {
		t.Errorf("p95 is %v; the status line must finish in under 50 ms", p95)
	}
}

func TestStatusLineJSONAndNoColor(t *testing.T) {
	h := newHarness(t, map[string]string{"NO_COLOR": "1"})
	seedForStatus(t, h.app.Getenv("TROVE_DATA_DIR"), 10)
	h.run("budget", "set", "20", "--period", "month") // small cap: certainly near or over
	h.out.Reset()
	if code := h.run("status-line", "--format", "claude"); code != 0 || strings.Contains(h.out.String(), "\x1b[") {
		t.Errorf("NO_COLOR must remove color: exit %d %q", code, h.out)
	}
	h.out.Reset()
	if code := h.run("status-line", "--format", "claude", "--json"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	var v struct {
		Line   string `json:"line"`
		Status struct {
			Spend struct {
				Label string `json:"label"`
			} `json:"spend_usd"`
		} `json:"status"`
	}
	if err := json.Unmarshal(h.out.Bytes(), &v); err != nil || v.Line == "" || v.Status.Spend.Label != "estimated" {
		t.Errorf("json: %q (%v)", h.out, err)
	}
}
