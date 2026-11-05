package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/features"
	"github.com/usetrove/trove/internal/hooks"
	"github.com/usetrove/trove/internal/store"
)

// tuneSetup gives a data dir whose repo has trimmed outputs that the agent often
// fetched back, and returns the repo's folder.
func tuneSetup(t *testing.T, trimmerOn bool) (h *harness, dir, repo string) {
	t.Helper()
	h = newHarness(t, nil)
	dir = h.app.Getenv("TROVE_DATA_DIR")
	repo = t.TempDir()
	if trimmerOn {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, config.FileName), []byte("[optimizers]\noutput_trimmer = \"on\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	salt, err := db.Features().Salt(ctx)
	if err != nil {
		t.Fatal(err)
	}
	key := features.RepoKey(repo, salt)
	now := time.Now()
	if err := db.Features().PutSession(ctx, store.SessionFeaturesRow{SessionKey: "s", RepoKey: key, RecordedAt: now}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		kind := "rewritten"
		if i < 50 {
			kind = "fetched_back"
		}
		// 100 events: 50 rewritten (trimmed) outputs and 50 fetch-backs
		if _, err := db.ExecContext(ctx, `INSERT INTO hook_events (ts, session_key, hook, kind, bytes, tokens) VALUES (?,?,?,?,0,0)`,
			now.Add(-time.Hour).UnixMilli(), "s", config.OptimizerOutputTrimmer, kind); err != nil {
			t.Fatal(err)
		}
	}
	return h, dir, repo
}

func TestTuneProposesAppliesAndResets(t *testing.T) {
	h, dir, repo := tuneSetup(t, true)

	if code := h.run("tune", "--repo", repo, "--json"); code != 0 {
		t.Fatalf("tune: exit %d: %s", code, h.err)
	}
	var out tuneOut
	if err := json.Unmarshal(h.out.Bytes(), &out); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, h.out)
	}
	if len(out.Proposals) != 3 || len(out.Tuning) != 3 || out.Tuning[0].State != "suggested" || len(out.Repo) != 16 {
		t.Fatalf("out = %+v", out)
	}
	if strings.Contains(h.out.String(), repo) {
		t.Errorf("the repo's folder must never be printed or stored: %s", h.out)
	}
	if snap, _ := hooks.LoadSnapshot(dir); len(snap.Tuning) != 0 {
		t.Errorf("a suggestion must not reach the hooks: %+v", snap.Tuning)
	}

	h.out.Reset()
	if code := h.run("tune", "apply", "--repo", repo); code != 0 {
		t.Fatalf("apply: exit %d: %s", code, h.err)
	}
	if !strings.Contains(h.out.String(), "new sessions") {
		t.Errorf("apply should say it only affects new sessions: %s", h.out)
	}
	snap, _ := hooks.LoadSnapshot(dir)
	if got := snap.Tuning[out.Repo]["output_trimmer.keep_tail"]; got != 15 {
		t.Errorf("the hooks' snapshot should carry the applied value: %+v", snap.Tuning)
	}

	h.out.Reset()
	if code := h.run("tune", "reset", "--repo", repo, "--json"); code != 0 {
		t.Fatalf("reset: exit %d: %s", code, h.err)
	}
	if snap, _ := hooks.LoadSnapshot(dir); len(snap.Tuning) != 0 {
		t.Errorf("reset must clear the hooks' tuning: %+v", snap.Tuning)
	}
}

func TestTuneRefusesWhenThereIsNothingToDo(t *testing.T) {
	h, _, repo := tuneSetup(t, false)
	if code := h.run("tune", "apply", "--repo", repo); code == 0 || !strings.Contains(h.err.String(), "not on") {
		t.Errorf("with the trimmer off apply must refuse and say why: exit %d %q", code, h.err)
	}
	h.out.Reset()
	if code := h.run("tune", "--repo", repo); code != 0 || !strings.Contains(h.out.String(), "Nothing to tune") {
		t.Errorf("tune with the trimmer off: exit %d %q", code, h.out)
	}

	h, _, repo = tuneSetup(t, true)
	if code := h.run("tune", "apply", "--repo", filepath.Join(repo, "no-such-repo")); code == 0 || !strings.Contains(h.err.String(), "nothing to apply") {
		t.Errorf("a repo with no proposal: exit %d %q", code, h.err)
	}
}
