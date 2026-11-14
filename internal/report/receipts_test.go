package report

import (
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/measure"
)

func TestRenderReceiptsGoldens(t *testing.T) {
	at := time.Date(2026, 10, 29, 14, 5, 0, 0, time.UTC)
	est := func(v float64, unpriced int) measure.Figure {
		return measure.Figure{Value: v, Label: measure.Estimated, N: 10, Unpriced: unpriced}
	}
	hit := func(v float64) *measure.Figure { return &measure.Figure{Value: v, Label: measure.Measured, N: 10} }
	rs := []measure.Receipt{
		{Session: "abcd1234", Agent: "claude_code", EndedAt: at, Model: "claude-a", Models: 1, Calls: 42, Cost: est(3.2, 0), CacheHit: hit(81.2), Saved: est(0.4, 0)},
		{Session: "ef567890~1", Agent: "codex_cli", EndedAt: at.Add(-3 * time.Hour), Model: "gpt-x", Models: 3, Calls: 1, Cost: est(0.004, 1), Saved: est(0, 0)},
		{Session: "99999999", Agent: "", EndedAt: at.Add(-26 * time.Hour), Model: "claude-b", Models: 1, Calls: 7, Cost: est(212.4, 0), CacheHit: hit(0), Saved: est(31.5, 0)},
	}
	var out strings.Builder
	out.WriteString(RenderReceipts(nil, 30, Options{Loc: time.UTC}) + "\n\n")
	out.WriteString(RenderReceipts(rs, 30, Options{Loc: time.UTC}))
	out.WriteString("\n== narrow\n" + RenderReceipts(rs[:1], 30, Options{Loc: time.UTC, Width: 40}))
	out.WriteString("\n== notification text\n" + ReceiptLine(rs[0]) + "\n" + ReceiptLine(rs[1]) + "\n")
	golden(t, "receipts.txt", out.String())

	for _, r := range rs {
		assertLabeled(t, r)
	}
}

func TestReceiptLineNamesWhatWasOn(t *testing.T) {
	r := measure.Receipt{Calls: 2, Cost: measure.Figure{Value: 1, Label: measure.Estimated}, On: []string{"noise_normalizer"}}
	if got := ReceiptLine(r); !strings.Contains(got, "on: noise_normalizer") {
		t.Errorf("line %q does not name the optimizer that was on", got)
	}
	r.On = nil
	if got := ReceiptLine(r); strings.Contains(got, "on:") {
		t.Errorf("line %q mentions optimizers when none were on", got)
	}
}
