package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/config"
)

// The settings policy is its own yes: it needs Autopilot on, a prompt (or --yes), and it writes nothing
// outside ~/.trove when agreed to.
func TestSettingsPolicyNeedsItsOwnYes(t *testing.T) {
	h := newHarness(t, nil)
	if code := h.run("autopilot", "settings", "on", "--yes"); code != 1 || !strings.Contains(h.err.String(), "trove autopilot on") {
		t.Fatalf("settings while Autopilot is off: exit %d, stderr %q", code, h.err)
	}
	onLossless(t, h)
	h.out.Reset()
	if code := h.run("autopilot", "settings", "on"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	for _, want := range []string{"subagentPromptCacheTtl", "never changes a value you set", "no other agent session is open", "14 days", "trove autopilot settings off", "Nothing was changed"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("the consent or the refusal lacks %q:\n%s", want, h.out)
		}
	}
	if f, _ := config.LoadAutopilot(apDir(h)); f.SettingsPolicy != "" {
		t.Fatal("agreed with nobody saying yes")
	}
	h.out.Reset()
	if code := h.run("autopilot", "settings", "on", "--yes", "--json"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	var st autopilotStatusJSON
	if err := json.Unmarshal(h.out.Bytes(), &st); err != nil {
		t.Fatalf("%v: %s", err, h.out)
	}
	if st.SettingsPolicy != config.AutopilotSettingsPolicy || len(st.Settings) != 1 || st.Settings[0].State != config.AutopilotShadow {
		t.Fatalf("status %+v", st)
	}
	h.out.Reset()
	if code := h.run("autopilot", "settings", "off"); code != 0 || !strings.Contains(h.out.String(), "settings policy is off") {
		t.Fatalf("exit %d: %s", code, h.out)
	}
	if f, _ := config.LoadAutopilot(apDir(h)); f.SettingsActive() {
		t.Fatal("still on after off")
	}
}
