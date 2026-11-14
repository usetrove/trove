package autopilot

import (
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
)

var t0 = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

func good() Evidence {
	return Evidence{BaselineDone: true, ShadowSessions: 12, NetTokens: 5000}
}

func entry(state string, since time.Time, attempts int) config.AutopilotEntry {
	return config.AutopilotEntry{State: state, SinceMS: since.UnixMilli(), Attempts: attempts}
}

func TestDecide(t *testing.T) {
	p := DefaultPolicy()
	cases := []struct {
		name      string
		cur       config.AutopilotEntry
		ev        Evidence
		now       time.Time
		want      string
		attempts  int
		wantClear bool
	}{
		{"new optimizer starts in shadow", config.AutopilotEntry{}, Evidence{}, t0, config.AutopilotShadow, 0, false},
		{"off starts in shadow", entry(config.AutopilotOff, t0, 0), Evidence{}, t0, config.AutopilotShadow, 0, false},
		{"shadow waits for the baseline", entry(config.AutopilotShadow, t0, 0), Evidence{ShadowSessions: 50, NetTokens: 1e6}, t0, config.AutopilotShadow, 0, false},
		{"shadow waits for enough sessions", entry(config.AutopilotShadow, t0, 0), Evidence{BaselineDone: true, ShadowSessions: 9, NetTokens: 1e6}, t0, config.AutopilotShadow, 0, false},
		{"shadow waits for enough saving", entry(config.AutopilotShadow, t0, 0), Evidence{BaselineDone: true, ShadowSessions: 50, NetTokens: 1999}, t0, config.AutopilotShadow, 0, false},
		{"negative net saving never promotes", entry(config.AutopilotShadow, t0, 0), Evidence{BaselineDone: true, ShadowSessions: 50, NetTokens: -5}, t0, config.AutopilotShadow, 0, false},
		{"good evidence makes a candidate, not on", entry(config.AutopilotShadow, t0, 0), good(), t0, config.AutopilotCandidate, 0, false},
		{"candidate confirmed becomes on", entry(config.AutopilotCandidate, t0, 0), good(), t0, config.AutopilotOn, 1, false},
		{"candidate whose evidence fell goes back", entry(config.AutopilotCandidate, t0, 0), Evidence{BaselineDone: true, ShadowSessions: 12, NetTokens: 10}, t0, config.AutopilotShadow, 0, false},
		{"on stays on", entry(config.AutopilotOn, t0, 1), good(), t0, config.AutopilotOn, 1, false},
		{"on that a guardrail rolled back", entry(config.AutopilotOn, t0, 1), Evidence{RolledBack: true}, t0, config.AutopilotRolledBack, 1, false},
		{"rolled back waits out the cool-down", entry(config.AutopilotRolledBack, t0, 1), Evidence{RolledBack: true}, t0.Add(3 * 24 * time.Hour), config.AutopilotRolledBack, 1, false},
		{"rolled back retries after the cool-down", entry(config.AutopilotRolledBack, t0, 1), Evidence{RolledBack: true}, t0.Add(8 * 24 * time.Hour), config.AutopilotShadow, 1, true},
		{"rolled back too often is left alone", entry(config.AutopilotRolledBack, t0, 2), Evidence{RolledBack: true}, t0.Add(30 * 24 * time.Hour), config.AutopilotRolledBack, 2, false},
		{"unknown state resets to shadow", entry("bogus", t0, 0), good(), t0, config.AutopilotShadow, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := p.Decide(c.cur, c.ev, c.now)
			if d.Entry.State != c.want || d.Entry.Attempts != c.attempts || d.ClearRollback != c.wantClear {
				t.Errorf("got state %q attempts %d clear %v; want %q %d %v (reason %q)",
					d.Entry.State, d.Entry.Attempts, d.ClearRollback, c.want, c.attempts, c.wantClear, d.Entry.Reason)
			}
			if d.Entry.Reason == "" && d.Entry.State != config.AutopilotOn && d.Entry.State != config.AutopilotShadow {
				t.Errorf("state %q has no plain-English reason", d.Entry.State)
			}
		})
	}
}

// Promotion takes two checks in a row, and nothing skips the shadow step.
func TestNeverGoesStraightToOn(t *testing.T) {
	p := DefaultPolicy()
	cur := config.AutopilotEntry{}
	var seen []string
	for i := 0; i < 4; i++ {
		cur = p.Decide(cur, good(), t0).Entry
		seen = append(seen, cur.State)
	}
	want := []string{config.AutopilotShadow, config.AutopilotCandidate, config.AutopilotOn, config.AutopilotOn}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("sequence %v, want %v", seen, want)
		}
	}
}

func TestDecideIsDeterministic(t *testing.T) {
	p := DefaultPolicy()
	cur := entry(config.AutopilotShadow, t0, 0)
	a, b := p.Decide(cur, good(), t0), p.Decide(cur, good(), t0)
	if a != b {
		t.Errorf("same inputs gave %+v and %+v", a, b)
	}
}

func TestOnlyLosslessOptimizersAreEligibleOnDayOne(t *testing.T) {
	p := DefaultPolicy()
	for _, n := range []string{"noise_normalizer", "duplicate_result_collapser"} {
		if !p.IsEligible(n) {
			t.Errorf("%s should be eligible", n)
		}
	}
	// These touch the cached prefix or can change what the model sees.
	for _, n := range []string{"output_trimmer", "cache_breakpoints", "volatile_prefix_mover", "model_default", ""} {
		if p.IsEligible(n) {
			t.Errorf("%q must not be eligible for automatic promotion", n)
		}
	}
}
