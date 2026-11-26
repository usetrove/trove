package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/autopilot"
	"github.com/usetrove/trove/internal/config"
)

// Every trial candidate has a plain-English line in the words the user agrees to, so the consent can never list
// fewer changes than the policy makes.
func TestTrialsConsentNamesEveryCandidateAndQuotesThePolicysNumbers(t *testing.T) {
	for _, p := range []autopilot.Policy{autopilot.PolicyV2(), autopilot.PolicyV3()} {
		text := trialsConsent(p)
		for _, n := range p.Probation {
			line, ok := trialCandidates[n]
			if !ok || line == "" {
				t.Errorf("%s: no consent line for %s", p.Name, n)
				continue
			}
			if !strings.Contains(text, line) {
				t.Errorf("%s: the consent does not say what %s does", p.Name, n)
			}
		}
		if !strings.Contains(text, "at least 20 sessions on each side") || !strings.Contains(text, "28 days") {
			t.Errorf("%s: the consent must quote the policy's own numbers:\n%s", p.Name, text)
		}
		if strings.Contains(text, "%!") || strings.Contains(text, "%d") || strings.Contains(text, "%s") {
			t.Errorf("%s: unfilled placeholder in the consent:\n%s", p.Name, text)
		}
	}
	if len(trialCandidates) != len(autopilot.PolicyV3().Probation) {
		t.Errorf("trialCandidates has %d lines for %d candidates; remove or add one", len(trialCandidates), len(autopilot.PolicyV3().Probation))
	}
}

// A user who agreed to v2 keeps exactly v2 until they agree again, and the upgrade is a new yes that says what it adds.
func TestMovingFromV2ToV3NeedsANewYesAndKeepsWhatWasAlreadyKept(t *testing.T) {
	h := newHarness(t, nil)
	onLossless(t, h)
	// Put the file in the state a v0.3.0 user is in: v2 consent, one trial candidate already kept.
	if _, err := autopilot.EnableTrials(apDir(h), autopilot.PolicyV2(), time.Now()); err != nil {
		t.Fatal(err)
	}
	f, _ := config.LoadAutopilot(apDir(h))
	f.Entries["hook_repeat_read"] = config.AutopilotEntry{State: config.AutopilotOn, Attempts: 1}
	if err := config.SaveAutopilot(apDir(h), f); err != nil {
		t.Fatal(err)
	}
	before := f.TrialsConsentedAt

	// Nobody can answer a prompt here, so nothing changes, and the text says what is being added.
	h.reset()
	if code := h.run("autopilot", "trials", "on"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	f, _ = config.LoadAutopilot(apDir(h))
	if f.Policy != config.AutopilotPolicyV2 || f.TrialsConsentedAt != before {
		t.Fatalf("v2 was upgraded without a yes: %+v", f)
	}
	for _, want := range []string{"agreed to trials of 2 changes before", "adds 3 more", "needs a new yes", "Nothing was changed"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("the upgrade prompt lacks %q:\n%s", want, h.out)
		}
	}
	h.reset()
	if code := h.run("autopilot", "trials", "on", "--json"); code != 1 {
		t.Fatalf("--json without --yes: exit %d, want 1", code)
	}

	// With a yes: v3, the old state kept, the new candidates in shadow.
	h.reset()
	if code := h.run("autopilot", "trials", "on", "--yes"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	f, _ = config.LoadAutopilot(apDir(h))
	if f.Policy != config.AutopilotPolicyV3 || f.TrialsConsentedAt == before {
		t.Fatalf("after agreeing: %+v", f)
	}
	if f.Entries["hook_repeat_read"].State != config.AutopilotOn {
		t.Error("what v2 kept must stay kept")
	}
	for _, n := range []string{"output_trimmer", "hook_large_output", "hook_read_limit"} {
		if f.Entries[n].State != config.AutopilotShadow {
			t.Errorf("%s: %q, want shadow", n, f.Entries[n].State)
		}
	}
	if !strings.Contains(h.out.String(), "now include the new candidates") {
		t.Errorf("the result must say what changed:\n%s", h.out)
	}
	// Running it again is a no-op.
	h.reset()
	h.run("autopilot", "trials", "on", "--yes")
	if !strings.Contains(h.out.String(), "already on") {
		t.Errorf("a second yes: %s", h.out)
	}
	// The status shows trials on, and --json reports the policy.
	h.reset()
	h.run("autopilot", "--json")
	if !strings.Contains(h.out.String(), `"policy": "lossless-v3"`) || !strings.Contains(h.out.String(), `"trials": true`) {
		t.Errorf("status: %s", h.out)
	}
}
