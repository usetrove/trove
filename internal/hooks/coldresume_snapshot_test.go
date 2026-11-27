package hooks

import (
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
)

// The snapshot carries the cold-resume settings, so the hook never parses config.toml on its hot path.
func TestSnapshotCarriesTheColdResumeSettings(t *testing.T) {
	cfg := config.Default()
	cfg.Optimizers = map[string]string{config.OptimizerHookColdResume: config.StateOn}
	cfg.Optimize.ColdResumeAction, cfg.Optimize.ColdResumeMinTokens, cfg.Optimize.ColdResumeMinWeekPct = config.ColdResumeAsk, 200000, 1.5
	snap := BuildSnapshot(cfg, nil, false, time.Now())
	if snap.States[config.OptimizerHookColdResume] != stateOn {
		t.Errorf("state: %q", snap.States[config.OptimizerHookColdResume])
	}
	want := ColdResumeSettings{Action: config.ColdResumeAsk, MinTokens: 200000, MinWeekPct: 1.5}
	if snap.ColdResume != want {
		t.Errorf("settings: %+v, want %+v", snap.ColdResume, want)
	}
	if got := BuildSnapshot(config.Default(), nil, false, time.Now()); got.States[config.OptimizerHookColdResume] != stateOff {
		t.Errorf("the guard must be off by default, got %q", got.States[config.OptimizerHookColdResume])
	}
}
