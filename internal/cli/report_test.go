package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/store"
)

func TestReportOnAnEmptyDataDirExplainsAndCreatesNothing(t *testing.T) {
	for _, args := range [][]string{{}, {"report"}, {"report", "--week"}} {
		h := newHarness(t, nil)
		if code := h.run(args...); code != 0 {
			t.Fatalf("%v: exit %d: %s", args, code, h.err)
		}
		out := h.out.String()
		for _, want := range []string{"trove setup", "trove gateway"} {
			if !strings.Contains(out, want) {
				t.Errorf("%v: no mention of %q:\n%s", args, want, out)
			}
		}
		if _, err := os.Stat(filepath.Join(h.app.Getenv("TROVE_DATA_DIR"), store.DBFileName)); err == nil {
			t.Errorf("%v: a read-only command created the database", args)
		}

		h = newHarness(t, nil)
		h.run(append(append([]string{}, args...), "--json")...)
		var v struct {
			State string `json:"state"`
		}
		if err := json.Unmarshal(h.out.Bytes(), &v); err != nil || v.State != "empty" {
			t.Errorf("%v --json: %q (%v)", args, h.out, err)
		}
	}
}

func TestReportRejectsConflictingPeriods(t *testing.T) {
	h := newHarness(t, nil)
	if code := h.run("report", "--today", "--week"); code != 1 || !strings.Contains(h.err.String(), "choose one") {
		t.Errorf("exit %d, stderr %q", code, h.err)
	}
	h = newHarness(t, nil)
	if code := h.run("report", "--since", "yesterday"); code != 1 || !strings.Contains(h.err.String(), "2026-10-01") {
		t.Errorf("exit %d, stderr %q", code, h.err)
	}
}

func TestReportShowsRecordedUsageWithLabels(t *testing.T) {
	h := newHarness(t, nil)
	dir := h.app.Getenv("TROVE_DATA_DIR")
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		_, err := db.Exec(`INSERT INTO requests(ts, session_key, agent, provider, model, status_code, input_uncached_tokens, output_tokens, cost_usd)
			VALUES(?, 's', 'claude_code', 'anthropic', 'claude-a', 200, 1000, 100, 0.5)`, time.Now().UnixMilli())
		if err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()

	if code := h.run("report", "--today"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	out := h.out.String()
	for _, want := range []string{"claude_code", "claude-a", "(estimated", "(measured)", "Only 3 requests"} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
}

func TestBudgetSetShowAndRemove(t *testing.T) {
	h := newHarness(t, nil)
	if code := h.run("budget", "show"); code != 0 || !strings.Contains(h.out.String(), "No budget is set") {
		t.Fatalf("exit %d: %q %q", code, h.out, h.err)
	}

	h.out.Reset()
	if code := h.run("budget", "set", "$1,500", "--period", "month"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if out := h.out.String(); !strings.Contains(out, "Budget set to $1,500 per month.") || !strings.Contains(out, "of $1,500 used") {
		t.Errorf("unexpected output:\n%s", out)
	}
	cfg, err := os.ReadFile(filepath.Join(h.app.Getenv("TROVE_DATA_DIR"), "config.toml"))
	if err != nil || !strings.Contains(string(cfg), "cap_usd = 1500.0") || !strings.Contains(string(cfg), `period = "monthly"`) {
		t.Errorf("config not updated (%v):\n%s", err, cfg)
	}

	h.out.Reset()
	if code := h.run("budget", "--json"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	var v struct {
		Budget struct {
			CapUSD float64 `json:"cap_usd"`
			Spent  struct {
				Label string `json:"label"`
			} `json:"spent_usd"`
		} `json:"budget"`
	}
	if err := json.Unmarshal(h.out.Bytes(), &v); err != nil || v.Budget.CapUSD != 1500 || v.Budget.Spent.Label != "estimated" {
		t.Errorf("json: %q (%v)", h.out, err)
	}

	h.out.Reset()
	if code := h.run("budget", "set", "0"); code != 0 || !strings.Contains(h.out.String(), "Budget removed") {
		t.Errorf("exit %d: %q %q", code, h.out, h.err)
	}
}

func TestBudgetSetRejectsBadInput(t *testing.T) {
	for _, args := range [][]string{{"budget", "set", "lots"}, {"budget", "set", "-5"}, {"budget", "set", "10", "--period", "year"}} {
		h := newHarness(t, nil)
		if code := h.run(args...); code != 1 || !strings.HasPrefix(h.err.String(), "Error: ") {
			t.Errorf("%v: exit %d, stderr %q", args, code, h.err)
		}
	}
}
