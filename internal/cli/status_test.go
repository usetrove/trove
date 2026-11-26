package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/autopilot"
	"github.com/usetrove/trove/internal/config"
)

// The words the user agrees to name every item the optimize policy names.
func TestOptimizeConsentNamesEveryItem(t *testing.T) {
	p := autopilot.PolicyOptimize()
	for _, n := range p.Names() {
		if optimizeItems[n] == "" {
			t.Errorf("%s has no line in the consent text", n)
		}
	}
	txt := optimizeConsent(p)
	for _, n := range p.Names() {
		if !strings.Contains(txt, optimizeItems[n]) {
			t.Errorf("the consent text leaves out %s", n)
		}
	}
	for _, want := range []string{"On from your next session", "never change what is sent", "Watched in shadow first", "benchmark verdict",
		"Shadow only", "subagentPromptCacheTtl", "rolled back on its own", "plan limit", "API prices", "config.toml", "trove autopilot off"} {
		if !strings.Contains(txt, want) {
			t.Errorf("the consent text lacks %q:\n%s", want, txt)
		}
	}
}

func TestStatusShowsThePolicy(t *testing.T) {
	h := newHarness(t, nil)
	if code := h.run("status"); code != 0 || !strings.Contains(h.out.String(), "optimize policy is off") {
		t.Fatalf("before agreeing: exit %d: %s%s", code, h.out, h.err)
	}
	h.run("autopilot", "on", "--yes")
	h.out.Reset()
	if code := h.run("status"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	for _, want := range []string{"Optimize policy", "hook_cold_resume", "on from the optimize policy", "Shadow only", "hook_write_to_edit", "Nothing rolled back"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("status lacks %q:\n%s", want, h.out)
		}
	}
	h.out.Reset()
	if code := h.run("status", "--json"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	var st troveStatusJSON
	if err := json.Unmarshal(h.out.Bytes(), &st); err != nil || st.View == nil || st.Policy != config.AutopilotPolicyOptimize {
		t.Fatalf("%v %s", err, h.out)
	}
	on := 0
	for _, it := range st.View.Items {
		if it.Group == autopilot.GroupDirect && it.State == config.AutopilotOn {
			on++
		}
	}
	if on != 3 {
		t.Errorf("%d guards on, want 3: %s", on, h.out)
	}
	// The report carries the same view.
	h.out.Reset()
	if code := h.run("report", "--no-history"); code != 0 || !strings.Contains(h.out.String(), "Optimize policy") {
		t.Errorf("report lacks the policy view: exit %d\n%s", code, h.out)
	}
}

// Setup asks the one question in plain words, and --autopilot agrees to the optimize policy.
func TestSetupOffersTheOptimizePolicy(t *testing.T) {
	h, _ := setupHarness(t, "")
	if code := h.run("setup"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if !strings.Contains(h.out.String(), "Turn on the optimize policy?") || !strings.Contains(h.out.String(), "Optimize policy not turned on") {
		t.Errorf("setup did not ask:\n%s", h.out)
	}
	h, _ = setupHarness(t, "")
	if code := h.run("setup", "--yes", "--autopilot"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	f, _ := config.LoadAutopilot(apDir(h))
	if !f.Active() || !f.IsOptimize() || f.Entries[config.OptimizerHookColdResume].State != config.AutopilotOn {
		t.Errorf("after --autopilot: %+v", f)
	}
}

// Under the optimize policy, trials are still their own yes, keep the policy, and a second `trials on` is a no-op.
func TestTrialsUnderTheOptimizePolicy(t *testing.T) {
	h := newHarness(t, nil)
	h.run("autopilot", "on", "--yes")
	if code := h.run("autopilot", "trials", "on", "--yes"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	f, _ := config.LoadAutopilot(apDir(h))
	if !f.IsOptimize() || !f.HasTrials() {
		t.Fatalf("policy %s, trials %v", f.Policy, f.HasTrials())
	}
	h.out.Reset()
	if code := h.run("autopilot", "trials", "on"); code != 0 || !strings.Contains(h.out.String(), "Trials are already on") {
		t.Errorf("second trials on: exit %d: %s", code, h.out)
	}
	h.run("autopilot", "trials", "off")
	f, _ = config.LoadAutopilot(apDir(h))
	if !f.IsOptimize() || f.HasTrials() {
		t.Errorf("after trials off: policy %s, trials %v", f.Policy, f.HasTrials())
	}
}
