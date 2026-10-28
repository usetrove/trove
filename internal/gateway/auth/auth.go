// Package auth decides whether a request carries an API key. The gateway only
// handles API-key traffic; subscription or OAuth credentials are refused.
//
// The rules are header-shaped, not prefix-based, because key formats are not
// part of the providers' documented contract:
//
//   - Anthropic: the credential must be in x-api-key. An Authorization header
//     is allowed only if it repeats the same value (Claude Code's apiKeyHelper
//     sends both). Claude Code docs (code.claude.com/docs/en/llm-gateway-protocol,
//     verified 2026-10-28) say a claude.ai login adds an OAuth capability to
//     anthropic-beta; that value is refused.
//   - OpenAI: the credential must be "Authorization: Bearer <token>". A token
//     shaped like a JWT (three dot-separated segments starting "eyJ") is
//     treated as a ChatGPT/OAuth session token. This is a heuristic: API keys
//     are not documented to be JWTs, but that is not guaranteed forever.
//
// Credentials are only compared, never copied, logged or returned.
package auth

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// Message is what the client is told when a request is refused.
const Message = "Trove's gateway is for API-key authentication only. " +
	"To use a Claude or ChatGPT subscription, remove the custom endpoint setting " +
	"(ANTHROPIC_BASE_URL for Claude Code, or the trove provider in ~/.codex/config.toml) " +
	"or run `trove undo`. Trove never handles subscription credentials."

// Decision is the verdict for one request.
type Decision struct {
	OK     bool
	Reason string // short, credential-free label for logs ("oauth_beta", "bearer_only", ...)
}

var ok = Decision{OK: true}

func reject(reason string) Decision { return Decision{Reason: reason} }

// Anthropic classifies a request bound for the Anthropic API.
func Anthropic(h http.Header) Decision {
	key := strings.TrimSpace(h.Get("X-Api-Key"))
	authz := strings.TrimSpace(h.Get("Authorization"))
	for _, beta := range h.Values("Anthropic-Beta") {
		for _, v := range strings.Split(beta, ",") {
			if strings.Contains(strings.ToLower(v), "oauth") {
				return reject("oauth_beta")
			}
		}
	}
	if key == "" {
		if authz != "" {
			return reject("bearer_only")
		}
		return reject("no_credential")
	}
	if authz != "" {
		tok, isBearer := bearer(authz)
		if !isBearer || subtle.ConstantTimeCompare([]byte(tok), []byte(key)) != 1 {
			return reject("mixed_credentials")
		}
	}
	return ok
}

// OpenAI classifies a request bound for the OpenAI API.
func OpenAI(h http.Header) Decision {
	authz := strings.TrimSpace(h.Get("Authorization"))
	if authz == "" {
		return reject("no_credential")
	}
	tok, isBearer := bearer(authz)
	if !isBearer || tok == "" {
		return reject("unknown_scheme")
	}
	if looksLikeJWT(tok) {
		return reject("session_token")
	}
	return ok
}

func bearer(v string) (string, bool) {
	scheme, tok, found := strings.Cut(v, " ")
	if !found || !strings.EqualFold(scheme, "bearer") {
		return "", false
	}
	return strings.TrimSpace(tok), true
}

func looksLikeJWT(tok string) bool {
	return strings.HasPrefix(tok, "eyJ") && strings.Count(tok, ".") == 2
}
