package optimize

import (
	"testing"

	"github.com/usetrove/trove/internal/technique"
)

// The technique registry declares how many tokens each text-adding hook costs every time it acts, and
// Autopilot subtracts that figure (rule 15). If someone rewrites a message, the declared figure must
// follow, or Autopilot would be charging the wrong cost.
func TestDeclaredPerUseOverheadMatchesTheRealMessages(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{"hook_repeat_read", repeatReadMessage(12, "3m")},
		{"hook_large_output", largeOutputHint},
		{"hook_read_limit", readLimitNote(250_000)},
	}
	const slack = 6 // tokens: prompt numbers and sizes vary a little in width
	for _, c := range cases {
		o, ok := technique.OverheadOf(c.name)
		if !ok {
			t.Errorf("%s: no declared overhead", c.name)
			continue
		}
		real := EstTokens(int64(len(c.text)))
		if diff := real - o.PerUse; diff > slack || diff < -slack {
			t.Errorf("%s: registry declares %d tokens per use but the message is about %d; update UseOverhead in internal/technique/registry.go", c.name, o.PerUse, real)
		}
	}
}

// The trimmer's marker is netted inside its own estimate, so it declares no separate figure.
func TestUndeclaredTextAdderStaysOutOfAutopilot(t *testing.T) {
	if _, ok := technique.OverheadOf(OutputTrimmer{}.Name()); ok {
		t.Error("output_trimmer now declares numbers; revisit whether it may join Autopilot, and update this test")
	}
}
