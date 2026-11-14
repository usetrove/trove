package config

import "testing"

func probationFile(policy string) AutopilotFile {
	return AutopilotFile{Enabled: true, Policy: policy, Entries: map[string]AutopilotEntry{
		"hook_repeat_read": {State: AutopilotProbation},
		"noise_normalizer": {State: AutopilotOn},
		"output_trimmer":   {State: AutopilotShadow},
	}}
}

// A reader that cannot assign a random arm (the hooks, the plugin engine) must never see a trial as "on":
// that would turn it on for every session with nothing to compare against.
func TestAnOptimizerOnTrialIsShadowForReadersWithNoAssigner(t *testing.T) {
	got := probationFile(AutopilotPolicyV2).States()
	if got["hook_repeat_read"] != StateShadow || got["noise_normalizer"] != StateOn || got["output_trimmer"] != StateShadow {
		t.Errorf("States() = %v", got)
	}
	c := Default().WithAutopilot(probationFile(AutopilotPolicyV2))
	if c.OptimizerState("hook_repeat_read") != StateShadow {
		t.Errorf("a Config built for the hooks sees the trial as %q", c.OptimizerState("hook_repeat_read"))
	}
}

// The gateway has the assigner, which lowers "on" to shadow for the comparison half, so a trial is "on" there,
// but only if A/B comparison is enabled: without the assigner "on" would be on for everyone.
func TestAnOptimizerOnTrialIsOnOnlyWhereTheAssignerCanSplitSessions(t *testing.T) {
	f := probationFile(AutopilotPolicyV2)
	if got := f.StatesWithArms(true)["hook_repeat_read"]; got != StateOn {
		t.Errorf("with an assigner: %q, want on", got)
	}
	if got := f.StatesWithArms(false)["hook_repeat_read"]; got != StateShadow {
		t.Errorf("without an assigner: %q, want shadow", got)
	}
	if got := f.StatesWithArms(false)["noise_normalizer"]; got != StateOn {
		t.Errorf("a lossless optimizer Autopilot turned on stays on: %q", got)
	}
}

func TestOnlyKnownPoliciesAreActive(t *testing.T) {
	for policy, want := range map[string]bool{AutopilotPolicy: true, AutopilotPolicyV2: true, "lossless-v3": false, "": false} {
		if got := probationFile(policy).Active(); got != want {
			t.Errorf("policy %q active = %v, want %v", policy, got, want)
		}
	}
	paused := probationFile(AutopilotPolicyV2)
	paused.Paused = true
	if paused.States() != nil || paused.StatesWithArms(true) != nil {
		t.Error("a paused Autopilot contributes no states")
	}
}
