package autopilot

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/store"
)

func open(t *testing.T) (*store.DB, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, dir
}

func TestEnableStartsEligibleOptimizersInShadowAndChangesNoRequest(t *testing.T) {
	_, dir := open(t)
	f, err := Enable(dir, DefaultPolicy(), t0)
	if err != nil {
		t.Fatal(err)
	}
	if !f.Active() || f.ConsentedAt != t0.UnixMilli() {
		t.Fatalf("consent not recorded: %+v", f)
	}
	cfg, err := config.Load(dir, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range DefaultPolicy().Eligible {
		if got := cfg.OptimizerState(n); got != config.StateShadow {
			t.Errorf("%s: %q after Enable, want shadow (estimate only)", n, got)
		}
	}
	for _, n := range []string{"output_trimmer", "cache_breakpoints", "volatile_prefix_mover"} {
		if got := cfg.OptimizerState(n); got != config.StateOff {
			t.Errorf("%s: %q, want off; it is not eligible", n, got)
		}
	}
}

func TestDisableAndPauseReturnToTheConfigFile(t *testing.T) {
	_, dir := open(t)
	if _, err := Enable(dir, DefaultPolicy(), t0); err != nil {
		t.Fatal(err)
	}
	load := func() config.Config {
		c, err := config.Load(dir, func(string) string { return "" })
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	if _, err := SetPaused(dir, true); err != nil {
		t.Fatal(err)
	}
	if got := load().OptimizerState("noise_normalizer"); got != config.StateOff {
		t.Errorf("paused: %q, want off", got)
	}
	if _, err := SetPaused(dir, false); err != nil {
		t.Fatal(err)
	}
	if got := load().OptimizerState("noise_normalizer"); got != config.StateShadow {
		t.Errorf("resumed: %q, want shadow", got)
	}
	if err := Disable(dir); err != nil {
		t.Fatal(err)
	}
	if got := load().OptimizerState("noise_normalizer"); got != config.StateOff {
		t.Errorf("disabled: %q, want off", got)
	}
}

// With no usage recorded there is no evidence, so Advance promotes nothing.
func TestAdvanceWithoutEvidencePromotesNothing(t *testing.T) {
	db, dir := open(t)
	if _, err := Enable(dir, DefaultPolicy(), t0); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	for i := 0; i < 5; i++ {
		f, _, err := Advance(context.Background(), db, dir, cfg, DefaultPolicy(), t0.Add(time.Duration(i)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		for n, e := range f.Entries {
			if e.State == config.AutopilotOn || e.State == config.AutopilotCandidate {
				t.Fatalf("%s reached %q with no shadow evidence", n, e.State)
			}
		}
	}
}

func TestAdvanceDoesNothingUntilTheUserConsents(t *testing.T) {
	db, dir := open(t)
	f, changed, err := Advance(context.Background(), db, dir, config.Default(), DefaultPolicy(), t0)
	if err != nil || changed || f.Active() {
		t.Fatalf("active=%v changed=%v err=%v", f.Active(), changed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, config.AutopilotFileName)); err == nil {
		t.Error("Advance created autopilot.json without consent")
	}
}

func TestAdvanceLeavesAnOptimizerTheUserSetAlone(t *testing.T) {
	db, dir := open(t)
	if _, err := Enable(dir, DefaultPolicy(), t0); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Optimizers = map[string]string{"noise_normalizer": config.StateOff}
	f, changed, err := Advance(context.Background(), db, dir, cfg, DefaultPolicy(), t0.Add(time.Hour))
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if _, has := f.Entries["noise_normalizer"]; has {
		t.Error("Autopilot kept managing an optimizer the user set in config.toml")
	}
	if _, has := f.Entries["duplicate_result_collapser"]; !has {
		t.Error("Autopilot dropped an optimizer the user did not set")
	}
}
