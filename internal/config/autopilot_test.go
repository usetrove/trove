package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func apFile(states map[string]string) AutopilotFile {
	f := AutopilotFile{Enabled: true, Policy: AutopilotPolicy, Entries: map[string]AutopilotEntry{}}
	for n, s := range states {
		f.Entries[n] = AutopilotEntry{State: s}
	}
	return f
}

func TestAutopilotOverlayOnlyFillsWhatTheUserLeftUnset(t *testing.T) {
	c := Default()
	c.Optimizers = map[string]string{"noise_normalizer": StateOff}
	c = c.WithAutopilot(apFile(map[string]string{
		"noise_normalizer":           AutopilotOn, // the user said off: the user wins
		"duplicate_result_collapser": AutopilotOn,
		"output_trimmer":             AutopilotCandidate,
		"cache_breakpoints":          AutopilotRolledBack,
		"volatile_prefix_mover":      AutopilotOff,
	}))
	want := map[string]string{
		"noise_normalizer":           StateOff,
		"duplicate_result_collapser": StateOn,
		"output_trimmer":             StateShadow,
		"cache_breakpoints":          StateShadow,
		"volatile_prefix_mover":      StateOff,
		"something_else":             StateOff,
	}
	for n, w := range want {
		if got := c.OptimizerState(n); got != w {
			t.Errorf("%s: %q, want %q", n, got, w)
		}
	}
}

func TestAutopilotInactiveChangesNothing(t *testing.T) {
	on := map[string]string{"noise_normalizer": AutopilotOn}
	for name, mut := range map[string]func(*AutopilotFile){
		"not consented":  func(f *AutopilotFile) { f.Enabled = false },
		"paused":         func(f *AutopilotFile) { f.Paused = true },
		"unknown policy": func(f *AutopilotFile) { f.Policy = "lossless-v99" },
	} {
		f := apFile(on)
		mut(&f)
		if got := Default().WithAutopilot(f).OptimizerState("noise_normalizer"); got != StateOff {
			t.Errorf("%s: state %q, want off", name, got)
		}
	}
}

func TestLoadReadsAutopilotAndSurvivesADamagedFile(t *testing.T) {
	dir := t.TempDir()
	if err := SaveAutopilot(dir, apFile(map[string]string{"noise_normalizer": AutopilotOn})); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.OptimizerState("noise_normalizer"); got != StateOn {
		t.Errorf("state %q, want on", got)
	}
	if err := os.WriteFile(filepath.Join(dir, AutopilotFileName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(dir, func(string) string { return "" })
	if err != nil {
		t.Fatalf("a damaged autopilot.json must not stop Trove starting: %v", err)
	}
	if got := cfg.OptimizerState("noise_normalizer"); got != StateOff {
		t.Errorf("damaged file gave state %q, want off", got)
	}
	if _, err := LoadAutopilot(dir); err == nil {
		t.Error("LoadAutopilot on a damaged file should say so")
	}
}

func TestSaveAutopilotIsPrivateAndHoldsNoRequestContent(t *testing.T) {
	dir := t.TempDir()
	if err := SaveAutopilot(dir, apFile(map[string]string{"noise_normalizer": AutopilotShadow})); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, AutopilotFileName)
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
			t.Errorf("mode %v, want 0600", fi.Mode().Perm())
		}
	}
	if m, _ := filepath.Glob(filepath.Join(dir, AutopilotFileName+".*")); len(m) != 0 {
		t.Errorf("temp files left behind: %v", m)
	}
	f, err := LoadAutopilot(dir)
	if err != nil || !f.Active() || len(f.Names()) != 1 {
		t.Errorf("round trip: %+v, %v", f, err)
	}
}

func TestMissingFileIsNotEnabled(t *testing.T) {
	f, err := LoadAutopilot(t.TempDir())
	if err != nil || f.Active() {
		t.Errorf("missing file: active=%v err=%v", f.Active(), err)
	}
}
