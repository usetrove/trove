package hooks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/autopilot"
	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/gateway/session"
	"github.com/usetrove/trove/internal/store"
)

// This test runs a whole trial for a user with hooks and no gateway, through the real code at every seam and
// with nothing mocked between them:
//
//	snapshot lists the trial -> real SessionStart hooks pin a side and spool it -> the agent's history records
//	each session's tokens -> session-end bookkeeping ingests the spool, reads the history and runs Autopilot
//	-> the optimizer is kept, rolled back, or left on trial.
//
// The history is synthetic: tokens per session are the same for both sides except for a planted effect.
func runTrial(t *testing.T, onFactor float64, sessions int) (config.AutopilotEntry, int, int) {
	t.Helper()
	r := newRig(t, nil)
	start := r.now
	trialStarted := start.Add(-time.Hour)

	// Autopilot with trials on, the optimizer on trial, and enough hook traffic that Trove has seen the user.
	f, err := autopilot.Enable(r.dir, autopilot.DefaultPolicy(), start.AddDate(0, 0, -40))
	if err != nil {
		t.Fatal(err)
	}
	f.Policy = config.AutopilotPolicyV2
	f.Entries[trialHook] = config.AutopilotEntry{State: config.AutopilotProbation, SinceMS: trialStarted.UnixMilli(), Attempts: 1}
	if err := config.SaveAutopilot(r.dir, f); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default().WithAutopilot(f)
	r.snapshot(BuildSnapshot(cfg, nil, false, start))
	if snap, _ := LoadSnapshot(r.dir); len(snap.Trial) != 1 || snap.Trial[0] != trialHook {
		t.Fatalf("the snapshot does not list the trial: %+v", snap.Trial)
	}

	// Real SessionStart hooks, one per session.
	type run struct {
		id string
		on bool
	}
	var runs []run
	for i := 0; i < sessions; i++ {
		id := fmt.Sprintf("e2e-session-%03d", i)
		key := r.startAs(id)
		runs = append(runs, run{id, loadSession(r.dir, key).Pins[trialHook] == stateOn})
	}

	// The agent's own history for those sessions: 4 requests each, fewer tokens when the optimizer was on.
	var b strings.Builder
	onN, offN := 0, 0
	for i, rn := range runs {
		factor := 1.0
		if rn.on {
			factor, onN = onFactor, onN+1
		} else {
			offN++
		}
		in := int(1000 * factor * (1 + 0.03*float64(i%7)))
		for q := 0; q < 4; q++ {
			ts := start.Add(time.Duration(i)*time.Minute + time.Duration(q)*time.Second).UTC().Format("2006-01-02T15:04:05.000Z")
			fmt.Fprintf(&b, `{"type":"assistant","sessionId":%q,"requestId":"req_%d_%d","timestamp":%q,"version":"2.1.302","message":{"id":"m","model":"claude-sonnet-5-5","content":[{"type":"text","text":"x"}],"usage":{"input_tokens":%d,"output_tokens":50,"cache_read_input_tokens":4000,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0}}}}`+"\n", rn.id, i, q, ts, in)
		}
	}
	cc := t.TempDir()
	p := filepath.Join(cc, "projects", "proj")
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "s.jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	getenv := func(k string) string {
		if k == "CLAUDE_CONFIG_DIR" {
			return cc
		}
		return ""
	}

	// The user has been using the hooks, so Trove has seen them.
	db, err := store.Open(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`INSERT INTO hook_events(ts, session_key, hook, kind, bytes, tokens) VALUES(?,?,?,?,?,?)`,
		start.UnixMilli(), session.ClaudeCodeKey("e2e-session-000"), trialHook, "would_block", 4000, 1000); err != nil {
		t.Fatal(err)
	}

	// A session ends, a day later: the real bookkeeping.
	later := start.Add(36 * time.Hour)
	if err := Maintain(context.Background(), db, r.dir, config.Default(), getenv, later); err != nil {
		t.Fatal(err)
	}
	f, err = config.LoadAutopilot(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	return f.Entries[trialHook], onN, offN
}

func TestATrialRunsFromSessionStartToAKeptOptimizer(t *testing.T) {
	e, on, off := runTrial(t, 0.70, 160)
	if on < 20 || off < 20 {
		t.Fatalf("test data too unbalanced: %d on, %d off", on, off)
	}
	if e.State != config.AutopilotOn || !strings.Contains(e.Reason, "clean trial") {
		t.Fatalf("a planted 30%% saving should be kept: %+v", e)
	}
}

func TestATrialRunsFromSessionStartToARollback(t *testing.T) {
	e, _, _ := runTrial(t, 1.40, 160)
	if e.State != config.AutopilotRolledBack {
		t.Fatalf("a planted 40%% increase should be rolled back: %+v", e)
	}
}

func TestATrialWithNoEffectIsLeftRunning(t *testing.T) {
	e, _, _ := runTrial(t, 1.00, 160)
	if e.State != config.AutopilotProbation || !strings.Contains(e.Reason, "trial running") {
		t.Fatalf("no effect should leave the trial running: %+v", e)
	}
}
