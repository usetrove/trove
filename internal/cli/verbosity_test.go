package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/store"
)

// seedReplies records sessions of long streamed replies; every fourth one is cut off
// by the client when interrupts is true. The next request is always a tool turn.
func seedReplies(t *testing.T, dir string, sessions int, interrupts bool) {
	t.Helper()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	w := store.NewWriter(db, store.WriterOptions{})
	at := time.Now().Add(-48 * time.Hour)
	for s := 0; s < sessions; s++ {
		for i := 0; i < 8; i++ {
			r := store.Request{TS: at, SessionKey: fmt.Sprintf("s%d", s), Provider: "anthropic", Model: "claude-opus-5-5", Streamed: true,
				StatusCode: 200, LatencyMS: 5000, OutputTokens: 800,
				SegmentTokenEstimate: []byte(fmt.Sprintf(`{"categories":{"history":%d,"tool_results":%d}}`, 1000+i*500, i*400))}
			if interrupts && i%4 == 0 {
				r.ErrorClass = "client_disconnected"
			}
			w.Enqueue(r)
			at = at.Add(8 * time.Second)
		}
	}
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
}

func runVerbosity(t *testing.T, dir string, args ...string) (verbosityOutput, int, string) {
	t.Helper()
	h := newHarness(t, nil)
	code := h.run(append([]string{"optimize", "verbosity", "--json", "--data-dir", dir}, args...)...)
	var out verbosityOutput
	if code == 0 {
		if err := json.Unmarshal(h.out.Bytes(), &out); err != nil {
			t.Fatalf("%v: %s", err, h.out)
		}
	}
	return out, code, h.err.String()
}

func TestOptimizeVerbosityDryRunShowsEvidenceAndChangesNothing(t *testing.T) {
	dir := t.TempDir()
	seedReplies(t, dir, 12, true)
	out, code, stderr := runVerbosity(t, dir)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if out.Learned.Level != config.VerbosityTerse || out.Applied || out.Current.Value != config.VerbosityConcise {
		t.Errorf("dry run: %+v", out)
	}
	if out.Learned.Evidence.Interrupted != 24 || out.Learned.Evidence.Sessions != 12 || out.Learned.Evidence.Label != "estimated" {
		t.Errorf("evidence: %+v", out.Learned.Evidence)
	}
	if _, err := os.Stat(filepath.Join(dir, config.FileName)); !os.IsNotExist(err) {
		t.Error("a dry run must not write config.toml")
	}

	h := newHarness(t, nil)
	if code := h.run("optimize", "verbosity", "--data-dir", dir); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	for _, want := range []string{"Dry run", "terse", "24 interrupted", "12 sessions", "estimated", "--apply"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("text output lacks %q:\n%s", want, h.out)
		}
	}
}

func TestOptimizeVerbosityApply(t *testing.T) {
	dir := t.TempDir()
	seedReplies(t, dir, 12, true)
	out, code, stderr := runVerbosity(t, dir, "--apply")
	if code != 0 || !out.Applied {
		t.Fatalf("exit %d applied=%v: %s", code, out.Applied, stderr)
	}
	s, err := config.ReadVerbositySetting(dir)
	if err != nil || s.Value != config.VerbosityTerse || !s.Learned {
		t.Errorf("config after apply: %+v %v", s, err)
	}
	// Applying again is a no-op that says so.
	if out, code, _ := runVerbosity(t, dir, "--apply"); code != 0 || out.Applied || !strings.Contains(out.Message, "already") {
		t.Errorf("second apply: %d %+v", code, out)
	}
}

func TestOptimizeVerbosityNeverOverridesAHandSetLevel(t *testing.T) {
	dir := t.TempDir()
	seedReplies(t, dir, 12, false) // learns concise
	if err := os.WriteFile(filepath.Join(dir, config.FileName), []byte("[optimize]\nverbosity = \"terse\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, code, stderr := runVerbosity(t, dir, "--apply"); code == 0 || !strings.Contains(stderr, "--force") {
		t.Errorf("a hand-set level must not be replaced without --force: exit %d %s", code, stderr)
	}
	if s, _ := config.ReadVerbositySetting(dir); s.Value != config.VerbosityTerse || !s.HandSet {
		t.Errorf("hand-set level changed: %+v", s)
	}
	if out, code, stderr := runVerbosity(t, dir, "--apply", "--force"); code != 0 || !out.Applied {
		t.Errorf("--force: %d %+v %s", code, out, stderr)
	}
	if s, _ := config.ReadVerbositySetting(dir); s.Value != config.VerbosityConcise || !s.Learned {
		t.Errorf("after --force: %+v", s)
	}
}

func TestOptimizeVerbosityWithoutEnoughEvidenceChangesNothing(t *testing.T) {
	dir := t.TempDir()
	seedReplies(t, dir, 2, true)
	out, code, stderr := runVerbosity(t, dir, "--apply")
	if code != 0 || out.Applied || out.Learned.Level != "" || !strings.Contains(out.Learned.Reason, "not enough") {
		t.Errorf("exit %d %+v %s", code, out, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, config.FileName)); !os.IsNotExist(err) {
		t.Error("nothing must be written without enough evidence")
	}
	// No database at all is fine too.
	if _, code, stderr := runVerbosity(t, filepath.Join(t.TempDir(), "none")); code != 0 {
		t.Errorf("no data: exit %d %s", code, stderr)
	}
}
