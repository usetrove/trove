package measure

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/store"
	"github.com/usetrove/trove/internal/tasks"
)

func agentDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func mustExec(t *testing.T, db *store.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%v\n%s", err, q)
	}
}

func installTask(t *testing.T, db *store.DB, id, feature, effort string, at int64, reverted bool) {
	t.Helper()
	var rev any
	if reverted {
		rev = at + 1
	}
	mustExec(t, db, `INSERT INTO fixes (change_id, fix_id, kind, agent, path_hash, applied_at, est_tokens, est_usd, label, reverted_at)
		VALUES (?, ?, ?, 'claude_code', 'h', ?, 0, NULL, 'estimated', ?)`, id, "f-"+id, feature, at, rev)
	mustExec(t, db, `INSERT INTO task_installs (change_id, fix_id, feature, role, effort) VALUES (?, ?, ?, 'x', ?)`, id, "f-"+id, feature, effort)
}

// run adds a subagent run and its requests (each with in/out/thinking tokens and an effort word).
func addRun(t *testing.T, db *store.DB, sess, agent, typ, status string, at int64, model, effort string, out, thinking int64, reqs int) {
	t.Helper()
	mustExec(t, db, `INSERT INTO subagent_runs (session_key, agent_key, agent_type, started_at, stopped_at, status) VALUES (?, ?, ?, ?, ?, ?)`,
		sess, agent, typ, at, at+1000, status)
	for i := 0; i < reqs; i++ {
		mustExec(t, db, `INSERT INTO history_requests (request_key, source, session_key, ts, model, input_tokens, output_tokens, cache_read, sidechain, agent_key, thinking_tokens, effort)
			VALUES (?, 'claude_code', ?, ?, ?, 100, ?, 1000, 1, ?, ?, ?)`, fmt.Sprintf("%s-%s-%d", sess, agent, i), sess, at+int64(i), model, out, agent, thinking, effort)
	}
}

func TestAgentEffectsPerTypeBeforeAfterAndFailures(t *testing.T) {
	db := agentDB(t)
	const install = int64(1_000_000)
	installTask(t, db, "c1", tasks.FeatureRoutine, "low", install, false)
	installTask(t, db, "c2", tasks.FeatureExplore, "low", install, false)
	installTask(t, db, "c3", tasks.FeatureCascade, "", install, false) // not a per-agent feature
	// Explore before the override: the built-in on the main model at medium effort.
	addRun(t, db, "s0", "e0", "Explore", "unknown", install-5000, "claude-opus-5-5", "medium", 2000, 1000, 2)
	// After: Trove's Explore on Haiku at low effort.
	addRun(t, db, "s1", "e1", "Explore", "done", install+10, "claude-haiku-5-5", "low", 200, 20, 2)
	// Test runner: two done, one failed then re-delegated, one unsure, one with no status, one run at high effort.
	addRun(t, db, "s2", "t1", tasks.TestRunnerName, "done", install+100, "claude-haiku-5-5", "low", 100, 10, 1)
	addRun(t, db, "s2", "t2", tasks.TestRunnerName, "failed", install+200, "claude-haiku-5-5", "low", 100, 10, 1)
	addRun(t, db, "s2", "g1", "general-purpose", "unknown", install+300, "claude-opus-5-5", "medium", 900, 400, 1)
	addRun(t, db, "s3", "t3", tasks.TestRunnerName, "done", install+400, "claude-haiku-5-5", "high", 100, 10, 1)
	addRun(t, db, "s3", "t4", tasks.TestRunnerName, "unsure", install+500, "claude-haiku-5-5", "low", 100, 10, 1)
	addRun(t, db, "s4", "t5", tasks.TestRunnerName, "unknown", install+600, "claude-haiku-5-5", "low", 100, 10, 1)

	m := config.Default().Measure
	m.MinSessions = 3
	got, err := AgentEffects(context.Background(), db, m, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]AgentEffect{}
	for _, e := range got {
		by[e.AgentType] = e
	}
	if len(got) != 3 {
		t.Fatalf("want Explore, test runner and lint fixer, got %d: %+v", len(got), got)
	}
	ex := by["Explore"]
	if ex.Before == nil || ex.Before.Runs != 1 || ex.After.Runs != 1 {
		t.Fatalf("explore before/after: %+v", ex)
	}
	if ex.Before.TokensPerRun <= ex.After.TokensPerRun || ex.After.Models["claude-haiku-5-5"] != 2 {
		t.Errorf("explore tokens: before %v after %v", ex.Before.TokensPerRun, ex.After.TokensPerRun)
	}
	if ex.After.ThinkingShare == nil || *ex.After.ThinkingShare != 0.1 {
		t.Errorf("thinking share = thinking / output: %v", ex.After.ThinkingShare)
	}
	if ex.Design != "observational before/after" || ex.Label != Measured || ex.After.Limits != nil {
		t.Errorf("labels: %+v (no fit, so no limit share)", ex)
	}
	tr := by[tasks.TestRunnerName]
	if tr.Before != nil {
		t.Error("a routine agent did not exist before; it has no before")
	}
	if tr.After.Runs != 5 || tr.After.FailureRate == nil || *tr.After.FailureRate != 0.5 {
		t.Fatalf("failure rate is failed+unsure over known statuses (2 of 4): %+v", tr.After)
	}
	if tr.After.Redelegated != 1 {
		t.Errorf("the failed run was followed by general-purpose: %d", tr.After.Redelegated)
	}
	if tr.After.Effort["high"] != 1 || !strings.Contains(tr.Text, "did not run at low effort") {
		t.Errorf("a request at another effort must be pointed out: %v / %s", tr.After.Effort, tr.Text)
	}
	if !strings.Contains(tr.Recommend, "trove tasks remove agents") {
		t.Errorf("a high failure rate must suggest removing: %q", tr.Recommend)
	}
	if lf := by[tasks.LintFixerName]; lf.After.Runs != 0 || !strings.Contains(lf.Text, "Not enough runs") || lf.Recommend != "" {
		t.Errorf("no runs: %+v", lf)
	}
}

func TestAgentEffectsScoutNeedsTheEffortInstall(t *testing.T) {
	db := agentDB(t)
	installTask(t, db, "old", tasks.FeatureScout, "", 1000, false) // a scout from before effort existed
	if got, err := AgentEffects(context.Background(), db, config.Default().Measure, nil, time.Now()); err != nil || len(got) != 0 {
		t.Fatalf("a scout without an effort install has nothing per-agent to show: %+v %v", got, err)
	}
	installTask(t, db, "new", tasks.FeatureScout, "low", 5000, false)
	installTask(t, db, "gone", tasks.FeatureExplore, "low", 5000, true)
	got, err := AgentEffects(context.Background(), db, config.Default().Measure, nil, time.Now())
	if err != nil || len(got) != 1 || got[0].AgentType != tasks.ScoutName {
		t.Fatalf("want only the scout (Explore was removed): %+v %v", got, err)
	}
}
