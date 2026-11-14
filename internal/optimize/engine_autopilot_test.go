package optimize

import (
	"context"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
)

// A session keeps the states it started with, whatever Autopilot decides later.
// A session that starts after the change gets the new states. (CLAUDE.md rule 5.)
func TestAutopilotChangesReachOnlyNewSessions(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	states := map[string]string{"noise_normalizer": config.StateShadow}
	e := New(Options{
		Config:          config.Default(),
		Now:             func() time.Time { return now },
		AutopilotStates: func() map[string]string { return states },
	})
	ctx := context.Background()
	pin := func(key string) string {
		p, err := e.pins.get(ctx, key, e.current(), e.bpt, e.now(), nil)
		if err != nil {
			t.Fatal(err)
		}
		return p.States["noise_normalizer"]
	}

	if got := pin("session-a"); got != config.StateShadow {
		t.Fatalf("session-a started with %q, want shadow", got)
	}
	states = map[string]string{"noise_normalizer": config.StateOn}

	// Inside the refresh window nothing is re-read, and a pinned session never changes.
	now = now.Add(time.Second)
	if got := pin("session-a"); got != config.StateShadow {
		t.Errorf("session-a changed to %q mid-session", got)
	}
	if got := pin("session-b"); got != config.StateShadow {
		t.Errorf("session-b started inside the refresh window with %q, want the cached shadow", got)
	}

	now = now.Add(autopilotRefresh)
	if got := pin("session-a"); got != config.StateShadow {
		t.Errorf("session-a changed to %q after Autopilot turned it on; a running session must keep its pin", got)
	}
	if got := pin("session-c"); got != config.StateOn {
		t.Errorf("session-c started after the change with %q, want on", got)
	}
}

// A user's own setting beats Autopilot, and a failing source only lowers states.
func TestAutopilotNeverOverridesTheUserAndFailsLow(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	cfg := config.Default()
	cfg.Optimizers = map[string]string{"noise_normalizer": config.StateOff}
	states := map[string]string{"noise_normalizer": config.StateOn, "duplicate_result_collapser": config.StateOn}
	e := New(Options{Config: cfg, Now: func() time.Time { return now }, AutopilotStates: func() map[string]string { return states }})

	if got := e.current().OptimizerState("noise_normalizer"); got != config.StateOff {
		t.Errorf("Autopilot overrode the user's off: %q", got)
	}
	if got := e.current().OptimizerState("duplicate_result_collapser"); got != config.StateOn {
		t.Errorf("duplicate_result_collapser %q, want on", got)
	}
	if !e.Enabled() {
		t.Error("engine should be enabled once Autopilot has something on")
	}

	states = nil // the source failed
	now = now.Add(autopilotRefresh)
	if got := e.current().OptimizerState("duplicate_result_collapser"); got != config.StateOff {
		t.Errorf("a failing source left %q, want off", got)
	}
	if e.Enabled() {
		t.Error("engine should be idle again when nothing is on or in shadow")
	}
}
