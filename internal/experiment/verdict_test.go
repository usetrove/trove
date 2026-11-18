package experiment

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/store"
)

const ruleOpt = "ext_caveman"

// ruleSessions stores n sessions of a rule-set trial as a subscription user has them: the hooks' arm, history rows
// (priced) and test runs. With the rules on, replies are outFactor as long; every fourth on session ends with
// failing tests when failOn is set.
func ruleSessions(t *testing.T, db *store.DB, n int, outFactor float64, failOn bool) {
	t.Helper()
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("r%03d", i)
		arm := measure.Arm(key, ruleOpt)
		f := 1.0
		if arm == ArmOn {
			f = outFactor
		}
		if _, err := db.Exec(`INSERT INTO experiment_assignments(session_key, optimizer, arm, stratum, seq, assigned_at) VALUES(?,?,?,'',0,?)`,
			key, ruleOpt, arm, t0.UnixMilli()); err != nil {
			t.Fatal(err)
		}
		wobble := 1 + 0.03*float64(i%7)
		for r := 0; r < 5; r++ {
			ts := t0.Add(time.Duration(i)*time.Hour + time.Duration(r)*time.Minute)
			out := int64(2000 * f * wobble)
			if _, err := db.Exec(`INSERT INTO history_requests(request_key, source, session_key, ts, input_tokens, cache_read, output_tokens, cost_usd)
				VALUES(?,?,?,?,?,?,?,?)`, fmt.Sprintf("%s-%d", key, r), "claude_code", key, ts.UnixMilli(), 3000, 40000, out, 0.02+float64(out)*0.000075); err != nil {
				t.Fatal(err)
			}
		}
		code := 0
		if failOn && arm == ArmOn && i%4 == 0 {
			code = 1
		}
		if _, err := db.Exec(`INSERT INTO test_runs(session_key, ts, cmd_hash, exit_code) VALUES(?,?,'h',?)`, key, t0.Add(time.Duration(i)*time.Hour+3*time.Minute).UnixMilli(), code); err != nil {
			t.Fatal(err)
		}
	}
}

// Done when (6.6): the verdict reports output tokens, cost per turn, test-fail rate and turn count.
func TestRuleSetTrialVerdictReportsTheFourFigures(t *testing.T) {
	db := openDB(t)
	ruleSessions(t, db, 120, 0.6, true)
	r, err := AnalyzeHookTrial(t.Context(), db, ruleOpt, testCfg(), t0.Add(-time.Hour).UnixMilli(), trialNow, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if r.OutputPerTurn == nil || r.OutputPerTurn.Value > -0.3 || r.OutputPerTurn.Label != measure.Measured {
		t.Fatalf("output tokens per turn must show the planted 40%% cut, measured: %+v", r.OutputPerTurn)
	}
	if r.CostPerTurn == nil || r.CostPerTurn.High >= 0 {
		t.Fatalf("cost per turn must be measured from the priced history and come out lower: %+v", r.CostPerTurn)
	}
	if r.TestFail == nil || r.TestFail.OnRate <= r.TestFail.OffRate || r.TestFail.OffSessions == 0 {
		t.Fatalf("test-fail rate must show the planted failures on the on side: %+v", r.TestFail)
	}
	if r.outcome(MetricTurns) == nil {
		t.Fatal("turn count missing from the outcomes")
	}
	v := r.TrialVerdict()
	for _, want := range []string{"output tokens per turn: -", "cost per turn: -", "test-fail rate: ", "turns: ", "measured"} {
		if !strings.Contains(v, want) {
			t.Errorf("verdict lacks %q:\n%s", want, v)
		}
	}
}

// A session with an unpriced request has no dollar figure: it is left out of cost per turn, never counted as free.
func TestCostPerTurnLeavesOutUnpricedSessions(t *testing.T) {
	db := openDB(t)
	ruleSessions(t, db, 120, 1.0, false)
	if _, err := db.Exec(`UPDATE history_requests SET cost_usd = NULL WHERE request_key LIKE 'r00%-0'`); err != nil {
		t.Fatal(err)
	}
	r, err := AnalyzeHookTrial(t.Context(), db, ruleOpt, testCfg(), t0.Add(-time.Hour).UnixMilli(), trialNow, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if r.CostPerTurn == nil || r.CostPerTurn.NOn+r.CostPerTurn.NOff != r.NOn+r.NOff-10 {
		t.Fatalf("the 10 sessions with an unpriced request must be left out of cost per turn: %+v (sessions %d)", r.CostPerTurn, r.NOn+r.NOff)
	}
}

func TestTrialVerdictSaysWhenAFigureIsMissing(t *testing.T) {
	v := Result{Status: NotEnoughData, Reason: "few sessions"}.TrialVerdict()
	if strings.Contains(v, "0.0%") || !strings.Contains(v, "not enough sessions") || !strings.Contains(v, "no comparison yet") {
		t.Errorf("a missing figure must be said, never shown as zero:\n%s", v)
	}
}
