package report

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/measure"
)

func TestReportShowsSettingsProfiles(t *testing.T) {
	r := newReport(mustPeriod(t, Month), false)
	r.State, r.Requests = StateOK, 100
	r.SettingsProfiles = []measure.SettingsEffect{
		{FixID: "settings-1", Task: "quick-fix", Harness: "claude_code", Keys: []string{"outputStyle"}, AppliedAt: now,
			NBefore: 34, NAfter: 30, Label: measure.Measured, Verdict: measure.VerdictLessUsage, OutcomeVerdict: measure.OutcomeNoClearChange,
			Text: "outputStyle for quick-fix: 18% less output per session (measured, n=34 before / 30 after sessions); no clear change in tool errors, turns or abandoned sessions."},
		{FixID: "settings-2", Task: "feature", Harness: "claude_code", Keys: []string{"outputStyle"}, AppliedAt: now,
			Verdict: measure.VerdictLessUsage, OutcomeVerdict: measure.OutcomeWorse, Label: measure.Measured,
			Text: "outputStyle for feature: outcomes got worse: tool errors per session.", Recommend: "trove undo settings-2"},
	}
	out := strings.Join(strings.Fields(Render(r, Options{})), " ") // lines wrap; compare words
	for _, want := range []string{"Settings profiles you applied", "settings-1 (quick-fix, claude_code)", "18% less output per session (measured, n=34 before / 30 after sessions)",
		"Trove suggests undoing it: trove undo settings-2"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	r.SettingsProfiles = nil
	if strings.Contains(Render(r, Options{}), "Settings profiles") {
		t.Error("the section must not appear when no profile was applied")
	}
}

func TestBuildIncludesSettingsProfilesLabeled(t *testing.T) {
	db := openDB(t)
	seedSmall(t, db) // a few requests in the period, so the report is not in its empty state
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := db.Exec(q, a...); err != nil {
			t.Fatal(err)
		}
	}
	applied := now.Add(-10 * 24 * time.Hour)
	exec(`INSERT INTO fixes(change_id, fix_id, kind, agent, path_hash, applied_at, est_tokens, label)
		VALUES('c1', 'settings-1', 'settings_profile', 'claude_code', 'h', ?, 0, 'estimated')`, applied.UnixMilli())
	exec(`INSERT INTO settings_profiles(change_id, fix_id, task, harness, keys) VALUES('c1', 'settings-1', 'quick-fix', '["outputStyle"]', '["outputStyle"]')`)
	exec(`UPDATE settings_profiles SET harness = 'claude_code'`)
	seed := func(n int, start time.Time, output int, tag string) {
		for i := 0; i < n; i++ {
			s := start.Add(time.Duration(i) * time.Hour)
			exec(`INSERT INTO sessions(session_key, agent, started_at, ended_at, request_count, total_output_tokens, total_cache_read_tokens)
				VALUES(?, 'claude_code', ?, ?, 10, ?, 1000)`, fmt.Sprintf("%s-%d", tag, i), s.UnixMilli(), s.Add(10*time.Minute).UnixMilli(), output+(i%3)*20)
		}
	}
	seed(25, applied.Add(-40*time.Hour), 2000, "b")
	seed(25, applied.Add(time.Hour), 1500, "a")
	r, err := Build(context.Background(), db, Inputs{Config: config.Default(), Now: now}, mustPeriod(t, Month))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.SettingsProfiles) != 1 || r.SettingsProfiles[0].Label != measure.Measured || r.SettingsProfiles[0].Verdict != measure.VerdictLessUsage {
		t.Fatalf("want one measured profile with less usage: %+v", r.SettingsProfiles)
	}
	assertLabeled(t, r)
	out := Render(r, Options{})
	if !strings.Contains(out, "Settings profiles you applied") || !strings.Contains(out, "less output per session") {
		t.Errorf("report:\n%s", out)
	}
}
