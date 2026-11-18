package fixer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/setup"
	"github.com/usetrove/trove/internal/store"
)

// seedTTLHistory stores 30 Claude Code sessions whose main conversation wrote
// its cache at one hour although every request followed within two minutes, so
// the five-minute lifetime is clearly cheaper.
func seedTTLHistory(t *testing.T, f *fx, plan string) {
	t.Helper()
	db, err := store.Open(f.env.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	tbl, err := pricing.Default()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for i := 0; i < 30; i++ {
		at := now.Add(-5 * 24 * time.Hour).Add(time.Duration(i) * time.Hour)
		for r := 0; r < 11; r++ {
			w1, rd := int64(500), int64(60000)
			if r == 0 {
				w1, rd = 60000, 0
			}
			n++
			if _, err := db.Exec(`INSERT INTO history_requests (request_key, source, session_key, ts, model, cache_write_1h, cache_read, plan, sidechain)
				VALUES (?, 'claude_code', ?, ?, 'claude-sonnet-5-5', ?, ?, ?, 0)`,
				fmt.Sprintf("k%d", n), fmt.Sprintf("s%d", i), at.Add(time.Duration(2*r)*time.Minute).UnixMilli(), w1, rd, plan); err != nil {
				t.Fatal(err)
			}
		}
	}
	f.env.DB, f.env.Pricing = db.DB, tbl
}

func TestCacheTTLFixWritesTheSettingWithUndo(t *testing.T) {
	f := newFx(t)
	seedTTLHistory(t, f, "api_key")
	if err := os.MkdirAll(f.env.ClaudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(f.env.ClaudeDir, "settings.json")
	orig := "{\n  \"theme\": \"dark\"\n}\n"
	if err := os.WriteFile(settings, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}

	fixes := f.detect(CacheTTLMain)
	if len(fixes) != 1 {
		t.Fatalf("want one main-conversation fix, got %+v", fixes)
	}
	fx := fixes[0]
	if !fx.Applicable() || !fx.Suggestion || fx.Class != "cache_control" || fx.Tier != "T1" {
		t.Fatalf("want an applicable T1 cache_control suggestion: %+v", fx)
	}
	if !strings.Contains(fx.Diff, `"promptCacheTtl": "5m"`) || !strings.Contains(fx.Diff, `"theme": "dark"`) {
		t.Fatalf("diff:\n%s", fx.Diff)
	}
	if fx.USDPerSession == nil || *fx.USDPerSession <= 0 || fx.EvidenceLabel != "estimated" {
		t.Fatalf("an API-key history should carry a per-session estimate: %+v", fx)
	}
	if len(f.detect(CacheTTLSubagents)) != 0 {
		t.Fatal("no subagent history, so no subagent fix")
	}

	res, err := Apply(context.Background(), f.env, []string{fx.ID})
	if err != nil || len(res) != 1 || res[0].Status != "applied" {
		t.Fatalf("%v %+v", err, res)
	}
	if got := f.read(settings); !strings.Contains(got, `"promptCacheTtl": "5m"`) {
		t.Fatalf("setting not written:\n%s", got)
	}
	if _, err := setup.UndoSelected(f.env.DataDir, []string{res[0].Change.ID}); err != nil {
		t.Fatal(err)
	}
	if got := f.read(settings); got != orig {
		t.Fatalf("undo did not restore the file byte for byte:\n%s", got)
	}
}

func TestCacheTTLFixOnSubscriptionCarriesNoDollarFigure(t *testing.T) {
	f := newFx(t)
	seedTTLHistory(t, f, "max")
	fixes := f.detect(CacheTTLMain)
	if len(fixes) != 1 || fixes[0].USDPerSession != nil || !fixes[0].Applicable() {
		t.Fatalf("got %+v", fixes)
	}
}

func TestCacheTTLFixStepsAsideForAProjectSetting(t *testing.T) {
	f := newFx(t)
	seedTTLHistory(t, f, "api_key")
	f.write(".claude/settings.json", `{"promptCacheTtl": "1h"}`)
	fixes := f.detect(CacheTTLMain)
	if len(fixes) != 1 || fixes[0].Applicable() || !strings.Contains(fixes[0].Advisory, "promptCacheTtl") {
		t.Fatalf("a project setting wins, so the fix must be advice only: %+v", fixes)
	}
}

func TestCacheTTLFixNeedsHistory(t *testing.T) {
	f := newFx(t)
	if got := f.detect(CacheTTLMain); len(got) != 0 {
		t.Fatalf("no database, no fix: %+v", got)
	}
}

// After a cache lifetime fix sets one hour, settings_check must not offer to
// delete that same setting.
func TestSettingsCheckLeavesALifetimeTroveSetAlone(t *testing.T) {
	f := newFx(t)
	seedTTLHistory(t, f, "api_key") // one-hour writes in history
	if err := os.MkdirAll(f.env.ClaudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.env.ClaudeDir, "settings.json"), []byte(`{"promptCacheTtl": "1h"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	has := func() bool {
		for _, x := range f.detect(SettingsCheck) {
			if strings.Contains(x.Title, "promptCacheTtl") {
				return true
			}
		}
		return false
	}
	if !has() {
		t.Fatal("without a cache lifetime fix in place, settings_check should flag the one-hour setting")
	}
	if _, err := f.env.DB.Exec(`INSERT INTO fixes (change_id, fix_id, kind, agent, path_hash, applied_at, est_tokens, label)
		VALUES ('c', 'ttl-x', ?, 'claude_code', 'h', 1, 0, 'estimated')`, CacheTTLMain); err != nil {
		t.Fatal(err)
	}
	if has() {
		t.Fatal("settings_check offered to delete a lifetime a cache lifetime fix set")
	}
}
