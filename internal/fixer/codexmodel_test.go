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

// seedCodexHistory stores 30 Codex history rows on a frontier model, 25 of them
// flagged as small tasks, plus 30 Claude Code rows that must not be mixed in.
func seedCodexHistory(t *testing.T, f *fx) {
	t.Helper()
	db, err := store.Open(f.env.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	finding := `[{"detector":"expensive_model_small_task","tokens":100,"detail":{"alternative":"gpt-mini-test"}}]`
	for i := 0; i < 30; i++ {
		fin := any(nil)
		if i < 25 {
			fin = finding
		}
		if _, err := db.Exec(`INSERT INTO history_requests (request_key, source, session_key, ts, model, findings) VALUES (?, 'codex', 's', ?, 'gpt-big-test', ?)`,
			fmt.Sprintf("c%d", i), now.Add(-24*time.Hour).UnixMilli(), fin); err != nil {
			t.Fatal(err)
		}
	}
	f.env.DB = db.DB
}

const codexConfig = "# my codex config\nsandbox_mode = \"workspace-write\"\n\n[mcp_servers.docs]\ncommand = \"docs-mcp\"\n"

func TestCodexModelDefault(t *testing.T) {
	f := newFx(t)
	seedCodexHistory(t, f)
	if err := os.MkdirAll(f.env.CodexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(f.env.CodexDir, "config.toml")
	if err := os.WriteFile(cfg, []byte(codexConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	fixes := f.detect(ModelDefault)
	if len(fixes) != 1 || fixes[0].Agent != "codex" || !fixes[0].Suggestion || !fixes[0].Applicable() {
		t.Fatalf("want one applicable codex suggestion (and nothing for Claude Code), got %+v", fixes)
	}
	if fixes[0].USDPerSession != nil || fixes[0].TokensPerSession != 0 {
		t.Fatalf("no honest saving exists for a model change, got %+v", fixes[0])
	}
	golden(t, "model_default_codex.diff.golden", fixes[0].Diff)

	// A suggestion is never part of --all.
	if res := f.applyAll(); len(res) != 0 {
		t.Fatalf("a suggestion must not be applied by --all, got %+v", res)
	}
	if f.read(cfg) != codexConfig {
		t.Fatal("config.toml changed without approval")
	}

	// Explicit approval applies it; undo restores it exactly.
	res, err := Apply(context.Background(), f.env, []string{fixes[0].ID})
	if err != nil || len(res) != 1 || res[0].Status != "applied" {
		t.Fatalf("apply: %+v %v", res, err)
	}
	if !strings.Contains(f.read(cfg), `model = "gpt-mini-test"`) {
		t.Fatalf("model not set:\n%s", f.read(cfg))
	}
	if _, err := setup.Undo(f.env.DataDir); err != nil {
		t.Fatal(err)
	}
	if f.read(cfg) != codexConfig {
		t.Fatalf("not byte-identical after undo:\n%q", f.read(cfg))
	}
}

func TestCodexModelDefaultStepsAsideForOverrides(t *testing.T) {
	cases := map[string]struct{ rel, body, want string }{
		"project model": {".codex/config.toml", "model = \"gpt-other\"\n", "project's .codex/config.toml"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			seedCodexHistory(t, f)
			f.write(c.rel, c.body)
			fixes := f.detect(ModelDefault)
			if len(fixes) != 1 || fixes[0].Applicable() || !strings.Contains(fixes[0].Advisory, c.want) {
				t.Fatalf("an override must turn the fix into advice: %+v", fixes)
			}
		})
	}

	t.Run("profile selected", func(t *testing.T) {
		f := newFx(t)
		seedCodexHistory(t, f)
		if err := os.MkdirAll(f.env.CodexDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.env.CodexDir, "config.toml"), []byte("profile = \"work\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		fixes := f.detect(ModelDefault)
		if len(fixes) != 1 || fixes[0].Applicable() || !strings.Contains(fixes[0].Advisory, "profile") {
			t.Fatalf("a selected profile must turn the fix into advice: %+v", fixes)
		}
	})
}

func TestCodexModelDefaultNeedsEnoughSmallTasks(t *testing.T) {
	f := newFx(t)
	db, err := store.Open(f.env.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for i := 0; i < 5; i++ {
		if _, err := db.Exec(`INSERT INTO history_requests (request_key, source, session_key, ts, model, findings) VALUES (?, 'codex', 's', ?, 'gpt-big-test', ?)`,
			fmt.Sprintf("c%d", i), now.Add(-time.Hour).UnixMilli(),
			`[{"detector":"expensive_model_small_task","detail":{"alternative":"gpt-mini-test"}}]`); err != nil {
			t.Fatal(err)
		}
	}
	f.env.DB = db.DB
	if fixes := f.detect(ModelDefault); len(fixes) != 0 {
		t.Fatalf("5 small tasks is not enough evidence, got %+v", fixes)
	}
}
