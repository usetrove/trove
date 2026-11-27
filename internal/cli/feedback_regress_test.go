package cli

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/regress"
)

func TestFeedbackRegressionDraftsNumbersOnly(t *testing.T) {
	h := newHarness(t, nil)
	dir := h.app.Getenv("TROVE_DATA_DIR")
	h.app.Run = func(string, ...string) error { t.Error("opened a browser"); return nil }

	// No alert: nothing to draft.
	if code := h.run("feedback", "--regression", "--no-open"); code != 0 || !strings.Contains(h.out.String(), "no alert right now") {
		t.Fatalf("exit %d: %q", code, h.out)
	}

	a := regress.Alert{Check: regress.Check{Kind: regress.KindDay, Key: "2026-11-26", Metric: regress.MetricThinkShare,
		Value: 0.94, Baseline: 0.48, MADs: 6.2, Change: 0.96, N: 240, Sessions: 4, Blocks: 21, Label: regress.LabelMeasured},
		Cause: regress.CauseServer, Active: []string{"2.1.88", "2.1.89"}}
	a.Sentence, a.Advice = regress.Sentence(a), regress.Advice(a)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := regress.WriteCache(dir, regress.Result{ComputedAt: time.Now().UnixMilli(), Alerts: []regress.Alert{a}}); err != nil {
		t.Fatal(err)
	}
	h.out.Reset()
	if code := h.run("feedback", "--regression", "--no-open"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	out := h.out.String()
	for _, want := range []string{"day: 2026-11-26, Claude Code versions active: 2.1.88, 2.1.89", "now: 94.0%, usual: 48.0%",
		"likely cause: server side", "https://github.com/anthropics/claude-code/issues/new?", "/feedback inside Claude Code"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, dir) || strings.Contains(out, "drop effort") {
		t.Errorf("draft carries a path or the advice:\n%s", out)
	}
}
