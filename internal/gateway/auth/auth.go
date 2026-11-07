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
//   - OpenAI: an allow-list, because OpenAI documents neither its key formats
//     nor a complete list of credential types. Codex CLI can sign in with ChatGPT
//     (a JWT access token), an access token for CI, an agent identity or a
//     personal access token, and ChatGPT-login traffic goes to
//     chatgpt.com/backend-api/codex, not api.openai.com (codex-rs
//     model-provider-info, read 2026-11-06). The gateway accepts only
//     "Authorization: Bearer sk-..." and refuses everything else, including any
//     credential type it does not recognise (reason "unrecognized_credential").
//     The "sk-" prefix is a heuristic that can wrongly refuse a real key if
//     OpenAI changes the format (it fails closed, never open), and it cannot
//     tell a key from some other token that also starts "sk-"; the JWT, header
//     and claim checks below catch the documented OAuth shapes first.
//
// Credentials are only compared, never copied, logged or returned.
package auth

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
)

// Message is what the client is told when a request is refused.
const Message = "Trove's gateway is for API-key authentication only. " +
	"Gateway mode needs an API key or a cloud credential (Console key, Vertex, Foundry or Bedrock key). " +
	"A claude.ai login on any plan (Free, Pro, Max, Ultra, Team or Enterprise) is not supported, and neither is a ChatGPT or Codex login " +
	"(Sign in with ChatGPT, a Codex access token, or anything from ~/.codex/auth.json). " +
	"On the OpenAI route only an OpenAI API key (sk-...) is accepted; any other kind of credential is refused. " +
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

// openAIKeyPrefix starts OpenAI platform API keys (sk-proj-..., sk-svcacct-...,
// and older sk-...). It is the allow-list entry for the OpenAI route.
const openAIKeyPrefix = "sk-"

// OpenAI classifies a request bound for the OpenAI API. It fails closed: a
// credential is accepted only if it is recognisably an API key.
func OpenAI(h http.Header) Decision {
	authz := strings.TrimSpace(h.Get("Authorization"))
	if authz == "" {
		return reject("no_credential")
	}
	tok, isBearer := bearer(authz)
	if !isBearer || tok == "" {
		return reject("unknown_scheme")
	}
	if d, bad := chatGPTLogin(h, tok, false); bad {
		return d
	}
	if d, bad := claudeLogin(h); bad { // never forward a claude.ai token to OpenAI either
		return d
	}
	if !strings.HasPrefix(tok, openAIKeyPrefix) || strings.ContainsAny(tok, " 	") {
		return reject("unrecognized_credential")
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
	authz := strings.TrimSpace(h.Get("Authorization"))
	tok, _ := bearer(authz)
	if d, bad := chatGPTLogin(h, tok, true); bad {
		return d
	}
	if d, bad := claudeLogin(h); bad {
		return d
	}
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

// chatGPTLogin reports whether the request carries a ChatGPT or Codex login
// credential. tok is the bearer token, or "" if there is none. jwtMayBeKey is
// true on routes where a JWT can be an ordinary key. The signals are
// the chatgpt-account-id header (not confirmed in Codex's source, kept because
// it is cheap) and a JWT. On the OpenAI route any JWT is refused, since API keys
// are not JWTs. A third-party server behind a compat route may issue JWT keys,
// so there a JWT is refused only if its claims say OpenAI issued it (see
// openAIIssued). The token is decoded in memory and never kept or logged.
func chatGPTLogin(h http.Header, tok string, jwtMayBeKey bool) (Decision, bool) {
	if h.Get("Chatgpt-Account-Id") != "" {
		return reject("session_token"), true
	}
	if looksLikeJWT(tok) && (!jwtMayBeKey || openAIIssued(tok)) {
		return reject("session_token"), true
	}
	return Decision{}, false
}

// openAIIssued reports whether a JWT's payload says OpenAI's own login issued it:
// an "iss" naming openai.com, or a claim in the "https://api.openai.com/" namespace
// (Codex's token_data.rs parses "https://api.openai.com/auth" and
// "https://api.openai.com/profile" from its login JWT, read 2026-11-06; whether
// every Codex token carries them is not confirmed). The signature is not
// checked and nothing from the payload is returned.
func openAIIssued(tok string) bool {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return false
	}
	var claims map[string]json.RawMessage
	if json.Unmarshal(raw, &claims) != nil {
		return false
	}
	for k := range claims {
		if strings.HasPrefix(k, "https://api.openai.com/") {
			return true
		}
	}
	var iss string
	if b, found := claims["iss"]; found && json.Unmarshal(b, &iss) == nil {
		return strings.Contains(strings.ToLower(iss), "openai.com")
	}
	return false
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
