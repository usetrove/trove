package measure

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
)

func seedTaskInstall(exec func(string, ...any), feature string, applied time.Time) {
	exec(`INSERT INTO fixes(change_id, fix_id, kind, agent, path_hash, applied_at, est_tokens, label)
		VALUES('c1', 'scout-1', ?, 'claude_code', 'h', ?, 0, 'estimated')`, feature, applied.UnixMilli())
	exec(`INSERT INTO task_installs(change_id, fix_id, feature, role, overhead_request, overhead_use) VALUES('c1', 'scout-1', ?, 'scout', 60, 200)`, feature)
}

// seedHistory stores n sessions. Each has a main request and, when side is set,
// a subagent request on that model. cost varies a little so there is a spread.
func seedHistory(exec func(string, ...any), n int, start time.Time, cost float64, side, tag string, sideSecond string) {
	for i := 0; i < n; i++ {
		k := fmt.Sprintf("%s-%d", tag, i)
		ts := start.Add(time.Duration(i) * time.Hour)
		exec(`INSERT INTO history_requests(request_key, source, session_key, ts, model, input_tokens, output_tokens, cost_usd, sidechain, errors)
			VALUES(?, 'claude_code', ?, ?, 'main-model', 900, 100, ?, 0, 0)`, k+"m", k, ts.UnixMilli(), cost+float64(i%3)*0.01)
		if side != "" {
			exec(`INSERT INTO history_requests(request_key, source, session_key, ts, model, input_tokens, output_tokens, cost_usd, sidechain, errors)
				VALUES(?, 'claude_code', ?, ?, ?, 900, 100, 0.01, 1, 0)`, k+"s", k, ts.Add(time.Minute).UnixMilli(), side)
		}
		if sideSecond != "" {
			exec(`INSERT INTO history_requests(request_key, source, session_key, ts, model, input_tokens, output_tokens, cost_usd, sidechain, errors)
				VALUES(?, 'claude_code', ?, ?, ?, 900, 100, 0.01, 1, 0)`, k+"e", k, ts.Add(2*time.Minute).UnixMilli(), sideSecond)
		}
	}
}

func tasksRun(t *testing.T) (func(string, ...any), func() []TasksEffect) {
	db := openDB(t)
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := db.Exec(q, a...); err != nil {
			t.Fatal(err)
		}
	}
	return exec, func() []TasksEffect {
		t.Helper()
		fx, err := TasksEffects(context.Background(), db, config.Default().Measure)
		if err != nil {
			t.Fatal(err)
		}
		return fx
	}
}

func TestTasksEffectNeedsEnoughSessionsButStillCountsDelegation(t *testing.T) {
	exec, run := tasksRun(t)
	applied := t0.Add(48 * time.Hour)
	seedTaskInstall(exec, "task_scout", applied)
	seedHistory(exec, 3, t0, 0.50, "", "b", "")
	seedHistory(exec, 3, applied.Add(time.Hour), 0.30, "haiku-model", "a", "")
	e := run()[0]
	if e.Label != Estimated || e.Verdict != VerdictNotEnough || e.Cost != nil || !strings.Contains(e.Text, TextNotEnough) {
		t.Fatalf("few sessions must show no cost effect: %+v", e)
	}
	if e.DelegationRate == nil || *e.DelegationRate != 100 || e.FinishedOn["haiku-model"] != 3 {
		t.Fatalf("delegation is a plain count and may be shown: %+v", e)
	}
	if !strings.Contains(e.Text, "Overhead charged: about 60 tokens per request") {
		t.Errorf("overhead must be charged and shown: %s", e.Text)
	}
}

func TestTasksEffectLowerCostIsMeasuredAndHedged(t *testing.T) {
	exec, run := tasksRun(t)
	applied := t0.Add(48 * time.Hour)
	seedTaskInstall(exec, "task_scout", applied)
	seedHistory(exec, 25, t0.Add(-30*time.Hour), 0.60, "", "b", "")
	seedHistory(exec, 25, applied.Add(10*time.Hour), 0.30, "haiku-model", "a", "")
	e := run()[0]
	if e.Label != Measured || e.Verdict != VerdictLessUsage || e.Cost == nil || e.Cost.Percent > -40 {
		t.Fatalf("want a measured drop in cost, got %+v", e)
	}
	if e.Recommend != "" {
		t.Errorf("nothing got worse: %+v", e)
	}
	for _, want := range []string{"lower cost per session", "(measured, n=25 before / 25 after", "does not see whether tests pass", "not just this project or task", "supports but does not prove"} {
		if !strings.Contains(e.Text, want) {
			t.Errorf("text misses %q:\n%s", want, e.Text)
		}
	}
}

func TestTasksCascadeReportsEscalationAndFinishingModel(t *testing.T) {
	exec, run := tasksRun(t)
	applied := t0.Add(48 * time.Hour)
	seedTaskInstall(exec, "task_cascade", applied)
	seedHistory(exec, 4, t0, 0.5, "", "b", "")
	seedHistory(exec, 6, applied.Add(time.Hour), 0.5, "cheap", "ok", "")
	seedHistory(exec, 4, applied.Add(20*time.Hour), 0.5, "cheap", "esc", "strong")
	e := run()[0]
	if e.Escalated == nil || *e.Escalated != 40 {
		t.Fatalf("4 of 10 sessions escalated: %+v", e.Escalated)
	}
	if e.FinishedOn["cheap"] != 6 || e.FinishedOn["strong"] != 4 {
		t.Fatalf("the model of the last subagent request finished the session: %+v", e.FinishedOn)
	}
}

func TestTasksCostRiseRecommendsUndo(t *testing.T) {
	exec, run := tasksRun(t)
	applied := t0.Add(48 * time.Hour)
	seedTaskInstall(exec, "task_cascade", applied)
	seedHistory(exec, 25, t0.Add(-30*time.Hour), 0.30, "", "b", "")
	seedHistory(exec, 25, applied.Add(10*time.Hour), 0.60, "cheap", "a", "strong")
	e := run()[0]
	if e.Verdict != VerdictMoreUsage || e.Recommend != "trove undo scout-1" {
		t.Fatalf("a clear cost rise must suggest undo, never undo itself: %+v", e)
	}
}

func TestTasksNothingInstalledNothingReported(t *testing.T) {
	_, run := tasksRun(t)
	if fx := run(); len(fx) != 0 {
		t.Fatalf("%+v", fx)
	}
}
