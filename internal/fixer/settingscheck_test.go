package fixer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/setup"
	"github.com/usetrove/trove/internal/store"
)

func (f *fx) writeUser(body string) string {
	f.t.Helper()
	if err := os.MkdirAll(f.env.ClaudeDir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	p := filepath.Join(f.env.ClaudeDir, "settings.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
	return p
}

// history gives the fixture a database with n requests, each writing w1h tokens
// at the one-hour rate.
func (f *fx) history(n int, w1h int64) {
	f.t.Helper()
	db, err := store.Open(f.env.DataDir)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = db.Close() })
	for i := 0; i < n; i++ {
		if _, err := db.Exec(`INSERT INTO history_requests (request_key, source, session_key, ts, model, cache_write_1h) VALUES (?, 'claude_code', 's', ?, 'claude-opus-5-5', ?)`,
			fmt.Sprintf("k%d", i), now.Add(-24*time.Hour).UnixMilli(), w1h); err != nil {
			f.t.Fatal(err)
		}
	}
	f.env.DB = db.DB
}

func TestSettingsCheckFlagsSettingsThatTurnCachingOffAndRemovesOnlyThem(t *testing.T) {
	f := newFx(t)
	user := f.writeUser("{\n  \"theme\": \"dark\",\n  \"env\": {\n    \"KEEP\": \"1\",\n    \"DISABLE_PROMPT_CACHING\": \"1\"\n  }\n}\n")
	fixes := f.detect(SettingsCheck)
	if len(fixes) != 1 {
		t.Fatalf("want 1 fix, got %d: %+v", len(fixes), fixes)
	}
	x := fixes[0]
	if !x.Suggestion || !x.Applicable() || x.USDPerSession != nil || x.TokensPerSession != 0 {
		t.Errorf("must be an applicable suggestion with no invented saving: %+v", x)
	}
	if !strings.HasPrefix(x.ID, "chk-") || !strings.Contains(x.Why, "0.1x") || !strings.Contains(x.Why, "on purpose") {
		t.Errorf("id %q, why %q", x.ID, x.Why)
	}
	if !strings.Contains(x.Evidence[0], `env.DISABLE_PROMPT_CACHING is set to "1"`) {
		t.Errorf("evidence %q", x.Evidence)
	}
	// Removing the last member also drops the comma on the line before it, so that
	// line shows in the diff with the same text minus the comma. Nothing else moves.
	if !strings.Contains(x.Diff, `-    "DISABLE_PROMPT_CACHING": "1"`) || !strings.Contains(x.Diff, `+    "KEEP": "1"`) || strings.Contains(x.Diff, `"theme"`) && strings.Contains(x.Diff, `-  "theme"`) {
		t.Errorf("diff touches more than the one setting:\n%s", x.Diff)
	}
	// The file is unchanged until it is applied.
	if got := f.read(user); !strings.Contains(got, "DISABLE_PROMPT_CACHING") {
		t.Fatal("detecting changed the file")
	}
}

func TestSettingsCheckIsNeverPartOfApplyAll(t *testing.T) {
	f := newFx(t)
	user := f.writeUser("{\n  \"env\": {\n    \"DISABLE_PROMPT_CACHING\": \"1\"\n  }\n}\n")
	before := f.read(user)
	for _, r := range f.applyAll() {
		t.Errorf("--all applied %+v; a settings_check is a suggestion and needs its own yes", r)
	}
	if f.read(user) != before {
		t.Fatal("the file changed without an explicit apply")
	}
}

func TestSettingsCheckAppliesWithConsentAndUndoIsByteIdentical(t *testing.T) {
	f := newFx(t)
	orig := "{\r\n  \"env\": {\r\n    \"ENABLE_TOOL_SEARCH\": \"false\",\r\n    \"KEEP\": \"a b\"\r\n  },\r\n  \"theme\": \"dark\"\r\n}\r\n"
	user := f.writeUser(orig)
	fixes := f.detect(SettingsCheck)
	if len(fixes) != 1 {
		t.Fatalf("got %d fixes", len(fixes))
	}
	res, err := Apply(context.Background(), f.env, []string{fixes[0].ID})
	if err != nil || len(res) != 1 || res[0].Status != "applied" {
		t.Fatalf("%v %+v", err, res)
	}
	got := f.read(user)
	if strings.Contains(got, "ENABLE_TOOL_SEARCH") || !strings.Contains(got, `"KEEP": "a b"`) || !strings.Contains(got, `"theme": "dark"`) || !strings.Contains(got, "\r\n") {
		t.Errorf("after apply:\n%q", got)
	}
	if _, err := setup.Undo(f.env.DataDir); err != nil {
		t.Fatal(err)
	}
	if f.read(user) != orig {
		t.Errorf("undo is not byte-identical:\n got %q\nwant %q", f.read(user), orig)
	}
}

// A one-hour cache setting is flagged only when history shows one-hour writes.
func TestSettingsCheckOneHourSettingNeedsEvidenceOfOneHourWrites(t *testing.T) {
	body := "{\n  \"promptCacheTtl\": \"1h\",\n  \"env\": {\n    \"ENABLE_PROMPT_CACHING_1H\": \"1\"\n  }\n}\n"

	f := newFx(t)
	f.writeUser(body)
	if got := f.detect(SettingsCheck); len(got) != 0 {
		t.Fatalf("flagged a one-hour setting with no history at all: %+v", got)
	}

	f = newFx(t)
	f.writeUser(body)
	f.history(5, 0)
	if got := f.detect(SettingsCheck); len(got) != 0 {
		t.Fatalf("flagged a one-hour setting although no one-hour write was recorded: %+v", got)
	}

	f = newFx(t)
	f.writeUser(body)
	f.history(5, 1000)
	got := f.detect(SettingsCheck)
	if len(got) != 2 {
		t.Fatalf("want both one-hour settings flagged, got %d", len(got))
	}
	for _, x := range got {
		if !strings.Contains(strings.Join(x.Evidence, " "), "5000 tokens were written to the cache at the one-hour rate") {
			t.Errorf("evidence lacks the measured one-hour writes: %q", x.Evidence)
		}
		if !strings.Contains(x.Why, "2x") || !strings.Contains(x.Why, "1.25x") {
			t.Errorf("why lacks the billing rates: %q", x.Why)
		}
	}
}

func TestSettingsCheckLeavesHarmlessValuesAndManagedFilesAlone(t *testing.T) {
	f := newFx(t)
	// Caching on, tool search on, five-minute TTL: nothing to flag.
	f.writeUser("{\n  \"promptCacheTtl\": \"5m\",\n  \"env\": {\n    \"DISABLE_PROMPT_CACHING\": \"0\",\n    \"ENABLE_TOOL_SEARCH\": \"auto\"\n  }\n}\n")
	f.history(5, 1000)
	if got := f.detect(SettingsCheck); len(got) != 0 {
		t.Errorf("flagged harmless values: %+v", got)
	}
	// A file that is not strict JSON is never edited.
	f.writeUser("{\n  // comment\n  \"env\": {\"DISABLE_PROMPT_CACHING\": \"1\"}\n}\n")
	if got := f.detect(SettingsCheck); len(got) != 0 {
		t.Errorf("proposed an edit to a file it cannot parse: %+v", got)
	}
}

func TestSettingsCheckInSharedProjectFileSaysSo(t *testing.T) {
	f := newFx(t)
	f.write(".claude/settings.json", "{\n  \"env\": {\n    \"DISABLE_PROMPT_CACHING\": \"1\"\n  }\n}\n")
	fixes := f.detect(SettingsCheck)
	if len(fixes) != 1 {
		t.Fatalf("got %d", len(fixes))
	}
	if !strings.Contains(strings.Join(fixes[0].Notes, " "), "shared with everyone") {
		t.Errorf("a project file is shared; notes: %q", fixes[0].Notes)
	}
}
