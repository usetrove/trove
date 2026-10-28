package agent

import (
	"net/http"
	"testing"
)

func TestLabel(t *testing.T) {
	tests := []struct {
		name, ua, sid, label, ver string
	}{
		{"claude by header", "whatever", "abc", ClaudeCode, ""},
		{"claude by ua", "claude-cli/2.1.300 (external, cli)", "", ClaudeCode, "2.1.300"},
		{"codex", "codex_cli_rs/0.140.0 (Windows)", "", Codex, "0.140.0"},
		{"python sdk", "Anthropic/Python 0.70.1", "", SDK, "0.70.1"},
		{"node sdk", "OpenAI/JS 5.2.0", "", SDK, "5.2.0"},
		{"curl", "curl/8.5.0", "", Other, ""},
		{"empty", "", "", Other, ""},
	}
	for _, tc := range tests {
		h := http.Header{}
		h.Set("User-Agent", tc.ua)
		if tc.sid != "" {
			h.Set("X-Claude-Code-Session-Id", tc.sid)
		}
		l, v := Label(h)
		if l != tc.label || v != tc.ver {
			t.Errorf("%s: got %q %q, want %q %q", tc.name, l, v, tc.label, tc.ver)
		}
	}
}
