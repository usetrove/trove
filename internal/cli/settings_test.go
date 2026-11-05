package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/fixer"
)

// fakeVersions makes the version probe answer without starting a program.
func fakeVersions(t *testing.T, claude, codex string) {
	t.Helper()
	runVersionCmd = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		switch name {
		case "claude":
			return []byte(claude), nil
		case "codex":
			return []byte(codex), nil
		}
		return nil, os.ErrNotExist
	}
	t.Cleanup(func() { runVersionCmd = nil })
}

func settingsHarness(t *testing.T) (*harness, string, string) {
	t.Helper()
	fakeVersions(t, "2.1.314 (Claude Code)\n", "codex-cli 0.134.0\n")
	h := newHarness(t, nil)
	home, _ := h.app.HomeDir()
	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	return h, proj, filepath.Join(home, ".codex", "trove-quick-fix.config.toml")
}

func TestSettingsListShowsVersionsAndWhatTroveWillWrite(t *testing.T) {
	h, _, _ := settingsHarness(t)
	if code := h.run("settings", "--json"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	var rep settingsListReport
	if err := json.Unmarshal(h.out.Bytes(), &rep); err != nil {
		t.Fatalf("%v: %s", err, h.out)
	}
	if len(rep.Harnesses) != 2 || len(rep.Tasks) != 4 {
		t.Fatalf("%+v", rep)
	}
	for _, hl := range rep.Harnesses {
		if hl.Installed == "" || hl.DocsChecked == "" {
			t.Errorf("%s: version and docs date must be shown: %+v", hl.Harness, hl)
		}
		for _, s := range hl.Settings {
			if (s.Action == "applies") != s.Applyable {
				t.Errorf("%s: action %q disagrees with applyable=%v", s.Key, s.Action, s.Applyable)
			}
		}
	}
	h.out.Reset()
	h.run("settings")
	for _, want := range []string{"Claude Code (installed: 2.1.314", "suggestion only", "Not offered: custom compaction instructions"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, h.out)
		}
	}
}

func TestSettingsPlanChangesNothing(t *testing.T) {
	h, proj, codexProfile := settingsHarness(t)
	if code := h.run("settings", "plan", "--task", "quick-fix", "--project", proj); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	out := h.out.String()
	for _, want := range []string{`Profile "quick-fix"`, "model_reasoning_summary", "codex --profile trove-quick-fix", "Left out: outputStyle", "Nothing was changed"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if _, err := os.Stat(codexProfile); err == nil {
		t.Fatal("plan must not write anything")
	}
	// With the explicit opt-in the Claude value is planned too.
	h.out.Reset()
	h.run("settings", "plan", "--task", "quick-fix", "--project", proj, "--allow-unproven")
	if !strings.Contains(h.out.String(), `"outputStyle": "Concise"`) {
		t.Errorf("--allow-unproven should plan outputStyle:\n%s", h.out)
	}
}

func TestSettingsApplyNeedsApprovalThenUndoes(t *testing.T) {
	h, proj, codexProfile := settingsHarness(t)

	// No terminal and no --yes: nothing happens.
	h.out.Reset()
	h.run("settings", "apply", "--task", "quick-fix", "--project", proj)
	if _, err := os.Stat(codexProfile); err == nil || !strings.Contains(h.out.String(), "No changes made") {
		t.Fatalf("no approval, no change: %s", h.out)
	}
	// --json without --yes also changes nothing.
	h.out.Reset()
	h.run("settings", "apply", "--task", "quick-fix", "--project", proj, "--json")
	if _, err := os.Stat(codexProfile); err == nil {
		t.Fatal("--json alone must not apply")
	}

	h.out.Reset()
	if code := h.run("settings", "apply", "--task", "quick-fix", "--project", proj, "--yes", "--json"); code != 0 {
		t.Fatalf("apply: %d %s", code, h.err)
	}
	var rep fixApplyReport
	if err := json.Unmarshal(h.out.Bytes(), &rep); err != nil || rep.Applied != 1 {
		t.Fatalf("%v %+v", err, rep)
	}
	b, err := os.ReadFile(codexProfile)
	if err != nil || !strings.Contains(string(b), `model_reasoning_summary = "none"`) {
		t.Fatalf("profile not written: %v %q", err, b)
	}
	// It is now recorded for measurement, with names only.
	h.out.Reset()
	h.run("settings", "status")
	if !strings.Contains(h.out.String(), "settings-") || !strings.Contains(h.out.String(), "Codex profile") {
		t.Errorf("status:\n%s", h.out)
	}

	id := rep.Results[0].ID
	h.out.Reset()
	if code := h.run("undo", id); code != 0 {
		t.Fatalf("undo: %d %s %s", code, h.out, h.err)
	}
	if _, err := os.Stat(codexProfile); err == nil {
		t.Error("undo must remove the profile Trove created")
	}
	h.out.Reset()
	h.run("settings", "status")
	if !strings.Contains(h.out.String(), "No settings profile is in place") {
		t.Errorf("an undone profile must leave the status:\n%s", h.out)
	}
	_ = fixer.SettingsProfile
}

func TestSettingsMissingOrUnknownTask(t *testing.T) {
	h, proj, _ := settingsHarness(t)
	if code := h.run("settings", "plan", "--project", proj); code == 0 || !strings.Contains(h.err.String(), "--task") {
		t.Errorf("a missing task must say what to do: %d %s", code, h.err)
	}
	h.err.Reset()
	if code := h.run("settings", "plan", "--task", "nope", "--project", proj); code == 0 || !strings.Contains(h.err.String(), "quick-fix") {
		t.Errorf("an unknown task must list the valid ones: %d %s", code, h.err)
	}
	h.err.Reset()
	if code := h.run("settings", "plan", "--task", "quick-fix", "--yes", "--project", proj); code == 0 {
		t.Error("--yes on plan must be refused")
	}
}

func TestSettingsUnknownVersionChangesNothing(t *testing.T) {
	h, proj, codexProfile := settingsHarness(t)
	fakeVersions(t, "", "")
	h.out.Reset()
	h.run("settings", "apply", "--task", "quick-fix", "--project", proj, "--yes")
	if _, err := os.Stat(codexProfile); err == nil {
		t.Fatal("an unreadable harness version must not produce a write")
	}
	if !strings.Contains(h.out.String(), "Nothing to apply") {
		t.Errorf("should say nothing was applied:\n%s", h.out)
	}
}
