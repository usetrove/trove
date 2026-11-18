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

var ruleTrial = RuleTrialName("caveman")

// ruleSessions stores n finished sessions of the caveman trial the way a subscription user has them: the hooks'
// arm, priced history rows and one test run each. With the rules on, replies are outFactor as long; failOn makes
// every second on-arm session end with failing tests.
func ruleSessions(t *testing.T, db *store.DB, n int, at time.Time, outFactor float64, failOn bool) {
	t.Helper()
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("rule%03d", i)
		arm := measure.Arm(key, ruleTrial)
		f := 1.0
		if arm == experiment.ArmOn {
			f = outFactor
		}
		if _, err := db.Exec(`INSERT INTO experiment_assignments(session_key, optimizer, arm, stratum, seq, assigned_at) VALUES(?,?,?,'',0,?)`,
			key, ruleTrial, arm, at.UnixMilli()); err != nil {
			t.Fatal(err)
		}
		for r := 0; r < 4; r++ {
			ts := at.Add(time.Duration(i)*time.Hour + time.Duration(r)*time.Minute)
			out := int64(20000 * f * (1 + 0.03*float64(i%7)))
			if _, err := db.Exec(`INSERT INTO history_requests(request_key, source, session_key, ts, input_tokens, cache_read, output_tokens, cost_usd)
				VALUES(?,?,?,?,?,?,?,?)`, fmt.Sprintf("%s-%d", key, r), "claude_code", key, ts.UnixMilli(), 2000, 30000, out, 0.01+float64(out)*0.000075); err != nil {
				t.Fatal(err)
			}
		}
		code := 0
		if failOn && arm == experiment.ArmOn && i%2 == 0 {
			code = 1
		}
		if _, err := db.Exec(`INSERT INTO test_runs(session_key, ts, cmd_hash, exit_code) VALUES(?,?,'h',?)`, key, at.Add(time.Duration(i)*time.Hour+5*time.Minute).UnixMilli(), code); err != nil {
			t.Fatal(err)
		}
	}
}

// onRuleTrial is a user with hooks and no gateway, Autopilot on, who started a caveman trial at trialStart.
func onRuleTrial(t *testing.T) (*store.DB, string) {
	t.Helper()
	db, dir := open(t)
	setInstall(t, db, 45)
	hookSessions(t, db, trialOptimizer, 25, 500)
	if _, err := Enable(dir, DefaultPolicy(), trialStart.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := StartRuleTrial(dir, "caveman", "", trialStart); err != nil {
		t.Fatal(err)
	}
	return db, dir
}

func advanceRule(t *testing.T, db *store.DB, dir string, at time.Time) config.AutopilotEntry {
	t.Helper()
	res, err := Advance(context.Background(), db, dir, config.Default(), DefaultPolicy(), at)
	if err != nil {
		t.Fatal(err)
	}
	return res.File.Entries[ruleTrial]
}

// Done when (6.6): the verdict reports output tokens, cost per turn, test-fail rate and turn count. A clean result
// ends the trial with that verdict and never turns the rule set on (T2 needs a benchmark verdict for that).
func TestACleanRuleTrialReportsTheVerdictAndTurnsNothingOn(t *testing.T) {
	db, dir := onRuleTrial(t)
	ruleSessions(t, db, 160, trialStart.Add(24*time.Hour), 0.5, false)
	e := advanceRule(t, db, dir, hookNow)
	if e.State != config.AutopilotOff || !strings.Contains(e.Reason, "trial finished") || !strings.Contains(e.Reason, "does not turn rule sets on") {
		t.Fatalf("after a clean result: %+v", e)
	}
	for _, want := range []string{"output tokens per turn: -", "cost per turn: -", "test-fail rate: 0% with it on", "turns: ", "measured"} {
		if !strings.Contains(e.Reason, want) {
			t.Errorf("verdict lacks %q: %s", want, e.Reason)
		}
	}
	f, _ := config.LoadAutopilot(dir)
	if _, ok := f.States()[ruleTrial]; ok || len(f.RuleTrials()) != 0 || len(f.Trials()) != 0 {
		t.Errorf("a finished rule-set trial must leave no state and no trial: states %v", f.States())
	}
}

func TestARuleTrialThatBreaksTestsIsStopped(t *testing.T) {
	db, dir := onRuleTrial(t)
	ruleSessions(t, db, 160, trialStart.Add(24*time.Hour), 0.5, true)
	e := advanceRule(t, db, dir, hookNow)
	if e.State != config.AutopilotRolledBack || !strings.Contains(e.Reason, "test_fail_rate") {
		t.Fatalf("failing tests on the rules side must stop the trial: %+v", e)
	}
}

func TestARuleTrialStopsWhenTheRuleSetIsSwitchedOnEverywhere(t *testing.T) {
	db, dir := onRuleTrial(t)
	if err := db.ExternalTools().Put(context.Background(), store.ExternalTool{Name: "caveman", Version: "4.1.0", License: "Apache-2.0", State: "on", InstalledAt: hookNow}); err != nil {
		t.Fatal(err)
	}
	e := advanceRule(t, db, dir, hookNow)
	if e.State != config.AutopilotOff || !strings.Contains(e.Reason, "nothing to compare") {
		t.Fatalf("with caveman on for every session there is no control group: %+v", e)
	}
}

func TestARuleTrialWithTooFewSessionsKeepsRunning(t *testing.T) {
	db, dir := onRuleTrial(t)
	ruleSessions(t, db, 10, trialStart.Add(24*time.Hour), 0.5, false)
	e := advanceRule(t, db, dir, hookNow)
	if e.State != config.AutopilotProbation || !strings.Contains(e.Reason, "trial running") {
		t.Fatalf("%+v", e)
	}
	f, _ := config.LoadAutopilot(dir)
	if got := f.RuleTrials(); len(got) != 1 || got[0] != ruleTrial {
		t.Errorf("running trials: %v", got)
	}
}

func TestStartRuleTrialRefuses(t *testing.T) {
	_, dir := open(t)
	if _, err := StartRuleTrial(dir, "caveman", "", hookNow); err == nil || !strings.Contains(err.Error(), "autopilot on") {
		t.Errorf("without Autopilot: %v", err)
	}
	if _, err := Enable(dir, DefaultPolicy(), hookNow); err != nil {
		t.Fatal(err)
	}
	if _, err := StartRuleTrial(dir, "rtk", "", hookNow); err == nil || !strings.Contains(err.Error(), "not a rule set") {
		t.Errorf("rtk is not a rule set: %v", err)
	}
	if _, err := StartRuleTrial(dir, "caveman", "caveman is not installed", hookNow); err == nil || err.Error() != "caveman is not installed" {
		t.Errorf("the caller's blocker must be returned: %v", err)
	}
	if _, err := StartRuleTrial(dir, "ponytail", "", hookNow); err != nil {
		t.Fatal(err)
	}
	if _, err := StartRuleTrial(dir, "caveman", "", hookNow); err == nil || !strings.Contains(err.Error(), "one trial runs at a time") {
		t.Errorf("a second trial: %v", err)
	}
	if _, err := StopRuleTrial(dir, "ponytail", hookNow); err != nil {
		t.Fatal(err)
	}
	if f, _ := config.LoadAutopilot(dir); len(f.RuleTrials()) != 0 {
		t.Error("stop must end the trial")
	}
}

func TestRuleSetsAreTheBrevitySlotTools(t *testing.T) {
	if got := strings.Join(RuleSets(), ","); got != "caveman,ponytail,terse-rules" {
		t.Errorf("rule sets: %s", got)
	}
}
