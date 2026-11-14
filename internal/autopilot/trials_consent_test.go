package autopilot

import (
	"testing"

	"github.com/usetrove/trove/internal/config"
)

func TestEnableTrialsNeedsAutopilotAndChangesNoRequest(t *testing.T) {
	_, dir := open(t)
	if _, err := EnableTrials(dir, probNow); err == nil {
		t.Fatal("trials were enabled with Autopilot off")
	}
	if _, err := Enable(dir, DefaultPolicy(), probNow); err != nil {
		t.Fatal(err)
	}
	f, err := EnableTrials(dir, probNow)
	if err != nil {
		t.Fatal(err)
	}
	if f.Policy != config.AutopilotPolicyV2 || f.TrialsConsentedAt != probNow.UnixMilli() {
		t.Errorf("consent not recorded: %+v", f)
	}
	for _, n := range PolicyV2().Probation {
		if e := f.Entries[n]; e.State != config.AutopilotShadow {
			t.Errorf("%s: %q, want shadow", n, e.State)
		}
	}
	if CurrentPolicy(dir).Name != config.AutopilotPolicyV2 {
		t.Error("CurrentPolicy must follow the consent record")
	}
	if got := f.States()["hook_repeat_read"]; got != config.StateShadow {
		t.Errorf("a candidate only estimates: %q", got)
	}
}

func TestDisableTrialsForgetsCandidatesAndKeepsWhatV1Manages(t *testing.T) {
	_, dir := open(t)
	_, _ = Enable(dir, DefaultPolicy(), probNow)
	_, _ = EnableTrials(dir, probNow)
	f, _ := config.LoadAutopilot(dir)
	f.Entries["hook_repeat_read"] = config.AutopilotEntry{State: config.AutopilotOn} // kept by a trial
	f.Entries["noise_normalizer"] = config.AutopilotEntry{State: config.AutopilotOn} // kept under v1
	if err := config.SaveAutopilot(dir, f); err != nil {
		t.Fatal(err)
	}
	f, err := DisableTrials(dir, probNow)
	if err != nil {
		t.Fatal(err)
	}
	if f.Policy != config.AutopilotPolicy || f.TrialsConsentedAt != 0 {
		t.Errorf("policy after: %+v", f)
	}
	if _, there := f.Entries["hook_repeat_read"]; there {
		t.Error("what a trial kept must go when trials are withdrawn")
	}
	if f.Entries["noise_normalizer"].State != config.AutopilotOn {
		t.Error("what Autopilot manages under v1 must be untouched")
	}
	if got := f.States()["hook_repeat_read"]; got == config.StateOn {
		t.Error("hook_repeat_read is still on")
	}
	found := false
	for _, e := range f.History {
		if e.Optimizer == "hook_repeat_read" && e.From == config.AutopilotOn && e.To == config.AutopilotOff {
			found = true
		}
	}
	if !found {
		t.Error("the removal must be recorded as an event the user can see")
	}
}

func TestCurrentPolicyFallsBackToV1(t *testing.T) {
	_, dir := open(t)
	if CurrentPolicy(dir).Name != config.AutopilotPolicy {
		t.Error("no file: v1")
	}
	f := config.AutopilotFile{Version: 1, Enabled: true, Policy: "lossless-v9", Entries: map[string]config.AutopilotEntry{}}
	if err := config.SaveAutopilot(dir, f); err != nil {
		t.Fatal(err)
	}
	if CurrentPolicy(dir).Name != config.AutopilotPolicy || len(CurrentPolicy(dir).Probation) != 0 {
		t.Error("an unknown policy must shrink to v1")
	}
}

// A stray probation entry under the v1 policy (a hand-edited file, or a leftover) must not become a trial.
func TestAProbationEntryUnderV1IsNeverATrial(t *testing.T) {
	f := config.AutopilotFile{Enabled: true, Policy: config.AutopilotPolicy, Entries: map[string]config.AutopilotEntry{
		"hook_repeat_read": {State: config.AutopilotProbation},
	}}
	if len(f.Trials()) != 0 {
		t.Errorf("Trials() = %v under v1", f.Trials())
	}
	if got := f.StatesWithArms(true)["hook_repeat_read"]; got != config.StateShadow {
		t.Errorf("under v1 an assigner must still see shadow, got %q", got)
	}
}
