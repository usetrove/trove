package redact

import (
	"strings"
	"testing"
)

// Credential shapes other than sk- keys and JWTs are redacted even with no sensitive name beside them (F-26).
// The values below are made up, in the documented shapes; none is a real credential.
func TestOtherCredentialShapesAreRedactedWithoutAName(t *testing.T) {
	pem := "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEAxxxxxxxxxxxxxxxxxxxx\nyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy\n-----END RSA PRIVATE KEY-----" // secretscan:allow
	cases := []struct{ name, secret string }{
		{"AWS access key id", "AKIAIOSFODNN7EXAMPLE"},
		{"AWS temporary key id", "ASIAIOSFODNN7EXAMPLE"},
		{"GitHub personal token", "ghp_" + strings.Repeat("a1B2", 9)},
		{"GitHub OAuth token", "gho_" + strings.Repeat("Z9y8", 9)},
		{"GitHub fine-grained token", "github_pat_" + strings.Repeat("q1w2e3r4t5", 4)},
		{"Google API key", "AIzaSyA-1234567890abcdefghijklmnopqrstuv"},
		{"Slack token", "xox" + "b-1234567890-abcdefghijklmnop"}, // split so no secret-shaped literal sits in the source
		{"Stripe live key", "sk_live_" + strings.Repeat("a1b2", 6)},
		{"npm token", "npm_" + strings.Repeat("A1b2C3", 6)},
		{"private key block", pem},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := String("failed to call the service with " + c.secret + " and then retried")
			if strings.Contains(got, c.secret) || (c.name == "private key block" && strings.Contains(got, "MIIEow")) {
				t.Fatalf("the credential survived: %q", got)
			}
			if !strings.Contains(got, Placeholder) || !strings.Contains(got, "and then retried") {
				t.Fatalf("the rest of the text must survive: %q", got)
			}
		})
	}
}

func TestOrdinaryTextSurvivesTheNewShapes(t *testing.T) {
	for _, s := range []string{
		"AKIA is a prefix", "ghp_short", "the key AIza is short", "npm_install failed", "sk_live is a mode",
		"file xoxo-notes.txt", "BEGIN PRIVATE is not a block",
	} {
		if got := String(s); got != s {
			t.Errorf("%q was changed to %q", s, got)
		}
	}
}

// An unterminated private key block (a log cut off in the middle) still loses everything after the BEGIN line.
func TestAnUnterminatedPrivateKeyBlockIsRedactedToTheEnd(t *testing.T) {
	got := String("before\n-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASC\nmore base64 here") // secretscan:allow
	if strings.Contains(got, "MIIEvQ") || strings.Contains(got, "more base64") || !strings.HasPrefix(got, "before") {
		t.Fatalf("%q", got)
	}
}
