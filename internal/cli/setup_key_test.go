package cli

import (
	"strings"
	"testing"
)

// trove setup uses the same key validator as the gateway and the live probe. Each row is a
// fake key. An accepted key gives no refusal; a refused key gives a plain-English reason, and the
// key's value never appears in the message.
func TestSetupRefusesAnUnacceptableKeyWithAReason(t *testing.T) {
	const fakeBody = "FAKEBODYFORTESTS0123456789abcdefghijklmnop"
	cases := []struct {
		name    string
		key     string
		refused bool
		want    string // a phrase the refusal must contain
	}{
		{"console api03 key is accepted", "sk-ant-api03-" + fakeBody, false, ""},
		{"personal sk-ant-usr key is accepted", "sk-ant-usr01-" + fakeBody, false, ""},
		{"oauth token is refused by name", "sk-ant-oat01-" + fakeBody, true, "OAuth token"},
		{"admin key is refused by name", "sk-ant-admin01-" + fakeBody, true, "Admin API key"},
		{"unknown prefix is refused", "sk-ant-zzz01-" + fakeBody, true, "not one Trove recognises"},
		{"console prefix with a short body is refused", "sk-ant-api03-SHORT", true, "not one Trove recognises"},
		{"an OpenAI key is refused", "sk-proj-" + fakeBody, true, "not one Trove recognises"},
	}
	for _, tc := range cases {
		h := newHarness(t, map[string]string{"ANTHROPIC_API_KEY": tc.key})
		msg := claudeNeedsAPIKey(h.app, t.TempDir(), "")
		if !tc.refused {
			if msg != "" {
				t.Errorf("%s: refused with %q", tc.name, msg)
			}
			continue
		}
		if !strings.HasPrefix(msg, "Not changed.") || !strings.Contains(msg, tc.want) {
			t.Errorf("%s: message %q, want it to contain %q", tc.name, msg, tc.want)
		}
		if strings.Contains(msg, tc.key) {
			t.Errorf("%s: the message echoed the key", tc.name)
		}
	}
}
