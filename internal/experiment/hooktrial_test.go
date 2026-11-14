package experiment

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/store"
)

const hookOpt = "hook_repeat_read"

var trialNow = t0.Add(30 * 24 * time.Hour)

// historySessions stores n sessions the way a user with no gateway has them: an assignment from the hooks, and
// history_requests rows from the agent's own logs. A session ran with the optimizer on when the hash arm says
// so; tokens are base, times onFactor for those, with a small deterministic wobble.
func historySessions(t *testing.T, db *store.DB, prefix string, n int, startedAt time.Time, onFactor float64) (on, off int) {
	t.Helper()
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("%s%03d", prefix, i)
		arm := measure.Arm(key, hookOpt)
		factor := 1.0
		if arm == ArmOn {
			factor = onFactor
			on++
		} else {
			off++
		}
		if _, err := db.Exec(`INSERT INTO experiment_assignments(session_key, optimizer, arm, stratum, seq, assigned_at) VALUES(?,?,?,'',0,?)`,
			key, hookOpt, arm, startedAt.UnixMilli()); err != nil {
			t.Fatal(err)
		}
		tokens := int64(100000 * factor * (1 + 0.03*float64(i%7)))
		for r := 0; r < 4; r++ {
			ts := startedAt.Add(time.Duration(i)*time.Hour + time.Duration(r)*time.Minute)
			if _, err := db.Exec(`INSERT INTO history_requests(request_key, source, session_key, ts, input_tokens, cache_read, output_tokens, errors)
				VALUES(?,?,?,?,?,?,?,?)`, fmt.Sprintf("%s-%d", key, r), "claude_code", key, ts.UnixMilli(), tokens/8, tokens/2/4, tokens/8/4, i%2); err != nil {
				t.Fatal(err)
			}
		}
	}
	return on, off
}

func analyzeTrial(t *testing.T, db *store.DB, notBefore time.Time) Result {
	t.Helper()
	r, err := AnalyzeHookTrial(context.Background(), db, hookOpt, testCfg(), notBefore.UnixMilli(), trialNow, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Planted effects in both directions, on data a user with no gateway really has. The comparison has to find
// each one, and has to report no effect when there is none.
func TestHookTrialFindsAPlantedEffectInBothDirections(t *testing.T) {
	for _, c := range []struct {
		name   string
		factor float64
		want   Status
	}{
		{"saves tokens", 0.70, Saves},
		{"uses more tokens", 1.30, CostsMore},
		{"no effect", 1.00, NoClearEffect},
	} {
		db := openDB(t)
		on, off := historySessions(t, db, "h", 120, t0, c.factor)
		if on < 20 || off < 20 {
			t.Fatalf("test data too unbalanced: %d on, %d off", on, off)
		}
		r := analyzeTrial(t, db, t0.Add(-time.Hour))
		if r.Status != c.want || r.Headline != MetricTokens {
			t.Errorf("%s: status %q (%s), want %q", c.name, r.Status, r.Reason, c.want)
		}
		if r.NOn != on || r.NOff != off {
			t.Errorf("%s: counted %d on and %d off, assigned %d and %d", c.name, r.NOn, r.NOff, on, off)
		}
	}
}

func TestHookTrialSaysTokensNotDollars(t *testing.T) {
	db := openDB(t)
	historySessions(t, db, "h", 120, t0, 0.70)
	r := analyzeTrial(t, db, t0.Add(-time.Hour))
	if r.Cost == nil || r.Cost.Metric != MetricTokens || !contains(r.Reason, "fewer tokens") || contains(r.Reason, "cost") {
		t.Errorf("a no-gateway trial must be described in tokens: %+v / %q", r.Cost, r.Reason)
	}
	for _, o := range r.Outcomes {
		if o.Metric == MetricAbandoned {
			t.Error("abandoned sessions have no source without the gateway and must not be compared")
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestHookTrialLeavesOutRunningSessionsAndThoseFromBeforeTheTrial(t *testing.T) {
	db := openDB(t)
	historySessions(t, db, "old", 60, t0.Add(-48*time.Hour), 0.5) // from an earlier trial
	if r := analyzeTrial(t, db, t0); r.NOn+r.NOff != 0 {
		t.Errorf("sessions assigned before the trial were counted: %d", r.NOn+r.NOff)
	}
	// One session whose last request was a minute ago is still running.
	key := "live"
	_, _ = db.Exec(`INSERT INTO experiment_assignments(session_key, optimizer, arm, stratum, seq, assigned_at) VALUES(?,?,?,'',0,?)`, key, hookOpt, ArmOn, t0.UnixMilli())
	_, _ = db.Exec(`INSERT INTO history_requests(request_key, source, session_key, ts, input_tokens) VALUES('l1','claude_code',?,?,10)`, key, trialNow.Add(-time.Minute).UnixMilli())
	r := analyzeTrial(t, db, t0.Add(-time.Hour))
	if r.NOn != 0 || r.Excluded != 1 {
		t.Errorf("a running session must be left out and counted as excluded: on=%d excluded=%d", r.NOn, r.Excluded)
	}
}

// A session the gateway saw is taken from the gateway only, so it is never counted twice.
func TestHookTrialTakesASessionFromTheGatewayWhenThereIsOne(t *testing.T) {
	db := openDB(t)
	historySessions(t, db, "h", 1, t0, 1.0)
	key := "h000"
	if _, err := db.Exec(`INSERT INTO sessions(session_key, agent, started_at, ended_at, request_count, total_input_uncached_tokens, outcome_signals)
		VALUES(?, 'claude_code', ?, ?, 7, 12345, '{"tool_errors":3}')`, key, t0.UnixMilli(), t0.Add(time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	s, ok, err := hookSession(context.Background(), db, key, trialNow.UnixMilli())
	if err != nil || !ok || s.cost != 12345 || s.turns != 7 || s.errs != 3 {
		t.Errorf("gateway figures expected, got %+v ok=%v err=%v", s, ok, err)
	}
}

func TestHookTrialWithNoAssignmentsSaysNotEnoughData(t *testing.T) {
	r := analyzeTrial(t, openDB(t), t0)
	if r.Status != NotEnoughData {
		t.Errorf("status %q", r.Status)
	}
}
