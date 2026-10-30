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

func seedRequests(t *testing.T, dir string, rows ...[]any) {
	t.Helper()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO requests (ts, provider, model, status_code, input_uncached_tokens, output_tokens, cost_usd, pricing_version)
VALUES (?, ?, ?, ?, 1000, 100, ?, 'v1')`, r...); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReconcileMatchMismatchAndExport(t *testing.T) {
	h := newHarness(t, nil)
	dir := h.app.Getenv("TROVE_DATA_DIR")
	in := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC).UnixMilli()
	out := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC).UnixMilli()
	seedRequests(t, dir,
		[]any{in, "anthropic", "claude-a", 200, 60.0},
		[]any{in, "anthropic", "claude-a", 200, 40.0},
		[]any{in, "anthropic", "claude-a", 500, 99.0},  // failed request: not counted
		[]any{in, "openai", "gpt-x", 200, 7.0},         // other provider
		[]any{out, "anthropic", "claude-a", 200, 50.0}, // outside the period
		[]any{in, "anthropic", "claude-new", 200, nil}, // unpriced
	)
	base := []string{"reconcile", "--utc", "--provider", "anthropic", "--from", "2026-10-01", "--to", "2026-10-31"}

	if code := h.run(append(base, "--invoice", "101")...); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	for _, want := range []string{"$100.00", "$101.00", "match, within 2.0%", "1 unpriced", "lower bound"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, h.out)
		}
	}

	h.out.Reset()
	exp := filepath.Join(t.TempDir(), "r.json")
	if code := h.run(append(base, "--invoice", "130", "--json", "--export", exp)...); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	var res map[string]any
	if err := json.Unmarshal(h.out.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res["verdict"] != "mismatch" || res["recorded_usd"].(float64) != 100 {
		t.Errorf("unexpected result: %v", res)
	}
	b, err := os.ReadFile(exp)
	if err != nil || !strings.Contains(string(b), `"verdict": "mismatch"`) {
		t.Errorf("export missing or wrong: %v %s", err, b)
	}
}

func TestReconcileErrorsAndNoDatabase(t *testing.T) {
	h := newHarness(t, nil)
	cases := [][]string{
		{"reconcile", "--from", "2026-10-01", "--to", "2026-10-31"},                                           // no invoice
		{"reconcile", "--invoice", "5", "--from", "10/01", "--to", "2026-10-31"},                              // bad date
		{"reconcile", "--invoice", "5", "--from", "2026-10-31", "--to", "2026-10-01"},                         // reversed
		{"reconcile", "--invoice", "5", "--provider", "google", "--from", "2026-10-01", "--to", "2026-10-31"}, // provider
	}
	for _, args := range cases {
		if code := h.run(args...); code == 0 {
			t.Errorf("%v: want a non-zero exit", args)
		}
	}
	// No database yet: runs, creates nothing, reports a mismatch with the helpful hint.
	h2 := newHarness(t, nil)
	if code := h2.run("reconcile", "--invoice", "5", "--from", "2026-10-01", "--to", "2026-10-31"); code != 0 {
		t.Fatalf("exit %d: %s", code, h2.err)
	}
	if !strings.Contains(h2.out.String(), "recorded no requests") {
		t.Errorf("missing no-requests hint:\n%s", h2.out)
	}
	if _, err := os.Stat(h2.app.Getenv("TROVE_DATA_DIR")); err == nil {
		t.Error("reconcile created the data directory")
	}
}
