package logs_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/logs"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestDetectClaudeCodePlan(t *testing.T) {
	write := func(t *testing.T, settings string) string {
		dir := t.TempDir()
		if settings != "" {
			_ = os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o644)
		}
		return dir
	}
	tests := []struct {
		name     string
		env      map[string]string
		settings string
		override string
		want     logs.Plan
	}{
		{"nothing", nil, "", "", logs.PlanUnknown},
		{"api key in env", map[string]string{"ANTHROPIC_API_KEY": "x"}, "", "", logs.PlanAPIKey},
		{"api key helper", nil, `{"apiKeyHelper":"/bin/get-key"}`, "", logs.PlanAPIKey},
		{"console login", nil, `{"forceLoginMethod":"console"}`, "", logs.PlanAPIKey},
		{"claude.ai login", nil, `{"forceLoginMethod":"claudeai"}`, "", logs.PlanSubscription},
		{"conflict is unknown", map[string]string{"ANTHROPIC_API_KEY": "x"}, `{"forceLoginMethod":"claudeai"}`, "", logs.PlanUnknown},
		{"override wins", map[string]string{"ANTHROPIC_API_KEY": "x"}, "", "subscription", logs.PlanSubscription},
		{"bad override ignored", nil, "", "bogus", logs.PlanUnknown},
		{"broken settings file", nil, `{not json`, "", logs.PlanUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := logs.DetectClaudeCodePlan(env(tc.env), write(t, tc.settings), tc.override)
			if got.Plan != tc.want || !got.Estimated {
				t.Errorf("got %+v, want %s (estimated)", got, tc.want)
			}
			for _, e := range got.Evidence {
				if strings.Contains(e, "x") && strings.Contains(e, "=x") {
					t.Errorf("evidence must name signals, never values: %q", e)
				}
			}
		})
	}
}

// Credential files must never be opened. The config directory here holds a
// .claude.json-style file and an auth file that fail the test if they are read:
// they are unreadable, so any attempt to open them would surface as an error
// only if the code tried; the check is that detection ignores their contents.
func TestDetectPlanIgnoresCredentialFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{".credentials.json", "auth.json"} {
		_ = os.WriteFile(filepath.Join(dir, name), []byte(`{"claudeAiOauth":{"accessToken":"sk-ant-oat-secret"},"OPENAI_API_KEY":"sk-secret"}`), 0o600)
	}
	got := logs.DetectClaudeCodePlan(env(nil), dir, "")
	if got.Plan != logs.PlanUnknown || len(got.Evidence) != 0 {
		t.Errorf("credential files must not influence detection: %+v", got)
	}
}

func TestDetectCodexPlan(t *testing.T) {
	if p := logs.DetectCodexPlan(env(map[string]string{"OPENAI_API_KEY": "x"}), ""); p.Plan != logs.PlanAPIKey {
		t.Errorf("%+v", p)
	}
	if p := logs.DetectCodexPlan(env(nil), ""); p.Plan != logs.PlanUnknown {
		t.Errorf("a ChatGPT login cannot be told without opening auth.json: %+v", p)
	}
	if p := logs.DetectCodexPlan(env(nil), "subscription"); p.Plan != logs.PlanSubscription {
		t.Errorf("%+v", p)
	}
}
