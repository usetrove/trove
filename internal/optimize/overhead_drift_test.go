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
		got := EstTokens(int64(len(c.text)))
		if diff := got - o.PerUse; diff > slack || diff < -slack {
			t.Errorf("%s: registry declares %d tokens per use but the message is about %d; update UseOverhead in internal/technique/registry.go", c.name, o.PerUse, got)
		}
	}
}

// The trimmer's marker is netted inside its own estimate, so it declares no per-use figure; the Trove MCP tool
// definitions it cannot work without are charged to it per session (rule 15). Autopilot trials it only because
// both are declared: if either is dropped it must be refused again.
func TestTheTrimmerDeclaresItsOverheadAsNumbers(t *testing.T) {
	o, ok := technique.OverheadOf(OutputTrimmer{}.Name())
	if !ok {
		t.Fatal("output_trimmer adds text, so it must declare numbers or Autopilot has to refuse it")
	}
	if o.PerUse != 0 || !o.Netted || o.PerSession <= 0 {
		t.Errorf("declared %+v: the marker is netted in its estimate, and the tool definitions are a per-session cost", o)
	}
}

// The registry and the code must agree on how risky each optimizer is, because Autopilot's rule for
// what it may promote reads the registry's tier and the user is told the code's.
func TestRegistryTiersMatchTheCode(t *testing.T) {
	for _, c := range Matrix() {
		tq, ok := technique.Get(c.Optimizer)
		if !ok {
			continue
		}
		if tq.Effect != string(c.Tier) {
			t.Errorf("%s: registry %s says %q but the code says %q", c.Optimizer, tq.ID, tq.Effect, c.Tier)
		}
	}
}
