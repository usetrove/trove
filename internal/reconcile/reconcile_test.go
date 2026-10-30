package reconcile

import (
	"strings"
	"testing"
	"time"
)

func TestCompareVerdicts(t *testing.T) {
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	in := func(invoice float64) Input {
		return Input{Provider: "anthropic", From: day, To: day.AddDate(0, 1, 0), InvoiceUSD: invoice}
	}
	rows := func(cost float64, unpriced int64) []ModelRow {
		return []ModelRow{{Model: "m", Requests: 10, Unpriced: unpriced, CostUSD: cost}}
	}
	tests := []struct {
		name    string
		invoice float64
		rows    []ModelRow
		want    string
		pct     float64
	}{
		{"exact", 100, rows(100, 0), VerdictMatch, 0},
		{"inside tolerance below", 100, rows(98.5, 0), VerdictMatch, -1.5},
		{"boundary is a match", 100, rows(98, 0), VerdictMatch, -2},
		{"just past boundary", 100, rows(97.9, 0), VerdictMismatch, -2.1},
		{"above invoice", 100, rows(103, 0), VerdictMismatch, 3},
		{"empty period zero invoice", 0, nil, VerdictMatch, 0},
		{"recorded but zero invoice", 0, rows(5, 0), VerdictMismatch, 0},
		{"nothing recorded", 50, nil, VerdictMismatch, -100},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := Compare(in(tc.invoice), tc.rows, nil)
			if r.Verdict != tc.want {
				t.Errorf("verdict = %s, want %s (diff %.2f%%)", r.Verdict, tc.want, r.DiffPct)
			}
			if r.DiffPct != tc.pct {
				t.Errorf("diff pct = %v, want %v", r.DiffPct, tc.pct)
			}
			if r.TolerancePct != DefaultTolerancePct {
				t.Errorf("tolerance = %v, want default", r.TolerancePct)
			}
		})
	}
}

func TestCompareCustomToleranceAndHints(t *testing.T) {
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	in := Input{Provider: "openai", From: day, To: day.AddDate(0, 0, 7), InvoiceUSD: 100, TolerancePct: 5}
	r := Compare(in, []ModelRow{{Model: "a", Requests: 3, Unpriced: 1, CostUSD: 96}}, []string{"v1", "v2"})
	if r.Verdict != VerdictMatch || r.TolerancePct != 5 {
		t.Fatalf("got %s at %v%%", r.Verdict, r.TolerancePct)
	}
	if r.UnpricedRequests != 1 || !strings.Contains(strings.Join(r.Hints, " "), "lower bound") {
		t.Errorf("want an unpriced lower-bound hint, got %v", r.Hints)
	}

	bad := Compare(Input{Provider: "openai", From: day, To: day.AddDate(0, 0, 7), InvoiceUSD: 100},
		[]ModelRow{{Model: "a", Requests: 3, CostUSD: 80}}, []string{"v1", "v2"})
	joined := strings.Join(bad.Hints, " ")
	for _, want := range []string{"below the invoice", "--utc", "pricing data version"} {
		if !strings.Contains(joined, want) {
			t.Errorf("hints lack %q: %v", want, bad.Hints)
		}
	}
}

func TestCompareIsDeterministic(t *testing.T) {
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	in := Input{Provider: "anthropic", From: day, To: day.AddDate(0, 1, 0), InvoiceUSD: 10}
	a := []ModelRow{{Model: "b", Requests: 1, CostUSD: 3}, {Model: "a", Requests: 2, CostUSD: 7}}
	r1, r2 := Compare(in, a, nil), Compare(in, a, nil)
	if r1.RecordedUSD != 10 || r1.Models[0].Model != "a" {
		t.Errorf("rows not sorted or summed: %+v", r1)
	}
	if r1.Verdict != r2.Verdict || r1.RecordedUSD != r2.RecordedUSD {
		t.Error("same input gave different results")
	}
}
