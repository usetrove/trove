package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/store"
)

func putTool(t *testing.T, h *harness, name, state string) {
	t.Helper()
	db, err := store.Open(apDir(h))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.ExternalTools().Put(context.Background(), store.ExternalTool{Name: name, Version: "1", License: "MIT", State: state, InstalledAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

// A rule-set trial is T2, so it starts only on an explicit yes, only for an installed rule set that is off, and
// never beside another output-style rule set; --stop ends it.
func TestAutopilotRuleTrialCommand(t *testing.T) {
	h := newHarness(t, nil)
	if code := h.run("autopilot", "on", "--yes"); code != 0 {
		t.Fatalf("on: %s", h.err)
	}
	trials := func() []string { f, _ := config.LoadAutopilot(apDir(h)); return f.RuleTrials() }

	if code := h.run("autopilot", "trial", "caveman", "--yes"); code == 0 || !strings.Contains(h.err.String(), "not installed") {
		t.Fatalf("not installed: exit %d: %s", code, h.err)
	}
	putTool(t, h, "caveman", "off")
	putTool(t, h, "ponytail", "on")
	if code := h.run("autopilot", "trial", "caveman", "--yes"); code == 0 || !strings.Contains(h.err.String(), "only one output-style rule set") {
		t.Fatalf("beside ponytail: exit %d: %s", code, h.err)
	}
	putTool(t, h, "ponytail", "off")

	// No terminal: the consent prompt cannot be answered, so nothing starts; --json refuses without --yes.
	if code := h.run("autopilot", "trial", "caveman"); code != 0 || len(trials()) != 0 || !strings.Contains(h.out.String(), "half of new sessions") {
		t.Fatalf("without a yes: exit %d, trials %v:\n%s", code, trials(), h.out)
	}
	if code := h.run("autopilot", "trial", "caveman", "--json"); code != 1 || len(trials()) != 0 {
		t.Fatalf("--json without --yes: exit %d, trials %v", code, trials())
	}

	if code := h.run("autopilot", "trial", "caveman", "--yes", "--json"); code != 0 {
		t.Fatalf("start: %s", h.err)
	}
	if got := trials(); len(got) != 1 || got[0] != "ext_caveman" {
		t.Fatalf("running trials: %v", got)
	}
	if !strings.Contains(h.out.String(), `"ext_caveman"`) || !strings.Contains(h.out.String(), `"probation"`) {
		t.Errorf("status JSON should list the trial:\n%s", h.out)
	}
	if code := h.run("autopilot", "trial", "caveman", "--stop"); code != 0 || len(trials()) != 0 {
		t.Fatalf("stop: exit %d, trials %v: %s", code, trials(), h.err)
	}
	if code := h.run("autopilot", "trial", "rtk", "--yes"); code == 0 || !strings.Contains(h.err.String(), "not a rule set") {
		t.Fatalf("rtk: exit %d: %s", code, h.err)
	}
}
