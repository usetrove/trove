package auth

import "regexp"

// consoleKeyRe matches the shape of a Claude Console API key: the documented
// "sk-ant-api" prefix, a two-digit version (sk-ant-api03-... today; a future
// api04 key is still recognised), a dash, then at least 32 key characters.
//
// Anthropic's get-api-key page (platform.claude.com/docs/en/get-api-key, read
// 2026-11-07) shows the format as `sk-ant-api03-...`. The page gives no full
// specification, so the minimum length is a Trove choice: a short string is
// refused rather than guessed at. Everything else is refused, including OAuth
// tokens (sk-ant-oat...), admin keys (sk-ant-admin...), and unknown prefixes
// such as sk-ant-usr. Refusing a real key by mistake fails closed.
var consoleKeyRe = regexp.MustCompile(`^sk-ant-api[0-9]{2}-[A-Za-z0-9_-]{32,}$`)

// IsConsoleAPIKey reports whether s has the shape of a Claude Console API key.
// It never logs, stores or returns s.
func IsConsoleAPIKey(s string) bool {
	return consoleKeyRe.MatchString(s)
}
