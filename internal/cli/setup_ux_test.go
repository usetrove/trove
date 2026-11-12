package cli

import (
	"strings"
	"testing"
)

// Setup without --gateway reads as an optional extra, not a failure.
func TestSetupWithoutGatewayCallsItOptionalNotAFailure(t *testing.T) {
	h, claudePath := setupWith(t, nil, `{"theme":"dark"}`)
	if code := h.run("setup", "--yes"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	out := h.out.String()
	if strings.Contains(out, "cannot set up") {
		t.Errorf("an optional gateway must not read as a failure:\n%s", out)
	}
	if !strings.Contains(out, "Optional: route this agent through the Trove gateway with `trove setup --gateway`") {
		t.Errorf("the optional gateway should be named with its command:\n%s", out)
	}
	if _, set := readClaudeEnv(t, claudePath)["ANTHROPIC_BASE_URL"]; set {
		t.Error("nothing should change without --gateway")
	}
}

// The verify, turn-off and "what happens next" text appears only when the gateway was set up.
func TestSetupShowsGatewayGuidanceOnlyWhenTheGatewayIsSetUp(t *testing.T) {
	const guidance = "To verify it works:"
	const next = "What happens next:"

	h, _ := setupWith(t, nil, `{"theme":"dark"}`)
	if code := h.run("setup", "--yes"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	for _, s := range []string{guidance, next, "Who the gateway is for"} {
		if strings.Contains(h.out.String(), s) {
			t.Errorf("without --gateway, %q must not be shown:\n%s", s, h.out)
		}
	}

	g, _ := setupWith(t, nil, `{"theme":"dark"}`)
	if code := g.run("setup", "--gateway", "--yes"); code != 0 {
		t.Fatalf("exit %d: %s", code, g.err)
	}
	for _, s := range []string{guidance, next} {
		if !strings.Contains(g.out.String(), s) {
			t.Errorf("after --gateway, %q should be shown", s)
		}
	}
}

// The applied result comes before the optional status-line question, so the prompt is not read first.
func TestSetupReportsAppliedBeforeAskingAboutTheStatusLine(t *testing.T) {
	h, _ := setupWith(t, nil, `{"theme":"dark"}`)
	h.app.In = strings.NewReader("n\n")
	if code := h.run("setup", "--gateway", "--yes"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	out := h.out.String()
	applied := strings.Index(out, "Applied ")
	prompt := strings.Index(out, "Add Trove to Claude Code's status line?")
	if applied < 0 || prompt < 0 || applied > prompt {
		t.Errorf("want the applied result before the status-line question (applied=%d, prompt=%d):\n%s", applied, prompt, out)
	}
}

// With nothing to change, the summary comes first and does not claim a problem when there is none.
func TestSetupNothingToChangeSaysSoPlainly(t *testing.T) {
	h, _ := setupWith(t, nil, `{"theme":"dark"}`)
	if code := h.run("setup", "--yes"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	out := h.out.String()
	if !strings.Contains(out, "Nothing to change.") {
		t.Errorf("want the plain summary \"Nothing to change.\":\n%s", out)
	}
	if strings.Contains(out, "Nothing to change: .") {
		t.Errorf("an empty problem list leaked into the summary:\n%s", out)
	}
}

// Doctor never says "No problems found" when it printed warnings.
func TestDoctorDoesNotSayNoProblemsWhenThereAreWarnings(t *testing.T) {
	r := doctorReport{OK: true, Checks: []check{
		{"data directory", statusOK, "private", ""},
		{"database", statusWarn, "something to look at", "do this"},
	}}
	h := newHarness(t, nil)
	text := r.text(h.app)
	if strings.Contains(text, "No problems found") {
		t.Errorf("warnings were printed but the summary says no problems:\n%s", text)
	}
	if !strings.Contains(text, "1 warning(s)") {
		t.Errorf("the summary should count the warnings:\n%s", text)
	}
}

// On a fresh install the missing data folder and database are expected, so they are information, and
// doctor reports no problems.
func TestDoctorFreshInstallHasNoWarnings(t *testing.T) {
	h := newHarness(t, nil, "sh") // sh is present; without it doctor warns, which TestDoctorWarnsWhenShIsMissing covers
	dir := t.TempDir() + "/fresh"
	if code := h.run("doctor", "--data-dir", dir); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if strings.Contains(h.out.String(), "[warn]") {
		t.Errorf("a fresh install should have no warnings:\n%s", h.out)
	}
	if !strings.Contains(h.out.String(), "No problems found.") {
		t.Errorf("a fresh install with nothing wrong should say so:\n%s", h.out)
	}
}

// The uninstall plan is printed once, then asked once, and a "no" changes nothing.
func TestUninstallPrintsThePlanOnceAndAsksOnce(t *testing.T) {
	h, home, _, _ := installEverything(t)
	installed := snapshot(t, home)
	h.app.In = strings.NewReader("n\n")
	if code := h.run("uninstall", "--purge-data"); code != 0 {
		t.Fatalf("a declined uninstall should exit 0: %d %s", code, h.err)
	}
	if n := strings.Count(h.out.String(), "Uninstall will:"); n != 1 {
		t.Errorf("the plan was printed %d times, want once:\n%s", n, h.out)
	}
	if strings.Count(h.out.String(), "Go ahead?") != 1 {
		t.Errorf("the question should be asked once:\n%s", h.out)
	}
	for p, want := range snapshot(t, home) {
		if installed[p] != want {
			t.Errorf("%s changed after a no", p)
		}
	}
}

// The restart reminder appears only when an agent's settings were actually changed back.
func TestUninstallRestartReminderOnlyWhenSomethingChanged(t *testing.T) {
	empty := newHarness(t, nil)
	if code := empty.run("uninstall", "--yes"); code != 0 {
		t.Fatalf("exit %d: %s", code, empty.err)
	}
	if strings.Contains(empty.out.String(), "Restart Claude Code") {
		t.Errorf("nothing changed, so there is nothing to restart for:\n%s", empty.out)
	}

	h, _, _, _ := installEverything(t)
	if code := h.run("uninstall", "--yes"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if !strings.Contains(h.out.String(), "Restart Claude Code") {
		t.Errorf("settings were changed back, so the reminder belongs here:\n%s", h.out)
	}
}
