package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeModelSetting(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, map[string]string{"CLAUDE_CONFIG_DIR": dir})
	if got := claudeModelSetting(h.app); got != "" {
		t.Errorf("no settings file: %q", got)
	}
	for _, c := range []struct{ body, want string }{
		{`{"model":"opusplan","env":{}}`, "opusplan"},
		{`{"model":"sonnet"}`, "sonnet"},
		{`{"permissions":{}}`, ""},
		{`{not json`, ""},
		{`{"model":42}`, ""},
	} {
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(c.body), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := claudeModelSetting(h.app); got != c.want {
			t.Errorf("%s: %q, want %q", c.body, got, c.want)
		}
	}
}
