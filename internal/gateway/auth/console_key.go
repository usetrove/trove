package auth

import (
	"regexp"
	"strings"
)

// Reasons a key is refused. They are short labels for logs and messages; a key
// value is never part of a reason.
const (
	ReasonOAuthToken = "oauth_token"             //nolint:gosec // G101: a refusal label, not a credential; sk-ant-oat... is a claude.ai login or setup-token
	ReasonAdminKey   = "admin_key"               // sk-ant-admin...: an Admin API key
	ReasonUnknown    = "unrecognized_credential" // anything else, including OpenAI keys and unknown sk-ant- prefixes
	ReasonEmpty      = "no_credential"
)

// consoleKeyRe is the allow-list for Claude Console API keys. Two shapes are
// accepted, both with at least 32 key characters after the prefix:
//
//   - sk-ant-api + two digits + "-" (sk-ant-api03-... is the documented example;
//     a future api04 key is still recognised).
//   - sk-ant-usr + "-" (identity-backed personal keys; the project owner states
//     this format, and the Anthropic docs pages read on 2026-11-07 do not
//     document the prefix, so it is taken on that statement and marked as such
//     in docs/gateway.md).
//
// Anything else is refused. The minimum length is a Trove choice: the docs give
// no full specification. Refusing a real key by mistake fails closed.
var consoleKeyRe = regexp.MustCompile(`^(sk-ant-api[0-9]{2}|sk-ant-usr[0-9]{0,2})-[A-Za-z0-9_-]{32,}$`)

// IsConsoleAPIKey reports whether s has the shape of an accepted Console API key.
// It never logs, stores or returns s.
func IsConsoleAPIKey(s string) bool {
	return consoleKeyRe.MatchString(s)
}

// ClassifyAnthropicKey decides whether an ANTHROPIC_API_KEY-style value is
// accepted. It returns ok=true, or ok=false with a reason label. OAuth tokens
// and admin keys are named by their prefixes so each gets its own reason; every
// other value that is not a Console key is ReasonUnknown.
func ClassifyAnthropicKey(s string) (ok bool, reason string) {
	switch {
	case s == "":
		return false, ReasonEmpty
	case IsConsoleAPIKey(s):
		return true, ""
	case strings.HasPrefix(s, oauthTokenPrefix):
		return false, ReasonOAuthToken
	case strings.HasPrefix(s, "sk-ant-admin"):
		return false, ReasonAdminKey
	default:
		return false, ReasonUnknown
	}
}
