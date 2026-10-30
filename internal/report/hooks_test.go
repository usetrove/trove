package report

import (
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/measure"
)

func TestHooksLinesLabelEveryNumber(t *testing.T) {
	rate := measure.Figure{Value: 10, Label: measure.Measured, N: 40}
	got := hooksLines([]measure.HookStats{
		{Hook: "hook_repeat_read", Blocked: 40, WouldBlock: 3, RereadAnyway: 4, RereadRate: &rate,
			TokensSaved: measure.Figure{Value: 72000, Label: measure.Estimated, N: 40}},
		{Hook: "hook_large_output", WouldAdvise: 5},
		{Hook: "hook_other"},
	})
	if len(got) != 3 {
		t.Fatalf("lines = %v", got)
	}
	for _, want := range []string{"40 repeat reads blocked", "3 more would have been blocked", "(estimated)", "10% of blocks (measured)"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("line 0 missing %q: %s", want, got[0])
		}
	}
	if !strings.Contains(got[1], "5 more would have been added in shadow mode") || strings.Contains(got[1], "tokens") {
		t.Errorf("line 1 = %s (advice has no savings estimate)", got[1])
	}
	if !strings.Contains(got[2], "no activity") {
		t.Errorf("line 2 = %s", got[2])
	}
}
