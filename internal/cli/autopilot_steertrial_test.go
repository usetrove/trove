package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/hookcaps"
	"github.com/usetrove/trove/internal/hooks"
	"github.com/usetrove/trove/internal/store"
)

// The steer's trial starts only from shadow, after the live check, with enough routine prompts counted, and on an
// explicit yes (T2).
func TestAutopilotSteerTrialCommand(t *testing.T) {
	h := newHarness(t, nil)
	dir := apDir(h)
	if code := h.run("autopilot", "on", "--yes"); code != 0 {
		t.Fatalf("on: %s", h.err)
	}
	trials := func() []string { f, _ := config.LoadAutopilot(dir); return f.RuleTrials() }
	setState := func(s string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[optimizers]\nhook_routine_steer = \""+s+"\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	try := func(want string, args ...string) {
		t.Helper()
		h.reset()
		code := h.run(append([]string{"autopilot", "trial", "hook_routine_steer"}, args...)...)
		if code == 0 || !strings.Contains(h.err.String(), want) {
			t.Fatalf("want a refusal with %q, got exit %d: %s%s", want, code, h.out, h.err)
		}
		if len(trials()) != 0 {
			t.Fatalf("a refused trial started: %v", trials())
		}
	}

	try("run it in shadow first", "--yes")
	setState("on")
	try("on for every session", "--yes")
	setState("shadow")
	try("--prompt-context", "--yes")
	if err := hooks.SaveCapabilities(dir, hooks.Capabilities{V: hookcaps.Version, Entries: []hooks.Capability{
		{Agent: "claude", Tool: hookcaps.ToolPrompt, Context: hookcaps.Yes, Method: "session"}}}); err != nil {
		t.Fatal(err)
	}
	try("counted 0 routine prompts", "--yes")

	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if _, err := db.ExecContext(context.Background(), `INSERT INTO hook_events(ts, session_key, hook, kind) VALUES(?,?,?,?)`,
			time.Now().UnixMilli(), "c-x", "hook_routine_steer", "would_steer"); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()

	h.reset()
	if code := h.run("autopilot", "trial", "hook_routine_steer"); code != 0 || len(trials()) != 0 || !strings.Contains(h.out.String(), "Answer directly without deliberating.") {
		t.Fatalf("without a yes nothing starts, and the consent names the text: exit %d, trials %v:\n%s", code, trials(), h.out)
	}
	h.reset()
	if code := h.run("autopilot", "trial", "hook_routine_steer", "--yes"); code != 0 {
		t.Fatalf("start: %s", h.err)
	}
	if got := trials(); len(got) != 1 || got[0] != "hook_routine_steer" {
		t.Fatalf("running trials: %v", got)
	}
	h.reset()
	if code := h.run("autopilot", "trial", "hook_routine_steer", "--stop"); code != 0 || len(trials()) != 0 {
		t.Fatalf("stop: exit %d, trials %v: %s", code, trials(), h.err)
	}
}
