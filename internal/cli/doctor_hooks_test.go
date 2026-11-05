package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/hooks"
)

func stateOf(m map[string]string) func(string) string {
	return func(n string) string {
		if s, ok := m[n]; ok {
			return s
		}
		return "off"
	}
}

func noAgent() *App {
	return &App{LookPath: func(string) (string, error) { return "", errors.New("not found") },
		HomeDir: func() (string, error) { return "", errors.New("no home") },
		Getenv:  func(string) string { return "" }}
}

func TestDoctorSaysWhyAHookRewriteIsHeldInShadow(t *testing.T) {
	dir := t.TempDir()
	a := noAgent()

	// Nothing turned on: just information.
	if c := hookRewritesCheck(a, dir, stateOf(nil)); c.Status != statusInfo {
		t.Fatalf("%+v", c)
	}

	// Turned on, never probed: a warning that names the optimizer, the tool and the fix.
	c := hookRewritesCheck(a, dir, stateOf(map[string]string{"hook_quiet_commands": "on"}))
	if c.Status != statusWarn || !strings.Contains(c.Detail, "hook_quiet_commands") || !strings.Contains(c.Detail, "never probed") ||
		!strings.Contains(c.Fix, "trove hooks probe --live") {
		t.Fatalf("%+v", c)
	}

	// Probed live and the agent ignored it: the evidence is shown.
	if err := hooks.SaveCapabilities(dir, hooks.Capabilities{Entries: []hooks.Capability{
		{Agent: "claude", Tool: "Bash", Method: "live", InputRewrite: hooks.CapNo, OutputRewrite: hooks.CapNo, Evidence: "the tool ran with its original input; updatedInput was ignored"}}}); err != nil {
		t.Fatal(err)
	}
	c = hookRewritesCheck(a, dir, stateOf(map[string]string{"hook_quiet_commands": "on"}))
	if c.Status != statusWarn || !strings.Contains(c.Detail, "updatedInput was ignored") {
		t.Fatalf("%+v", c)
	}

	// Probed live and it works: ok.
	if err := hooks.SaveCapabilities(dir, hooks.Capabilities{Entries: []hooks.Capability{
		{Agent: "claude", Tool: "Bash", Method: "live", InputRewrite: hooks.CapYes, OutputRewrite: hooks.CapYes}}}); err != nil {
		t.Fatal(err)
	}
	if c = hookRewritesCheck(a, dir, stateOf(map[string]string{"hook_quiet_commands": "on"})); c.Status != statusOK {
		t.Fatalf("%+v", c)
	}
}
