package report

import (
	"testing"

	"github.com/usetrove/trove/internal/analyze"
	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/measure"
)

// Fixed overhead (unused tool definitions, heavy context files) needs no proxy
// and works on subscriptions, so it is listed before larger per-request waste.
func TestLeaksListFixedOverheadFirst(t *testing.T) {
	fig := func(v float64) measure.Figure { return measure.Figure{Value: v, Label: measure.Estimated} }
	cats := []measure.WasteRow{
		{Category: analyze.DetOversized, USD: fig(9), Tokens: fig(9000)},
		{Category: analyze.DetDuplicate, USD: fig(7), Tokens: fig(7000)},
		{Category: analyze.DetContextFiles, USD: fig(1), Tokens: fig(1000)},
		{Category: analyze.DetNoisy, USD: fig(5), Tokens: fig(5000)},
		{Category: analyze.DetUnusedTools, USD: fig(2), Tokens: fig(2000)},
		{Category: analyze.DetDrift, USD: fig(4), Tokens: fig(4000)},
		{Category: analyze.DetCacheMiss, USD: fig(3), Tokens: fig(3000)},
	}
	got := buildLeaks(cats, config.Default(), nil, false)
	want := []string{analyze.DetUnusedTools, analyze.DetContextFiles, analyze.DetOversized, analyze.DetDuplicate, analyze.DetNoisy}
	if len(got) != len(want) {
		t.Fatalf("%d leaks, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Category != w {
			t.Errorf("leak %d is %s, want %s", i, got[i].Category, w)
		}
		if fixed := i < 2; got[i].FixedOverhead != fixed {
			t.Errorf("leak %s fixed_overhead=%v, want %v", got[i].Category, got[i].FixedOverhead, fixed)
		}
	}
}
