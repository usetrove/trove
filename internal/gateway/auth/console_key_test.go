package auth

import "testing"

// Every key in this file is a fake built from a fixed body. No real credential
// is written into the repository.
const (
	fakeBody  = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJ" // 46 key characters
	fakeMin   = "abcdefghijklmnopqrstuvwxyz012345"               // exactly 32 characters
	fakeShort = "abcdefghijklmnopqrstuvwxyz01234"                // 31 characters
)

// Each row is one case. ok is the expected verdict and reason the expected
// reason label when ok is false. The table is the specification: the gateway,
// the live probe and trove setup all call ClassifyAnthropicKey.
func TestClassifyAnthropicKey(t *testing.T) {
	cases := []struct {
		name   string
		key    string
		ok     bool
		reason string
	}{
		// Accepted: Console API keys.
		{"console api03 key", "sk-ant-api03-" + fakeBody, true, ""},
		{"console api03 at the minimum length", "sk-ant-api03-" + fakeMin, true, ""},
		{"future api04 version is recognised", "sk-ant-api04-" + fakeBody, true, ""},
		{"dash and underscore allowed in the body", "sk-ant-api03-ab_cd-" + fakeMin, true, ""},
		// Accepted: identity-backed personal keys (sk-ant-usr-). Stated by the project owner;
		// not on the docs pages read 2026-11-07.
		{"personal key sk-ant-usr- with no digits", "sk-ant-usr-" + fakeBody, true, ""},
		{"personal key with two digits", "sk-ant-usr01-" + fakeBody, true, ""},
		{"personal key at the minimum length", "sk-ant-usr-" + fakeMin, true, ""},

		// Refused: OAuth tokens, classified by the sk-ant-oat prefix.
		{"oauth token (setup-token)", "sk-ant-oat01-" + fakeBody, false, ReasonOAuthToken},
		{"oauth token with no digits", "sk-ant-oat-" + fakeBody, false, ReasonOAuthToken},
		{"oauth token even if the body looks like a key", "sk-ant-oat01-sk-ant-api03-" + fakeBody, false, ReasonOAuthToken},

		// Refused: admin keys.
		{"admin key", "sk-ant-admin01-" + fakeBody, false, ReasonAdminKey},

		// Refused: Console-shaped but not valid.
		{"console prefix, body too short", "sk-ant-api03-" + fakeShort, false, ReasonUnknown},
		{"personal prefix, body too short", "sk-ant-usr-" + fakeShort, false, ReasonUnknown},
		{"console prefix, space inside", "sk-ant-api03-ab cd" + fakeMin, false, ReasonUnknown},
		{"console prefix, tab inside", "sk-ant-api03-ab\tcd" + fakeMin, false, ReasonUnknown},
		{"console prefix, leading space", " sk-ant-api03-" + fakeBody, false, ReasonUnknown},
		{"console prefix, trailing newline", "sk-ant-api03-" + fakeBody + "\n", false, ReasonUnknown},
		{"console prefix, no version digits", "sk-ant-api-" + fakeBody, false, ReasonUnknown},
		{"console prefix, one-digit version", "sk-ant-api3-" + fakeBody, false, ReasonUnknown},
		{"console prefix, three-digit version", "sk-ant-api003-" + fakeBody, false, ReasonUnknown},
		{"console prefix, no dash after version", "sk-ant-api03" + fakeBody, false, ReasonUnknown},
		{"personal prefix, three digits", "sk-ant-usr001-" + fakeBody, false, ReasonUnknown},
		{"personal prefix, uppercase", "SK-ANT-USR-" + fakeBody, false, ReasonUnknown},
		{"prefix only, console", "sk-ant-api03-", false, ReasonUnknown},
		{"prefix only, personal", "sk-ant-usr-", false, ReasonUnknown},

		// Refused: unknown sk-ant- prefixes and other providers' keys.
		{"unknown sk-ant- prefix", "sk-ant-zzz01-" + fakeBody, false, ReasonUnknown},
		{"openai project key", "sk-proj-" + fakeBody, false, ReasonUnknown},
		{"openai key in the Anthropic slot", "sk-" + fakeBody, false, ReasonUnknown},
		{"a bare word from a test fixture", "test-value-not-a-key", false, ReasonUnknown},
		{"lowercase console prefix", "SK-ANT-API03-" + fakeBody, false, ReasonUnknown},

		// Refused: nothing.
		{"empty", "", false, ReasonEmpty},
	}
	for _, tc := range cases {
		ok, reason := ClassifyAnthropicKey(tc.key)
		if ok != tc.ok || reason != tc.reason {
			t.Errorf("%s: ClassifyAnthropicKey = (%v, %q), want (%v, %q)", tc.name, ok, reason, tc.ok, tc.reason)
		}
		// IsConsoleAPIKey must agree with the classifier on acceptance.
		if IsConsoleAPIKey(tc.key) != tc.ok {
			t.Errorf("%s: IsConsoleAPIKey disagrees with ClassifyAnthropicKey", tc.name)
		}
	}
}
