package autopilot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/experiment"
	"github.com/usetrove/trove/internal/hookcaps"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/store"
)

const steerName = config.OptimizerHookRoutineSteer

func steerArm(n int, mean float64, closed, reprompted int) measure.SteerArm {
	a := measure.SteerArm{Sessions: n, Turns: 3 * n, Closed: closed, Reprompted: reprompted}
	for i := 0; i < n; i++ {
		a.PerSession = append(a.PerSession, mean*(1+0.1*float64(i%5-2)))
	}
	return a
}

func TestDecideSteerTrial(t *testing.T) {
	running := config.AutopilotEntry{State: config.AutopilotProbation, SinceMS: hookNow.Add(-24 * time.Hour).UnixMilli()}
	old := config.AutopilotEntry{State: config.AutopilotProbation, SinceMS: hookNow.AddDate(0, 0, -400).UnixMilli()}
	saves := measure.JudgeSteerTrial(steerArm(40, 100, 80, 4), steerArm(40, 400, 80, 4), 20, RepromptMarginPts)
	worse := measure.JudgeSteerTrial(steerArm(40, 700, 80, 4), steerArm(40, 400, 80, 4), 20, RepromptMarginPts)
	flat := measure.JudgeSteerTrial(steerArm(40, 400, 80, 4), steerArm(40, 400, 80, 4), 20, RepromptMarginPts)
	reprompts := measure.JudgeSteerTrial(steerArm(40, 100, 200, 120), steerArm(40, 400, 200, 10), 20, RepromptMarginPts)
	few := measure.JudgeSteerTrial(steerArm(3, 100, 5, 0), steerArm(3, 400, 5, 0), 20, RepromptMarginPts)
	failing := TestEvidence{On: measure.TestFailArm{Sessions: 30, Failed: 15}, Off: measure.TestFailArm{Sessions: 30, Failed: 1}}
	moreTurns := experiment.Result{Status: experiment.WorseOutcomes, Reason: "with it on, turns is higher (measured)"}
	cases := []struct {
		name   string
		cur    config.AutopilotEntry
		ev     Evidence
		v      experiment.Result
		s      measure.SteerVerdict
		state  string
		reason string
	}{
		{"blocked", running, Evidence{Blocked: "the check has not passed"}, experiment.Result{}, saves, config.AutopilotOff, "trial stopped: the check"},
		{"tests fail more", running, Evidence{Tests: failing}, experiment.Result{}, saves, config.AutopilotRolledBack, "test_fail_rate"},
		{"re-prompts", running, Evidence{}, experiment.Result{}, reprompts, config.AutopilotRolledBack, "re-prompt guardrail"},
		{"more turns", running, Evidence{}, moreTurns, saves, config.AutopilotRolledBack, "turns is higher"},
		{"more thinking", running, Evidence{}, experiment.Result{}, worse, config.AutopilotRolledBack, "more thinking per routine turn"},
		{"less thinking", running, Evidence{}, experiment.Result{}, saves, config.AutopilotOff, "trial finished: less thinking"},
		{"no effect yet", running, Evidence{}, experiment.Result{}, flat, config.AutopilotProbation, "trial running"},
		{"too few", running, Evidence{}, experiment.Result{}, few, config.AutopilotProbation, "trial running"},
		{"out of time", old, Evidence{}, experiment.Result{}, flat, config.AutopilotOff, "no clear effect on thinking"},
		{"not running", config.AutopilotEntry{State: config.AutopilotOff}, Evidence{}, experiment.Result{}, saves, config.AutopilotOff, ""},
	}
	for _, c := range cases {
		d := DecideSteerTrial(c.cur, c.ev, c.v, c.s, hookNow)
		if d.Entry.State != c.state || !strings.Contains(d.Entry.Reason, c.reason) {
			t.Errorf("%s: %+v", c.name, d.Entry)
		}
		if d.Entry.State == config.AutopilotOn {
			t.Errorf("%s: Autopilot must never turn the steer on", c.name)
		}
	}
	if d := DecideSteerTrial(running, Evidence{}, experiment.Result{}, saves, hookNow); !strings.Contains(d.Entry.Reason, "does not turn it on") ||
		!strings.Contains(d.Entry.Reason, "(measured") {
		t.Errorf("a finished trial must say it turns nothing on and label its figures: %s", d.Entry.Reason)
	}
}

func contextChecked(t *testing.T, dir string) {
	t.Helper()
	c := hookcaps.Capabilities{V: hookcaps.Version, Entries: []hookcaps.Capability{
		{Agent: "claude", Tool: hookcaps.ToolPrompt, Context: hookcaps.Yes, Method: "session"}}}
	b, _ := json.Marshal(c)
	if err := os.MkdirAll(filepath.Dir(hookcaps.Path(dir)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hookcaps.Path(dir), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// steerSessions stores n finished sessions of the steer's trial as the hooks and the history parser leave them:
// the arm, three routine turns each with thinking onThinking or offThinking, and one test run.
func steerSessions(t *testing.T, db *store.DB, n int, at time.Time, onThinking, offThinking int64) {
	t.Helper()
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("steer%03d", i)
		arm := measure.Arm(key, steerName)
		think := offThinking
		kind := "would_steer"
		if arm == experiment.ArmOn {
			think, kind = onThinking, "steered"
		}
		think = think * int64(90+i%20) / 100
		exec := func(q string, args ...any) {
			t.Helper()
			if _, err := db.Exec(q, args...); err != nil {
				t.Fatal(err)
			}
		}
		base := at.Add(time.Duration(i) * time.Hour)
		exec(`INSERT INTO experiment_assignments(session_key, optimizer, arm, stratum, seq, assigned_at) VALUES(?,?,?,'',0,?)`, key, steerName, arm, at.UnixMilli())
		for r := 0; r < 3; r++ {
			start := base.Add(time.Duration(r) * 10 * time.Minute)
			exec(`INSERT INTO hook_events(ts, session_key, hook, kind) VALUES(?,?,?,?)`, start.UnixMilli(), key, steerName, kind)
			exec(`INSERT INTO history_requests(request_key, source, session_key, ts, input_tokens, cache_read, output_tokens, thinking_tokens, cost_usd)
				VALUES(?,?,?,?,?,?,?,?,?)`, fmt.Sprintf("%s-%d", key, r), "claude_code", key, start.Add(time.Minute).UnixMilli(), 2000, 30000, 2000, think, 0.05)
			exec(`INSERT INTO hook_events(ts, session_key, hook, kind) VALUES(?,?,?,?)`, start.Add(5*time.Minute).UnixMilli(), key, steerName, "turn_end")
		}
		exec(`INSERT INTO test_runs(session_key, ts, cmd_hash, exit_code) VALUES(?,?,'h',0)`, key, base.Add(40*time.Minute).UnixMilli())
	}
}

func onSteerTrial(t *testing.T, checked bool) (*store.DB, string) {
	t.Helper()
	db, dir := open(t)
	setInstall(t, db, 45)
	hookSessions(t, db, trialOptimizer, 25, 500)
	if _, err := Enable(dir, DefaultPolicy(), trialStart.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if checked {
		contextChecked(t, dir)
	}
	if _, err := StartRuleTrial(dir, steerName, "", trialStart); err != nil {
		t.Fatal(err)
	}
	return db, dir
}

func TestSteerTrialEntryIsTheOptimizerAndNeverAState(t *testing.T) {
	_, dir := onSteerTrial(t, true)
	f, _ := config.LoadAutopilot(dir)
	if got := f.RuleTrials(); len(got) != 1 || got[0] != steerName {
		t.Fatalf("rule trials: %v", got)
	}
	if _, ok := f.States()[steerName]; ok {
		t.Error("a trial must not set the steer's state: the hooks assign its arms")
	}
	if _, ok := f.StatesWithArms(true)[steerName]; ok {
		t.Error("nor with arms")
	}
	if len(f.Trials()) != 0 || !config.HookAssignsTrial(steerName) {
		t.Error("the gateway must never split sessions for the steer")
	}
	if !strings.Contains(f.Entries[steerName].Reason, "routine prompts are steered") {
		t.Errorf("reason: %s", f.Entries[steerName].Reason)
	}
}

func TestACleanSteerTrialReportsLessThinkingAndTurnsNothingOn(t *testing.T) {
	db, dir := onSteerTrial(t, true)
	steerSessions(t, db, 160, trialStart.Add(24*time.Hour), 120, 480)
	res, err := Advance(t.Context(), db, dir, config.Default(), DefaultPolicy(), hookNow)
	if err != nil {
		t.Fatal(err)
	}
	e := res.File.Entries[steerName]
	if e.State != config.AutopilotOff || !strings.Contains(e.Reason, "trial finished: less thinking") || !strings.Contains(e.Reason, "does not turn it on") {
		t.Fatalf("after a clean result: %+v", e)
	}
	for _, want := range []string{"thinking per routine turn", "with the steer against", "(measured", "re-prompts"} {
		if !strings.Contains(e.Reason, want) {
			t.Errorf("verdict lacks %q: %s", want, e.Reason)
		}
	}
}

func TestASteerTrialStopsWithoutTheLiveCheck(t *testing.T) {
	db, dir := onSteerTrial(t, false)
	steerSessions(t, db, 10, trialStart.Add(24*time.Hour), 120, 480)
	res, err := Advance(t.Context(), db, dir, config.Default(), DefaultPolicy(), hookNow)
	if err != nil {
		t.Fatal(err)
	}
	if e := res.File.Entries[steerName]; e.State != config.AutopilotOff || !strings.Contains(e.Reason, "--prompt-context") {
		t.Fatalf("%+v", e)
	}
}

func TestASteerTrialStopsWhenSwitchedOnEverywhere(t *testing.T) {
	db, dir := onSteerTrial(t, true)
	cfg := config.Default()
	cfg.Optimizers = map[string]string{steerName: config.StateOn}
	res, err := Advance(t.Context(), db, dir, cfg, DefaultPolicy(), hookNow)
	if err != nil {
		t.Fatal(err)
	}
	if e := res.File.Entries[steerName]; e.State != config.AutopilotOff || !strings.Contains(e.Reason, "nothing to compare") {
		t.Fatalf("%+v", e)
	}
}

func TestRuleTrialNameOfTheSteer(t *testing.T) {
	if RuleTrialName(steerName) != steerName || RuleTrialTool(steerName) != steerName {
		t.Error("the steer's trial entry is its own name")
	}
	if RuleTrialName("caveman") != "ext_caveman" {
		t.Error("rule sets keep the ext_ prefix")
	}
	if IsRuleSet(steerName) {
		t.Error("the steer is not a third-party rule set")
	}
}
