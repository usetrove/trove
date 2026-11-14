package hooks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/gateway/session"
	"github.com/usetrove/trove/internal/store"
)

// writeClaudeHistory puts n synthetic assistant turns in a fake Claude Code config dir. Nothing here is a real log.
func writeClaudeHistory(t *testing.T, configDir string, n int, at time.Time) {
	t.Helper()
	var b strings.Builder
	for i := 0; i < n; i++ {
		ts := at.Add(time.Duration(i) * time.Second).UTC().Format("2006-01-02T15:04:05.000Z")
		fmt.Fprintf(&b, `{"type":"assistant","sessionId":"trial-sess","requestId":"req_%d","timestamp":%q,"version":"2.1.302","message":{"id":"m","model":"claude-sonnet-5-5","content":[{"type":"text","text":"SECRET-ANSWER"}],"usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":5000,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0}}}}`+"\n", i, ts)
	}
	p := filepath.Join(configDir, "projects", "proj")
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "s.jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func trialDB(t *testing.T, onTrial bool) (*store.DB, string, func(string) string, time.Time) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".trove")
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now()
	cc := t.TempDir()
	writeClaudeHistory(t, cc, 6, now.Add(-2*time.Hour))
	f := config.AutopilotFile{Version: 1, Enabled: true, Policy: config.AutopilotPolicyV2, Entries: map[string]config.AutopilotEntry{
		trialHook: {State: config.AutopilotShadow, SinceMS: now.Add(-48 * time.Hour).UnixMilli()},
	}}
	if onTrial {
		f.Entries[trialHook] = config.AutopilotEntry{State: config.AutopilotProbation, SinceMS: now.Add(-48 * time.Hour).UnixMilli()}
	}
	if err := config.SaveAutopilot(dir, f); err != nil {
		t.Fatal(err)
	}
	getenv := func(k string) string {
		if k == "CLAUDE_CONFIG_DIR" {
			return cc
		}
		return ""
	}
	return db, dir, getenv, now
}

func historyRows(t *testing.T, db *store.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM history_requests`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// While a hook technique is on trial, the session-end bookkeeping reads the agent's history, so a user with no
// gateway has sessions to compare.
func TestHistoryIsReadWhileAHookTechniqueIsOnTrial(t *testing.T) {
	db, dir, getenv, now := trialDB(t, true)
	if err := Maintain(context.Background(), db, dir, config.Default(), getenv, now); err != nil {
		t.Fatal(err)
	}
	if n := historyRows(t, db); n != 6 {
		t.Errorf("%d history rows, want 6", n)
	}
}

// With no trial running, the hooks never read the user's history on their own.
func TestHistoryIsNotReadWhenNothingIsOnTrial(t *testing.T) {
	db, dir, getenv, now := trialDB(t, false)
	if err := Maintain(context.Background(), db, dir, config.Default(), getenv, now); err != nil {
		t.Fatal(err)
	}
	if n := historyRows(t, db); n != 0 {
		t.Errorf("%d history rows were read with no trial running", n)
	}
}

// A user who opted out of A/B comparison has no trial, so their history is not read for one.
func TestHistoryIsNotReadWhenComparisonIsOff(t *testing.T) {
	db, dir, getenv, now := trialDB(t, true)
	cfg := config.Default()
	cfg.Experiment.Enabled = false
	if err := Maintain(context.Background(), db, dir, cfg, getenv, now); err != nil {
		t.Fatal(err)
	}
	if n := historyRows(t, db); n != 0 {
		t.Errorf("%d history rows were read with A/B comparison off", n)
	}
}

// A user who switched history reading off never has it read for a trial.
func TestHistoryIsNotReadWhenHistoryIsSwitchedOff(t *testing.T) {
	db, dir, getenv, now := trialDB(t, true)
	cfg := config.Default()
	cfg.History.Enabled = false
	if err := Maintain(context.Background(), db, dir, cfg, getenv, now); err != nil {
		t.Fatal(err)
	}
	if n := historyRows(t, db); n != 0 {
		t.Errorf("%d history rows were read with history switched off", n)
	}
}

// The scan is bounded: a context that is already over reads nothing and returns at once, and never errors.
func TestTheTrialScanGivesUpQuietlyWhenOutOfTime(t *testing.T) {
	db, dir, getenv, _ := trialDB(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	scanTrialHistory(ctx, db, dir, config.Default(), getenv)
	if time.Since(start) > time.Second {
		t.Error("a cancelled scan took too long")
	}
}

// The comparison joins the hooks' arm records to history rows on the session key, so the two must be the same
// string for the same Claude Code session. If they ever drift apart the comparison finds nothing and says
// "not enough data" forever.
func TestHistoryRowsUseTheSameSessionKeyAsTheHooks(t *testing.T) {
	db, dir, getenv, now := trialDB(t, true)
	if err := Maintain(context.Background(), db, dir, config.Default(), getenv, now); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM history_requests WHERE session_key = ? OR session_key LIKE ?`,
		session.ClaudeCodeKey("trial-sess"), session.ClaudeCodeKey("trial-sess")+"~%").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 6 {
		t.Errorf("%d history rows match the hooks' key for the session, want 6", n)
	}
}
