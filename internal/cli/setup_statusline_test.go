package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupHarness returns a harness whose home holds a fake Claude Code install.
func setupHarness(t *testing.T, input string) (*harness, string) {
	t.Helper()
	h := newHarness(t, nil, "claude")
	settings, _ := agentHome(t, h)
	h.app.HomeDir = func() (string, error) { return filepath.Dir(filepath.Dir(settings)), nil }
	h.app.Executable = func() (string, error) { return "/opt/trove bin/trove", nil }
	if input != "" {
		h.app.In = strings.NewReader(input)
	}
	return h, settings
}

func TestSetupOffersStatusLineAsASeparateStepAndUndoRemovesBoth(t *testing.T) {
	h, settings := setupHarness(t, "y\ny\n")
	if code := h.run("setup", "--gateway"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	got, _ := os.ReadFile(settings)
	var doc struct {
		Env        map[string]string `json:"env"`
		StatusLine struct {
			Type    string `json:"type"`
			Command string `json:"command"`
		} `json:"statusLine"`
	}
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("%v: %s", err, got)
	}
	// The harness chose a data directory, so the command carries it.
	wantCmd := `'/opt/trove bin/trove' status-line --format claude --data-dir ` + shellQuote(h.app.Getenv("TROVE_DATA_DIR"))
	if doc.StatusLine.Type != "command" || doc.StatusLine.Command != wantCmd {
		t.Errorf("statusLine = %+v, want command %q", doc.StatusLine, wantCmd)
	}
	if doc.Env["ANTHROPIC_BASE_URL"] == "" {
		t.Errorf("gateway edit missing: %s", got)
	}
	out := h.out.String()
	for _, want := range []string{"Add Trove to Claude Code's status line?", "Added Trove to Claude Code's status line", "Applied 3 change(s)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	h2 := newHarness(t, map[string]string{"TROVE_DATA_DIR": h.app.Getenv("TROVE_DATA_DIR")}, "claude")
	if code := h2.run("undo"); code != 0 {
		t.Fatalf("undo exit %d: %s", code, h2.err)
	}
	if got, _ := os.ReadFile(settings); string(got) != claudeSettings {
		t.Errorf("undo did not restore the original settings:\n%s", got)
	}
}

func TestSetupStatusLineNeedsItsOwnConsent(t *testing.T) {
	// "y" for the gateway, "n" for the status line.
	h, settings := setupHarness(t, "y\nn\n")
	h.run("setup", "--gateway")
	got, _ := os.ReadFile(settings)
	if strings.Contains(string(got), "statusLine") || !strings.Contains(string(got), "ANTHROPIC_BASE_URL") {
		t.Errorf("only the gateway edit should be applied:\n%s", got)
	}
	if !strings.Contains(h.out.String(), "trove setup --status-line") {
		t.Errorf("no hint for adding it later:\n%s", h.out)
	}

	// --yes agrees to the gateway change only.
	h, settings = setupHarness(t, "")
	h.run("setup", "--gateway", "--yes")
	if got, _ := os.ReadFile(settings); strings.Contains(string(got), "statusLine") {
		t.Errorf("--yes alone must not add the status line:\n%s", got)
	}

	// --status-line is the explicit opt-in for scripts, and works when the
	// gateway is already set up.
	h, settings = setupHarness(t, "")
	h.run("setup", "--gateway", "--yes", "--status-line")
	if got, _ := os.ReadFile(settings); !strings.Contains(string(got), "statusLine") {
		t.Errorf("--status-line did not add it:\n%s", got)
	}
	h.out.Reset()
	if code := h.run("setup", "--gateway", "--status-line"); code != 0 || !strings.Contains(h.out.String(), "already shows Trove") {
		t.Errorf("second run: exit %d\n%s", code, h.out)
	}
}

func TestSetupNeverReplacesAnExistingStatusLine(t *testing.T) {
	h, settings := setupHarness(t, "y\ny\n")
	mine := "{\n  \"statusLine\": {\"type\": \"command\", \"command\": \"~/mine.sh\"}\n}\n"
	if err := os.WriteFile(settings, []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := h.run("setup", "--gateway"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	got, _ := os.ReadFile(settings)
	if !strings.Contains(string(got), "~/mine.sh") || strings.Contains(string(got), "trove") && strings.Contains(string(got), "status-line") {
		t.Errorf("your status line must survive untouched:\n%s", got)
	}
	if !strings.Contains(h.out.String(), "already have a Claude Code status line") {
		t.Errorf("no explanation:\n%s", h.out)
	}
}

func TestSetupEndsWithAnExplanation(t *testing.T) {
	h, _ := setupHarness(t, "y\nn\n")
	h.run("setup", "--gateway")
	out := h.out.String()
	i := strings.Index(out, "What happens next:")
	if i < 0 {
		t.Fatalf("no closing explanation:\n%s", out)
	}
	tail := out[i:]
	for _, want := range []string{"never stores your prompts", "Trove sends nothing anywhere", "observe-only", "7 days and 20 sessions", "run `trove`"} {
		if !strings.Contains(tail, want) {
			t.Errorf("explanation lacks %q:\n%s", want, tail)
		}
	}

	h, _ = setupHarness(t, "")
	h.run("setup", "--gateway", "--json", "--yes")
	var rep setupReport
	if err := json.Unmarshal(h.out.Bytes(), &rep); err != nil || len(rep.Explain) == 0 {
		t.Errorf("--json lacks the explanation: %v %q", err, h.out)
	}
}
