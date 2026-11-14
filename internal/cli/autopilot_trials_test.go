package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/autopilot"
	"github.com/usetrove/trove/internal/config"
)

func TestTrialsNeedAutopilotOnFirst(t *testing.T) {
	h := newHarness(t, nil)
	if code := h.run("autopilot", "trials", "on", "--yes"); code != 1 || !strings.Contains(h.err.String(), "trove autopilot on") {
		t.Errorf("trials while Autopilot is off: exit %d, stderr %q", code, h.err)
	}
	if f, _ := config.LoadAutopilot(apDir(h)); f.Policy == config.AutopilotPolicyV2 {
		t.Error("trials were recorded with Autopilot off")
	}
}

// Trials are a second, separate yes. Nothing can answer a prompt here, so nothing is turned on, and JSON mode
// refuses rather than guessing.
func TestTrialsNeedTheirOwnExplicitYes(t *testing.T) {
	h := newHarness(t, nil)
	h.run("autopilot", "on", "--yes")
	h.out.Reset()
	if code := h.run("autopilot", "trials", "on"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	f, _ := config.LoadAutopilot(apDir(h))
	if f.Policy != config.AutopilotPolicy {
		t.Fatalf("trials turned on with nobody saying yes: policy %q", f.Policy)
	}
	for _, want := range []string{"not lossless", "about half of your new sessions", "20 sessions on each side", "never switches sides", "trove autopilot trials off", "Nothing was changed"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("the consent or the refusal lacks %q:\n%s", want, h.out)
		}
	}
	h.err.Reset()
	if code := h.run("autopilot", "trials", "on", "--json"); code != 1 {
		t.Fatalf("--json without --yes: exit %d, want 1", code)
	}
	if f, _ := config.LoadAutopilot(apDir(h)); f.Policy != config.AutopilotPolicy {
		t.Fatal("--json turned trials on without --yes")
	}
	// The first agreement does not cover trials: setup's --autopilot and --yes never reach them.
	h2, _ := setupHarness(t, "")
	h2.run("setup", "--yes", "--autopilot")
	if f, _ := config.LoadAutopilot(apDir(h2)); f.Policy == config.AutopilotPolicyV2 {
		t.Fatal("setup turned on trials")
	}
}

func TestTrialsOnListsTheCandidatesInShadowAndOffRemovesThem(t *testing.T) {
	h := newHarness(t, nil)
	h.run("autopilot", "on", "--yes")
	if code := h.run("autopilot", "trials", "on", "--yes"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	f, _ := config.LoadAutopilot(apDir(h))
	if f.Policy != config.AutopilotPolicyV2 || f.TrialsConsentedAt == 0 || !f.Active() {
		t.Fatalf("after trials on: %+v", f)
	}
	for _, n := range []string{"hook_repeat_read", "hook_quiet_commands"} {
		if f.Entries[n].State != config.AutopilotShadow {
			t.Errorf("%s starts %q, want shadow (nothing changes yet)", n, f.Entries[n].State)
		}
	}
	cfg, err := config.Load(apDir(h), h.app.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.OptimizerState("hook_repeat_read"); got != config.StateShadow {
		t.Errorf("a candidate must only estimate: %q", got)
	}
	// The policy Advance runs is the one the user agreed to.
	if autopilot.CurrentPolicy(apDir(h)).Name != config.AutopilotPolicyV2 {
		t.Error("Advance would still run the v1 policy")
	}

	// Pretend one is on trial, then withdraw: the trial and its entry go, new sessions are not split.
	f.Entries["hook_repeat_read"] = config.AutopilotEntry{State: config.AutopilotProbation, SinceMS: time.Now().UnixMilli()}
	if err := config.SaveAutopilot(apDir(h), f); err != nil {
		t.Fatal(err)
	}
	h.out.Reset()
	if code := h.run("autopilot", "trials", "off"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	f, _ = config.LoadAutopilot(apDir(h))
	if f.Policy != config.AutopilotPolicy || f.TrialsConsentedAt != 0 {
		t.Errorf("after trials off: %+v", f)
	}
	for _, n := range []string{"hook_repeat_read", "hook_quiet_commands"} {
		if _, there := f.Entries[n]; there {
			t.Errorf("%s still has an entry after trials were withdrawn", n)
		}
	}
	if len(f.Trials()) != 0 {
		t.Errorf("trials still listed: %v", f.Trials())
	}
	if len(f.Entries) != 2 {
		t.Errorf("what Autopilot manages under v1 must be untouched: %v", f.Names())
	}
	if !strings.Contains(h.out.String(), "Trials are off") {
		t.Errorf("output: %s", h.out)
	}
}

// Withdrawing Autopilot altogether withdraws trials, and agreeing to Autopilot again is a v1 agreement.
func TestAutopilotOffEndsTrialsAndOnStartsOverAtV1(t *testing.T) {
	h := newHarness(t, nil)
	h.run("autopilot", "on", "--yes")
	h.run("autopilot", "trials", "on", "--yes")
	h.run("autopilot", "off")
	h.run("autopilot", "on", "--yes")
	f, _ := config.LoadAutopilot(apDir(h))
	if f.Policy != config.AutopilotPolicy || len(f.Trials()) != 0 {
		t.Errorf("policy %q after off then on", f.Policy)
	}
	if _, there := f.Entries["hook_repeat_read"]; there {
		t.Error("a trial candidate survived Autopilot being switched off")
	}
}

func TestStatusSaysWhetherTrialsAreOnAndWhatOneNeeds(t *testing.T) {
	h := newHarness(t, nil)
	h.run("autopilot", "on", "--yes")
	h.out.Reset()
	h.run("autopilot")
	if !strings.Contains(h.out.String(), "Trials") || !strings.Contains(h.out.String(), "are off") {
		t.Errorf("status with trials off:\n%s", h.out)
	}
	h.run("autopilot", "trials", "on", "--yes")
	h.out.Reset()
	h.run("autopilot")
	if !strings.Contains(h.out.String(), "Trials are on") || !strings.Contains(h.out.String(), "20 sessions on each side") || !strings.Contains(h.out.String(), "not yet validated") {
		t.Errorf("status with trials on:\n%s", h.out)
	}
	h.out.Reset()
	h.run("autopilot", "--json")
	var st struct {
		Trials    bool   `json:"trials"`
		TrialRule string `json:"trial_rule"`
	}
	if err := json.Unmarshal(h.out.Bytes(), &st); err != nil || !st.Trials || st.TrialRule == "" {
		t.Errorf("--json: %v %+v", err, st)
	}
}

func TestAnOptimizerOnTrialIsShownAsOnTrial(t *testing.T) {
	h := newHarness(t, nil)
	h.run("autopilot", "on", "--yes")
	h.run("autopilot", "trials", "on", "--yes")
	f, _ := config.LoadAutopilot(apDir(h))
	f.Entries["hook_repeat_read"] = config.AutopilotEntry{State: config.AutopilotProbation, SinceMS: time.Now().UnixMilli(), Reason: "trial running: 3 sessions with it on and 4 with it off so far, each side needs 20"}
	if err := config.SaveAutopilot(apDir(h), f); err != nil {
		t.Fatal(err)
	}
	h.out.Reset()
	h.run("autopilot")
	if !strings.Contains(h.out.String(), "on trial") || !strings.Contains(h.out.String(), "3 sessions with it on and 4 with it off") {
		t.Errorf("the status must show how the trial is going:\n%s", h.out)
	}
}

func TestTrialsRejectAnUnknownSubAction(t *testing.T) {
	h := newHarness(t, nil)
	h.run("autopilot", "on", "--yes")
	if code := h.run("autopilot", "trials", "maybe"); code != 1 || !strings.Contains(h.err.String(), "trials on") {
		t.Errorf("exit %d, stderr %q", code, h.err)
	}
	h = newHarness(t, nil)
	if code := h.run("autopilot", "on", "now"); code != 1 {
		t.Errorf("a second word after on: exit %d", code)
	}
}
