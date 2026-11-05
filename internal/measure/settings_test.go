package measure

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
)

// seedProfile records an applied settings profile the way the fixer does.
func seedProfile(t *testing.T, exec func(string, ...any), harness, task, keys string, applied time.Time) {
	t.Helper()
	exec(`INSERT INTO fixes(change_id, fix_id, kind, agent, path_hash, applied_at, est_tokens, label)
		VALUES('c1', 'settings-1', 'settings_profile', ?, 'h', ?, 0, 'estimated')`, harness, applied.UnixMilli())
	exec(`INSERT INTO settings_profiles(change_id, fix_id, task, harness, keys) VALUES('c1', 'settings-1', ?, ?, ?)`, task, harness, keys)
}

// seedOutputSessions stores n sessions with the given output tokens, tool
// errors and turns, a little varied so there is a spread.
func seedOutputSessions(t *testing.T, exec func(string, ...any), n int, start time.Time, output, toolErrors int, tag string) {
	t.Helper()
	for i := 0; i < n; i++ {
		s := start.Add(time.Duration(i) * time.Hour)
		sig := fmt.Sprintf(`{"turns":10,"has_tools":true,"tool_errors":%d,"abandoned_after_action":false}`, toolErrors+i%2)
		exec(`INSERT INTO sessions(session_key, agent, started_at, ended_at, request_count, total_output_tokens, total_cache_read_tokens, outcome_signals)
			VALUES(?, 'claude_code', ?, ?, 10, ?, 1000, ?)`, fmt.Sprintf("%s-%d", tag, i), s.UnixMilli(), s.Add(10*time.Minute).UnixMilli(), int64(output+(i%3)*20), sig)
	}
}

func execOn(t *testing.T) (func(string, ...any), func() ([]SettingsEffect, error)) {
	db := openDB(t)
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := db.Exec(q, a...); err != nil {
			t.Fatal(err)
		}
	}
	return exec, func() ([]SettingsEffect, error) {
		return SettingsEffects(context.Background(), db, config.Default().Measure)
	}
}

func TestSettingsEffectNeedsEnoughSessions(t *testing.T) {
	exec, run := execOn(t)
	applied := t0.Add(48 * time.Hour)
	seedProfile(t, exec, "claude_code", "quick-fix", `["outputStyle"]`, applied)
	seedOutputSessions(t, exec, 3, t0, 2000, 1, "b")
	seedOutputSessions(t, exec, 3, applied.Add(time.Hour), 1000, 1, "a")
	fx, err := run()
	if err != nil || len(fx) != 1 {
		t.Fatalf("%v %+v", err, fx)
	}
	e := fx[0]
	if e.Label != Estimated || e.Verdict != VerdictNotEnough || e.Usage != nil || !strings.Contains(e.Text, TextNotEnough) {
		t.Fatalf("few sessions must show no effect: %+v", e)
	}
}

func TestSettingsEffectLessUsageNoWorseOutcomes(t *testing.T) {
	exec, run := execOn(t)
	applied := t0.Add(48 * time.Hour)
	seedProfile(t, exec, "claude_code", "quick-fix", `["outputStyle"]`, applied)
	seedOutputSessions(t, exec, 25, t0.Add(-30*time.Hour), 2000, 1, "b")
	seedOutputSessions(t, exec, 25, applied.Add(10*time.Hour), 1500, 1, "a")
	fx, err := run()
	if err != nil {
		t.Fatal(err)
	}
	e := fx[0]
	if e.Label != Measured || e.Verdict != VerdictLessUsage || e.Usage == nil || e.Usage.Percent > -20 || e.Usage.Percent < -30 {
		t.Fatalf("want a measured ~25%% drop in output, got %+v", e)
	}
	if e.OutcomeVerdict != OutcomeNoClearChange || e.Recommend != "" {
		t.Fatalf("outcomes did not change: %+v", e)
	}
	for _, want := range []string{"outputStyle for quick-fix", "less output per session", "measured", "n=25 before / 25 after", "does not see whether tests pass", "not just this project or task"} {
		if !strings.Contains(e.Text, want) {
			t.Errorf("text misses %q: %s", want, e.Text)
		}
	}
}

func TestSettingsEffectWorseOutcomesRecommendUndo(t *testing.T) {
	exec, run := execOn(t)
	applied := t0.Add(48 * time.Hour)
	seedProfile(t, exec, "claude_code", "quick-fix", `["outputStyle"]`, applied)
	seedOutputSessions(t, exec, 25, t0.Add(-30*time.Hour), 2000, 1, "b")
	seedOutputSessions(t, exec, 25, applied.Add(10*time.Hour), 1500, 6, "a") // cheaper, but many more tool errors
	fx, _ := run()
	e := fx[0]
	if e.OutcomeVerdict != OutcomeWorse || e.Recommend != "trove undo settings-1" || !strings.Contains(e.Text, "outcomes got worse: tool errors per session") {
		t.Fatalf("worse outcomes must be called out with the undo command: %+v", e)
	}
}

func TestSettingsEffectCodexProfileIsNotMeasurable(t *testing.T) {
	exec, run := execOn(t)
	applied := t0.Add(48 * time.Hour)
	seedProfile(t, exec, "codex", "feature", `["model_reasoning_summary"]`, applied)
	fx, _ := run()
	e := fx[0]
	if e.Verdict != VerdictNotMeasurable || e.Usage != nil || e.Label != Estimated || !strings.Contains(e.Text, "--profile") {
		t.Fatalf("a codex profile cannot be compared before/after: %+v", e)
	}
}

func TestSettingsEffectIgnoresRevertedAndOtherHarness(t *testing.T) {
	exec, run := execOn(t)
	applied := t0.Add(48 * time.Hour)
	seedProfile(t, exec, "claude_code", "quick-fix", `["outputStyle"]`, applied)
	exec(`UPDATE fixes SET reverted_at = ?`, applied.Add(time.Hour).UnixMilli())
	if fx, err := run(); err != nil || len(fx) != 0 {
		t.Fatalf("an undone profile must not be reported: %v %+v", err, fx)
	}
}

func TestFixEffectsLeavesSettingsToSettingsEffects(t *testing.T) {
	db := openDB(t)
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := db.Exec(q, a...); err != nil {
			t.Fatal(err)
		}
	}
	seedProfile(t, exec, "claude_code", "quick-fix", `["outputStyle"]`, t0)
	fx, err := FixEffects(context.Background(), db, config.Default().Measure)
	if err != nil || len(fx) != 0 {
		t.Fatalf("a settings profile is reported by SettingsEffects, not as a prompt-size fix: %v %+v", err, fx)
	}
	se, err := SettingsEffects(context.Background(), db, config.Default().Measure)
	if err != nil || len(se) != 1 {
		t.Fatalf("%v %+v", err, se)
	}
}
