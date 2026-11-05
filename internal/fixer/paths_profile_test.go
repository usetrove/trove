package fixer

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestAllowedCodexTroveProfileFiles(t *testing.T) {
	f := newFx(t)
	e := f.env
	if err := e.Allowed(filepath.Join(e.CodexDir, "trove-quick-fix.config.toml")); err != nil {
		t.Errorf("a Trove-named profile in the Codex folder should be allowed: %v", err)
	}
	bad := map[string]string{
		// A profile the user made is never touched.
		filepath.Join(e.CodexDir, "mine.config.toml"): "not one of",
		// Right name, wrong folder.
		filepath.Join(f.proj, ".codex", "trove-quick.config.toml"): "not one of",
		filepath.Join(e.Home, "trove-quick.config.toml"):           "not one of",
		// Names that merely look similar.
		filepath.Join(e.CodexDir, "trove-.config.toml.bak"):    "not one of",
		filepath.Join(e.CodexDir, "trove-Quick.config.toml"):   "not one of",
		filepath.Join(e.CodexDir, "trove-a/../auth.json"):      "credentials",
		filepath.Join(e.CodexDir, "trove-managed_config.toml"): "not one of",
	}
	for p, want := range bad {
		if err := e.Allowed(p); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want a refusal mentioning %q, got %v", p, want, err)
		}
	}
}
