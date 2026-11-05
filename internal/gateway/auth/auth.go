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
	"Gateway mode needs an API key or a cloud credential (Console key, Vertex, Foundry or Bedrock key). " +
	"A claude.ai login on any plan (Free, Pro, Max, Ultra, Team or Enterprise) is not supported, and neither is a ChatGPT login. " +
	"To use a subscription, remove the custom endpoint setting " +
	"(ANTHROPIC_BASE_URL for Claude Code, or the trove provider in ~/.codex/config.toml) " +
	"or run `trove undo`, and use the Trove plugin instead. Trove never handles subscription credentials."

// SigV4Message is what a Bedrock client is told when it signs with AWS
// credentials. SigV4 signs the request body hash and the Host header, so a
// request signed for 127.0.0.1 cannot be accepted by AWS, and Trove does not
// hold AWS credentials to re-sign it.
const SigV4Message = "Trove's gateway cannot forward AWS SigV4-signed requests: the signature covers the " +
	"request body and the Host header, and Trove never holds AWS credentials to re-sign. " +
	"Use an Amazon Bedrock API key (AWS_BEARER_TOKEN_BEDROCK), or remove ANTHROPIC_BEDROCK_BASE_URL " +
	"(or run `trove undo`) and use the Trove plugin instead."

// oauthTokenPrefix starts Claude.ai OAuth tokens (login and `claude setup-token`).
// Compared as a prefix only; used as a second signal beside the OAuth beta flag.
const oauthTokenPrefix = "sk-ant-oat" //nolint:gosec // G101: a prefix used to recognise and reject OAuth tokens, not a credential

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
	if d, bad := claudeLogin(h); bad {
		return d
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
	if looksLikeJWT(tok) || h.Get("Chatgpt-Account-Id") != "" {
		return reject("session_token")
	}
	return ok
}

// Vertex classifies a request bound for Google Cloud's Agent Platform
// (Vertex AI). The credential is a Google OAuth access token in Authorization,
// which is a cloud credential and not a claude.ai login. Tokens may be JWTs, so
// the JWT heuristic does not apply here.
func Vertex(h http.Header) Decision {
	if d, bad := claudeLogin(h); bad {
		return d
	}
	tok, isBearer := bearer(strings.TrimSpace(h.Get("Authorization")))
	if !isBearer || tok == "" {
		return reject("no_credential")
	}
	return ok
}

// Foundry classifies a request bound for Microsoft Foundry: an API key in
// x-api-key or api-key, or an Entra ID bearer token (a JWT, which is normal).
func Foundry(h http.Header) Decision {
	if d, bad := claudeLogin(h); bad {
		return d
	}
	if strings.TrimSpace(h.Get("X-Api-Key")) != "" || strings.TrimSpace(h.Get("Api-Key")) != "" {
		return ok
	}
	if tok, isBearer := bearer(strings.TrimSpace(h.Get("Authorization"))); isBearer && tok != "" {
		return ok
	}
	return reject("no_credential")
}

// Bedrock classifies a request bound for Amazon Bedrock. Only a Bedrock API
// key (a bearer token) is accepted; SigV4 requests are refused with SigV4Message.
func Bedrock(h http.Header) Decision {
	authz := strings.TrimSpace(h.Get("Authorization"))
	if strings.HasPrefix(authz, "AWS4-") {
		return reject("sigv4")
	}
	if d, bad := claudeLogin(h); bad {
		return d
	}
	tok, isBearer := bearer(authz)
	if !isBearer || tok == "" {
		return reject("no_credential")
	}
	return ok
}

// Compat classifies a request bound for an OpenAI-compatible server (vLLM,
// SGLang, or a provider serving open-weight models). Keys there are opaque
// and may be JWTs, so only explicit ChatGPT or claude.ai signals are refused.
// With allowNone, a request without any credential is accepted (open servers).
func Compat(h http.Header, allowNone bool) Decision {
	if h.Get("Chatgpt-Account-Id") != "" {
		return reject("session_token")
	}
	if d, bad := claudeLogin(h); bad {
		return d
	}
	authz := strings.TrimSpace(h.Get("Authorization"))
	if authz == "" {
		if allowNone {
			return ok
		}
		return reject("no_credential")
	}
	if tok, isBearer := bearer(authz); !isBearer || tok == "" {
		return reject("unknown_scheme")
	}
	return ok
}

// claudeLogin reports whether the headers carry a claude.ai login, on any
// plan: the OAuth value in anthropic-beta (set whenever a claude.ai login is
// the active credential) or a token with the OAuth prefix in a credential header.
func claudeLogin(h http.Header) (Decision, bool) {
	for _, beta := range h.Values("Anthropic-Beta") {
		for _, v := range strings.Split(beta, ",") {
			if strings.Contains(strings.ToLower(v), "oauth") {
				return reject("oauth_beta"), true
			}
		}
	}
	for _, v := range []string{h.Get("X-Api-Key"), h.Get("Api-Key")} {
		if strings.HasPrefix(strings.TrimSpace(v), oauthTokenPrefix) {
			return reject("oauth_token"), true
		}
	}
	if tok, isBearer := bearer(strings.TrimSpace(h.Get("Authorization"))); isBearer && strings.HasPrefix(tok, oauthTokenPrefix) {
		return reject("oauth_token"), true
	}
	return Decision{}, false
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
