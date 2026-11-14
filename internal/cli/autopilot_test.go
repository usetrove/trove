package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/config"
)

func apDir(h *harness) string { return h.app.Getenv("TROVE_DATA_DIR") }

func TestAutopilotNeedsAnExplicitYes(t *testing.T) {
	// No terminal input: a prompt can never be answered, so nothing is turned on.
	h := newHarness(t, nil)
	if code := h.run("autopilot", "on"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if f, _ := config.LoadAutopilot(apDir(h)); f.Enabled {
		t.Fatal("Autopilot turned on without anyone saying yes")
	}
	if !strings.Contains(h.out.String(), "Nothing was changed") || !strings.Contains(h.out.String(), "Only lossless optimizers") {
		t.Errorf("the consent text or the refusal is missing:\n%s", h.out)
	}

	// --json cannot prompt either, so it refuses rather than guessing.
	h = newHarness(t, nil)
	if code := h.run("autopilot", "on", "--json"); code != 1 {
		t.Fatalf("--json without --yes: exit %d, want 1", code)
	}
	if f, _ := config.LoadAutopilot(apDir(h)); f.Enabled {
		t.Fatal("--json turned Autopilot on without --yes")
	}
}

func TestAutopilotOnPauseResumeOff(t *testing.T) {
	h := newHarness(t, nil)
	if code := h.run("autopilot", "on", "--yes"); code != 0 {
		t.Fatalf("on: exit %d: %s", code, h.err)
	}
	cfgNow := func() config.Config {
		c, err := config.Load(apDir(h), h.app.Getenv)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	if got := cfgNow().OptimizerState("noise_normalizer"); got != config.StateShadow {
		t.Errorf("after on: %q, want shadow", got)
	}
	steps := []struct {
		args []string
		want string
		text string
	}{
		{[]string{"autopilot", "pause"}, config.StateOff, "paused"},
		{[]string{"autopilot", "resume"}, config.StateShadow, "running again"},
		{[]string{"autopilot", "off"}, config.StateOff, "Autopilot is off"},
	}
	for _, s := range steps {
		h.out.Reset()
		if code := h.run(s.args...); code != 0 {
			t.Fatalf("%v: exit %d: %s", s.args, code, h.err)
		}
		if got := cfgNow().OptimizerState("noise_normalizer"); got != s.want {
			t.Errorf("%v: state %q, want %q", s.args, got, s.want)
		}
		if !strings.Contains(h.out.String(), s.text) {
			t.Errorf("%v: output lacks %q:\n%s", s.args, s.text, h.out)
		}
	}
}

func TestAutopilotStatusJSON(t *testing.T) {
	h := newHarness(t, nil)
	h.run("autopilot", "on", "--yes")
	h.out.Reset()
	if code := h.run("autopilot", "--json"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	var st struct {
		Enabled    bool   `json:"enabled"`
		Policy     string `json:"policy"`
		Thresholds string `json:"thresholds"`
		Optimizers []struct {
			Name, State string
		} `json:"optimizers"`
	}
	if err := json.Unmarshal(h.out.Bytes(), &st); err != nil {
		t.Fatalf("%v: %s", err, h.out)
	}
	if !st.Enabled || st.Policy != config.AutopilotPolicy || len(st.Optimizers) != 2 {
		t.Errorf("status %+v", st)
	}
	if !strings.Contains(st.Thresholds, "not yet validated") {
		t.Errorf("thresholds must say they are unvalidated placeholders: %q", st.Thresholds)
	}
}

func TestAutopilotRejectsUnknownActions(t *testing.T) {
	h := newHarness(t, nil)
	if code := h.run("autopilot", "bogus"); code != 1 || !strings.Contains(h.err.String(), "Use on, off, pause or resume") {
		t.Errorf("exit %d, stderr %q", code, h.err)
	}
	h = newHarness(t, nil)
	if code := h.run("autopilot", "pause"); code != 1 || !strings.Contains(h.err.String(), "trove autopilot on") {
		t.Errorf("pause while off: exit %d, stderr %q", code, h.err)
	}
}

func TestSetupAutopilotIsItsOwnConsent(t *testing.T) {
	// --yes alone must not turn Autopilot on, as with the status line.
	h, _ := setupHarness(t, "")
	if code := h.run("setup", "--yes"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if f, _ := config.LoadAutopilot(apDir(h)); f.Enabled {
		t.Fatal("--yes alone turned Autopilot on")
	}
	if !strings.Contains(h.out.String(), "Autopilot not turned on") {
		t.Errorf("setup did not offer Autopilot:\n%s", h.out)
	}

	h, _ = setupHarness(t, "")
	if code := h.run("setup", "--yes", "--autopilot"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if f, _ := config.LoadAutopilot(apDir(h)); !f.Active() {
		t.Fatal("--autopilot did not turn Autopilot on")
	}

	h, _ = setupHarness(t, "")
	h.run("setup", "--yes", "--autopilot", "--json")
	var rep struct {
		AutopilotOn bool `json:"autopilot_on"`
	}
	if err := json.Unmarshal(h.out.Bytes(), &rep); err != nil || !rep.AutopilotOn {
		t.Errorf("setup --json should report autopilot_on: %v %s", err, h.out)
	}
}

// `trove --help` shows a short list. Every command that is hidden from it must
// still exist, because the plugin, the hooks and CI call them by name.
func TestHelpShowsOnlyTheEverydayCommands(t *testing.T) {
	root := NewRoot(newHarness(t, nil).app)
	var visible, hidden []string
	for _, c := range root.Commands() {
		if c.Name() == "help" {
			continue
		}
		if c.Hidden {
			hidden = append(hidden, c.Name())
		} else {
			visible = append(visible, c.Name())
		}
	}
	want := []string{"advanced", "autopilot", "doctor", "fix", "report", "setup", "undo", "uninstall"}
	if strings.Join(visible, ",") != strings.Join(want, ",") {
		t.Errorf("visible commands %v, want %v", visible, want)
	}
	// Called by the plugin, the hooks, the status line, CI or scripts.
	for _, name := range []string{"hook", "hooks", "gateway", "mcp", "plugin", "status-line", "engine", "version", "optimize", "pricing", "contribute", "data"} {
		found := false
		for _, h := range hidden {
			found = found || h == name
		}
		if !found {
			t.Errorf("%q should still exist, hidden from the main help", name)
		}
	}
}

func TestAdvancedListsTheHiddenCommands(t *testing.T) {
	h := newHarness(t, nil)
	if code := h.run("advanced"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	for _, want := range []string{"gateway", "optimize", "contribute", "trove <command> --help"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("advanced output lacks %q:\n%s", want, h.out)
		}
	}
	if strings.Contains(h.out.String(), "autopilot ") || strings.Contains(h.out.String(), "  setup ") {
		t.Errorf("advanced lists an everyday command:\n%s", h.out)
	}
	h = newHarness(t, nil)
	h.run("advanced", "--json")
	var list []map[string]string
	if err := json.Unmarshal(h.out.Bytes(), &list); err != nil || len(list) < 10 {
		t.Errorf("--json: %v, %d entries", err, len(list))
	}
}

func TestAutopilotSuffixOnTheStatusLine(t *testing.T) {
	dir := t.TempDir()
	if got := autopilotSuffix(dir); got != "" {
		t.Errorf("no consent gave %q, want nothing", got)
	}
	f := config.AutopilotFile{Enabled: true, Policy: config.AutopilotPolicy, Entries: map[string]config.AutopilotEntry{
		"noise_normalizer": {State: config.AutopilotShadow},
	}}
	_ = config.SaveAutopilot(dir, f)
	if got := autopilotSuffix(dir); got != " · autopilot learning" {
		t.Errorf("shadow only gave %q", got)
	}
	f.Entries["noise_normalizer"] = config.AutopilotEntry{State: config.AutopilotOn}
	_ = config.SaveAutopilot(dir, f)
	if got := autopilotSuffix(dir); got != " · autopilot 1 on" {
		t.Errorf("one on gave %q", got)
	}
	f.Paused = true
	_ = config.SaveAutopilot(dir, f)
	if got := autopilotSuffix(dir); got != "" {
		t.Errorf("paused gave %q, want nothing", got)
	}
}
