package autopilot

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/experiment"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/store"
)

var codeTrial = RuleTrialName("ponytail")

// codeSessions stores n finished sessions of the ponytail trial: the hooks' arm, priced history rows, a Write call
// per request whose input is codeFactor as large on the rules side, and one test run each. failOn makes every second
// on-arm session end with failing tests.
func codeSessions(t *testing.T, db *store.DB, n int, at time.Time, codeFactor float64, failOn bool) {
	t.Helper()
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("code%03d", i)
		arm := measure.Arm(key, codeTrial)
		f := 1.0
		if arm == experiment.ArmOn {
			f = codeFactor
		}
		if _, err := db.Exec(`INSERT INTO experiment_assignments(session_key, optimizer, arm, stratum, seq, assigned_at) VALUES(?,?,?,'',0,?)`,
			key, codeTrial, arm, at.UnixMilli()); err != nil {
			t.Fatal(err)
		}
		for r := 0; r < 4; r++ {
			ts := at.Add(time.Duration(i)*time.Hour + time.Duration(r)*time.Minute)
			code := int64(8000 * f * (1 + 0.05*float64(i%5)))
			out := 2000 + code/4
			rk := fmt.Sprintf("%s-%d", key, r)
			if _, err := db.Exec(`INSERT INTO history_requests(request_key, source, session_key, ts, model, input_tokens, cache_read, output_tokens, cost_usd)
				VALUES(?,?,?,?,?,?,?,?,?)`, rk, "claude_code", key, ts.UnixMilli(), "claude-opus-5-5", 2000, 30000, out, 0.01+float64(out)*0.000075); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO history_tool_calls(request_key, idx, ts, tool_name, input_bytes) VALUES(?,0,?,'Write',?)`, rk, ts.UnixMilli(), code); err != nil {
				t.Fatal(err)
			}
		}
		exit := 0
		if failOn && arm == experiment.ArmOn && i%2 == 0 {
			exit = 1
		}
		if _, err := db.Exec(`INSERT INTO test_runs(session_key, ts, cmd_hash, exit_code) VALUES(?,?,'h',?)`, key, at.Add(time.Duration(i)*time.Hour+5*time.Minute).UnixMilli(), exit); err != nil {
			t.Fatal(err)
		}
	}
}

func onCodeTrial(t *testing.T) (*store.DB, string) {
	t.Helper()
	db, dir := open(t)
	setInstall(t, db, 45)
	hookSessions(t, db, trialOptimizer, 25, 500)
	if _, err := Enable(dir, DefaultPolicy(), trialStart.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := StartRuleTrial(dir, "ponytail", "", trialStart); err != nil {
		t.Fatal(err)
	}
	return db, dir
}

func advanceCode(t *testing.T, db *store.DB, dir string) config.AutopilotEntry {
	t.Helper()
	res, err := Advance(context.Background(), db, dir, config.Default(), DefaultPolicy(), hookNow)
	if err != nil {
		t.Fatal(err)
	}
	return res.File.Entries[codeTrial]
}

func TestPonytailIsACodeRuleSet(t *testing.T) {
	if !IsCodeRuleSet("ponytail") {
		t.Error("ponytail must be judged by the code-output trial")
	}
	for _, other := range []string{"caveman", "terse-rules", "rtk"} {
		if IsCodeRuleSet(other) {
			t.Errorf("%s is not a code rule set", other)
		}
	}
}

func TestACodeTrialThatWritesLessCodeFinishesWithTheFigures(t *testing.T) {
	db, dir := onCodeTrial(t)
	codeSessions(t, db, 160, trialStart.Add(24*time.Hour), 0.5, false)
	e := advanceCode(t, db, dir)
	if e.State != config.AutopilotOff || !strings.Contains(e.Reason, "less code written per session") || !strings.Contains(e.Reason, "does not turn rule sets on") {
		t.Fatalf("after a clean result: %+v", e)
	}
	for _, want := range []string{"code written per session", "(estimated from tool-input sizes", "tests passed at the end of 100% of sessions", "(measured"} {
		if !strings.Contains(e.Reason, want) {
			t.Errorf("verdict lacks %q: %s", want, e.Reason)
		}
	}
}

func TestACodeTrialThatBreaksTestsIsStopped(t *testing.T) {
	db, dir := onCodeTrial(t)
	codeSessions(t, db, 160, trialStart.Add(24*time.Hour), 0.5, true)
	e := advanceCode(t, db, dir)
	if e.State != config.AutopilotRolledBack || !strings.Contains(e.Reason, "test_fail_rate") {
		t.Fatalf("failing tests on the rules side must stop the trial: %+v", e)
	}
}

func TestACodeTrialThatWritesMoreCodeIsStopped(t *testing.T) {
	db, dir := onCodeTrial(t)
	codeSessions(t, db, 160, trialStart.Add(24*time.Hour), 1.8, false)
	e := advanceCode(t, db, dir)
	if e.State != config.AutopilotRolledBack {
		t.Fatalf("more code with the rules must stop the trial: %+v", e)
	}
}

func TestACodeTrialWithTooFewSessionsKeepsRunning(t *testing.T) {
	db, dir := onCodeTrial(t)
	codeSessions(t, db, 10, trialStart.Add(24*time.Hour), 0.5, false)
	e := advanceCode(t, db, dir)
	if e.State != config.AutopilotProbation || !strings.Contains(e.Reason, "finished sessions with the rules") {
		t.Fatalf("%+v", e)
	}
}
