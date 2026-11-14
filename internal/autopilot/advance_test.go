package autopilot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/measure"
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
		res, err := Advance(context.Background(), db, dir, cfg, DefaultPolicy(), t0.Add(time.Duration(i)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		for n, e := range res.File.Entries {
			if e.State == config.AutopilotOn || e.State == config.AutopilotCandidate {
				t.Fatalf("%s reached %q with no shadow evidence", n, e.State)
			}
		}
	}
}

func TestAdvanceDoesNothingUntilTheUserConsents(t *testing.T) {
	db, dir := open(t)
	res, err := Advance(context.Background(), db, dir, config.Default(), DefaultPolicy(), t0)
	if err != nil || res.Changed || res.File.Active() {
		t.Fatalf("active=%v changed=%v err=%v", res.File.Active(), res.Changed, err)
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
	res, err := Advance(context.Background(), db, dir, cfg, DefaultPolicy(), t0.Add(time.Hour))
	if err != nil || !res.Changed {
		t.Fatalf("changed=%v err=%v", res.Changed, err)
	}
	f := res.File
	if _, has := f.Entries["noise_normalizer"]; has {
		t.Error("Autopilot kept managing an optimizer the user set in config.toml")
	}
	if _, has := f.Entries["duplicate_result_collapser"]; !has {
		t.Error("Autopilot dropped an optimizer the user did not set")
	}
}

// Something already on that cannot share the job holds the optimizer back, in the
// state machine and in the shadow planner. The reason comes from the registry.
func TestABlockedOptimizerIsHeldBackAtEveryStep(t *testing.T) {
	p := DefaultPolicy()
	ev := Evidence{BaselineDone: true, ShadowSessions: 50, NetTokens: 1e6, Blocked: "both shorten the same shell output"}
	for _, state := range []string{config.AutopilotShadow, config.AutopilotCandidate} {
		d := p.Decide(config.AutopilotEntry{State: state, SinceMS: t0.UnixMilli()}, ev, t0)
		want := state
		if state == config.AutopilotCandidate {
			want = config.AutopilotShadow // the evidence did not hold, so it goes back
		}
		if d.Entry.State != want || !strings.Contains(d.Entry.Reason, "held back: both shorten the same shell output") {
			t.Errorf("%s with a blocker -> %q (%q); want %q and the reason", state, d.Entry.State, d.Entry.Reason, want)
		}
	}
	plan := p.Plan(map[string]measure.ShadowRow{"noise_normalizer": row(50, 1e6)}, nil, true, map[string]string{"noise_normalizer": "RTK is on"}, nil)
	if v := verdicts(plan)["noise_normalizer"]; v != VerdictBlocked {
		t.Errorf("planner verdict %q, want blocked", v)
	}
}

// A wrapped shell compressor that is on keeps Autopilot from turning on the built-in
// output optimizer. `trove external` refuses the reverse order; this closes the gap.
func TestAutopilotDoesNotPromoteBesideAWrappedShellCompressor(t *testing.T) {
	db, dir := open(t)
	ctx := context.Background()
	if _, err := Enable(dir, DefaultPolicy(), t0); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	if r := blockedBy(onNow(ctx, db, cfg), "noise_normalizer"); r != "" {
		t.Fatalf("nothing is on, yet it is blocked: %q", r)
	}
	if err := db.ExternalTools().Put(ctx, store.ExternalTool{Name: "rtk", Version: "1.1.0", State: "on", InstalledAt: t0, LicenseAckAt: t0}); err != nil {
		t.Fatal(err)
	}
	r := blockedBy(onNow(ctx, db, cfg), "noise_normalizer")
	if !strings.Contains(r, "shorten the same shell output") {
		t.Fatalf("with RTK on, noise_normalizer is blocked by %q", r)
	}
	// duplicate_result_collapser works on a different job and is not in the way.
	if r := blockedBy(onNow(ctx, db, cfg), "duplicate_result_collapser"); r != "" {
		t.Errorf("duplicate_result_collapser was blocked: %q", r)
	}
	res, err := Advance(ctx, db, dir, cfg, DefaultPolicy(), t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	got := verdicts(res.File.Plan)
	if got["noise_normalizer"] != VerdictBlocked || got["duplicate_result_collapser"] == VerdictBlocked {
		t.Errorf("plan verdicts %v", got)
	}
	// A switched-off tool is not in the way.
	if err := db.ExternalTools().Put(ctx, store.ExternalTool{Name: "rtk", Version: "1.1.0", State: "off", InstalledAt: t0, LicenseAckAt: t0}); err != nil {
		t.Fatal(err)
	}
	if r := blockedBy(onNow(ctx, db, cfg), "noise_normalizer"); r != "" {
		t.Errorf("a tool that is off blocked an optimizer: %q", r)
	}
}
