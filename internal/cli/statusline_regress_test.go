package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/regress"
)

func TestStatusLineShowsTheRegressionAlert(t *testing.T) {
	h := newHarness(t, map[string]string{"NO_COLOR": "1"})
	dir := h.app.Getenv("TROVE_DATA_DIR")
	seedForStatus(t, dir, 10)
	a := regress.Alert{Check: regress.Check{Kind: regress.KindVersion, Key: "2.1.89", Metric: regress.MetricThinkShare,
		Value: 0.9, Baseline: 0.5, Change: 0.8, N: 300, Sessions: 6, Label: regress.LabelMeasured}, Cause: regress.CauseClient}
	a.Sentence = regress.Sentence(a)
	if err := regress.WriteCache(dir, regress.Result{ComputedAt: time.Now().UnixMilli(), Alerts: []regress.Alert{a}}); err != nil {
		t.Fatal(err)
	}
	if code := h.run("status-line", "--format", "claude"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	want := "· Claude Code 2.1.89: requests with thinking up 80% on your usual (90.0% vs 50.0%, 300 requests in 6 sessions, measured)."
	if !strings.Contains(h.out.String(), want) {
		t.Errorf("status line %q lacks %q", h.out, want)
	}

	// An old result is not shown.
	if err := regress.WriteCache(dir, regress.Result{ComputedAt: time.Now().Add(-48 * time.Hour).UnixMilli(), Alerts: []regress.Alert{a}}); err != nil {
		t.Fatal(err)
	}
	h.out.Reset()
	h.run("status-line", "--format", "claude")
	if strings.Contains(h.out.String(), "2.1.89") {
		t.Errorf("stale alert shown: %q", h.out)
	}
}
