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

const trialOptimizer = "hook_repeat_read"

// trialStart is when the optimizer entered probation in these tests.
var trialStart = hookNow.AddDate(0, 0, -20)

// trialSessions stores n finished sessions the way a user with no gateway has them: the arm the hooks recorded
// at session start (a hash of the key), and the requests from the agent's own history. A session that ran with
// the optimizer on uses onFactor times the tokens, with a small deterministic wobble. It returns how many ran on
// each side.
func trialSessions(t *testing.T, db *store.DB, prefix string, n int, at time.Time, onFactor float64) (on, off int) {
	t.Helper()
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("%s%03d", prefix, i)
		arm := measure.Arm(key, trialOptimizer)
		factor := 1.0
		if arm == experiment.ArmOn {
			factor = onFactor
			on++
		} else {
			off++
		}
		if _, err := db.Exec(`INSERT INTO experiment_assignments(session_key, optimizer, arm, stratum, seq, assigned_at) VALUES(?,?,?,'',0,?)`,
			key, trialOptimizer, arm, at.UnixMilli()); err != nil {
			t.Fatal(err)
		}
		tokens := int64(100000 * factor * (1 + 0.03*float64(i%7)))
		for r := 0; r < 4; r++ {
			ts := at.Add(time.Duration(i)*time.Hour + time.Duration(r)*time.Minute)
			if _, err := db.Exec(`INSERT INTO history_requests(request_key, source, session_key, ts, input_tokens, cache_read, output_tokens, errors)
				VALUES(?,?,?,?,?,?,?,?)`, fmt.Sprintf("%s-%d", key, r), "claude_code", key, ts.UnixMilli(), tokens/8, tokens/2/4, tokens/8/4, i%2); err != nil {
				t.Fatal(err)
			}
		}
	}
	return on, off
}

// onTrial sets up a user with hooks and no gateway (the subscription case), with the optimizer already in probation since trialStart.
func onTrial(t *testing.T) (*store.DB, string) {
	t.Helper()
	db, dir := open(t)
	setInstall(t, db, 45)
	hookSessions(t, db, trialOptimizer, 25, 500)
	probed(t, dir)
	v2File(t, dir)
	f, err := config.LoadAutopilot(dir)
	if err != nil {
		t.Fatal(err)
	}
	f.Entries[trialOptimizer] = config.AutopilotEntry{State: config.AutopilotProbation, SinceMS: trialStart.UnixMilli(), Attempts: 1}
	if err := config.SaveAutopilot(dir, f); err != nil {
		t.Fatal(err)
	}
	return db, dir
}

func advanceTrial(t *testing.T, db *store.DB, dir string, at time.Time) config.AutopilotEntry {
	t.Helper()
	res, err := Advance(context.Background(), db, dir, config.Default(), PolicyV2(), at)
	if err != nil {
		t.Fatal(err)
	}
	return res.File.Entries[trialOptimizer]
}

// The point of the whole step: a clean measured saving over enough sessions keeps the optimizer.
func TestACleanTrialKeepsTheOptimizer(t *testing.T) {
	db, dir := onTrial(t)
	trialSessions(t, db, "w", 160, trialStart.Add(24*time.Hour), 0.70) // 30% fewer tokens with it on
	e := advanceTrial(t, db, dir, hookNow)
	if e.State != config.AutopilotOn || e.Attempts != 1 || !strings.Contains(e.Reason, "clean trial") {
		t.Fatalf("after a clean saving: %+v", e)
	}
	f, _ := config.LoadAutopilot(dir)
	if got := f.States()[trialOptimizer]; got != config.StateOn {
		t.Errorf("once kept it is on for every reader, got %q", got)
	}
}

func TestATrialThatCostsMoreIsRolledBack(t *testing.T) {
	db, dir := onTrial(t)
	trialSessions(t, db, "w", 160, trialStart.Add(24*time.Hour), 1.30) // 30% more tokens with it on
	e := advanceTrial(t, db, dir, hookNow)
	if e.State != config.AutopilotRolledBack || !strings.Contains(e.Reason, "worse") {
		t.Fatalf("after a measured loss: %+v", e)
	}
	f, _ := config.LoadAutopilot(dir)
	if got := f.States()[trialOptimizer]; got == config.StateOn {
		t.Error("a rolled-back optimizer must not be on")
	}
}

func TestATrialWithNoDifferenceWaitsThenGivesUp(t *testing.T) {
	db, dir := onTrial(t)
	trialSessions(t, db, "w", 160, trialStart.Add(24*time.Hour), 1.0)
	if e := advanceTrial(t, db, dir, hookNow); e.State != config.AutopilotProbation {
		t.Fatalf("no clear effect yet, inside the deadline: %+v", e)
	}
	late := trialStart.AddDate(0, 0, 29)
	if e := advanceTrial(t, db, dir, late); e.State != config.AutopilotShadow || !strings.Contains(e.Reason, "no measurable benefit") {
		t.Fatalf("at the deadline: %+v", e)
	}
}

func TestATrialWithTooFewSessionsKeepsRunning(t *testing.T) {
	db, dir := onTrial(t)
	trialSessions(t, db, "w", 12, trialStart.Add(24*time.Hour), 0.50) // a big saving, but about 6 sessions a side
	e := advanceTrial(t, db, dir, hookNow)
	if e.State != config.AutopilotProbation || !strings.Contains(e.Reason, "trial running") {
		t.Fatalf("too few sessions must not decide anything: %+v", e)
	}
}

// Sessions assigned before this trial began belong to an earlier attempt and must not carry this one.
func TestSessionsFromBeforeTheTrialDoNotCount(t *testing.T) {
	db, dir := onTrial(t)
	trialSessions(t, db, "old", 160, trialStart.Add(-10*24*time.Hour), 0.50) // a great saving, but earlier
	e := advanceTrial(t, db, dir, hookNow)
	if e.State == config.AutopilotOn {
		t.Fatalf("an earlier trial's sessions promoted this one: %+v", e)
	}
	if !strings.Contains(e.Reason, "0 sessions with it on and 0 with it off") {
		t.Errorf("the trial should have seen none of them: %q", e.Reason)
	}
}

// If reading the comparison fails, nothing is kept: the error is returned and the state is left as it was.
func TestAFailureReadingTheTrialKeepsNothing(t *testing.T) {
	db, dir := onTrial(t)
	trialSessions(t, db, "w", 160, trialStart.Add(24*time.Hour), 0.70)
	if _, err := db.Exec(`DROP TABLE experiment_assignments`); err != nil {
		t.Fatal(err)
	}
	res, err := Advance(context.Background(), db, dir, config.Default(), PolicyV2(), hookNow)
	if err == nil {
		t.Fatal("want an error when the comparison cannot be read")
	}
	f, _ := config.LoadAutopilot(dir)
	if got := f.Entries[trialOptimizer].State; got != config.AutopilotProbation || res.File.Entries[trialOptimizer].State == config.AutopilotOn {
		t.Errorf("state after a failed read: %q", got)
	}
}
