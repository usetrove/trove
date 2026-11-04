package auth

import (
	"net/http"
	"strings"
	"testing"
)

func hdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i < len(kv); i += 2 {
		h.Add(kv[i], kv[i+1])
	}
	return h
}

func TestAnthropic(t *testing.T) {
	tests := []struct {
		name   string
		h      http.Header
		ok     bool
		reason string
	}{
		{"x-api-key", hdr("x-api-key", "FAKE-KEY"), true, ""},
		{"apiKeyHelper sends both, same value", hdr("x-api-key", "FAKE-KEY", "Authorization", "Bearer FAKE-KEY"), true, ""},
		// An Enterprise org's Console-issued key (usage-billed) is an ordinary API key on the wire.
		{"enterprise console key, same shape", hdr("x-api-key", "FAKE-ENTERPRISE-KEY", "anthropic-beta", "claude-code-20250219"), true, ""},
		{"api key with benign betas", hdr("x-api-key", "FAKE-KEY", "anthropic-beta", "interleaved-thinking-2025-05-14,effort-2025-11-24"), true, ""},
		{"bearer only (auth token or oauth)", hdr("Authorization", "Bearer FAKE-TOKEN"), false, "bearer_only"},
		{"oauth beta flag", hdr("x-api-key", "FAKE-KEY", "anthropic-beta", "foo,oauth-2025-04-20"), false, "oauth_beta"},
		{"different bearer and key", hdr("x-api-key", "FAKE-KEY", "Authorization", "Bearer OTHER"), false, "mixed_credentials"},
		{"non-bearer scheme beside key", hdr("x-api-key", "FAKE-KEY", "Authorization", "Basic abc"), false, "mixed_credentials"},
		{"nothing", hdr(), false, "no_credential"},
	}
	for _, tc := range tests {
		got := Anthropic(tc.h)
		if got.OK != tc.ok || got.Reason != tc.reason {
			t.Errorf("%s: got %+v", tc.name, got)
		}
	}
}

func TestOpenAI(t *testing.T) {
	tests := []struct {
		name   string
		h      http.Header
		ok     bool
		reason string
	}{
		{"bearer key", hdr("Authorization", "Bearer FAKE-KEY"), true, ""},
		{"lowercase scheme", hdr("Authorization", "bearer FAKE-KEY"), true, ""},
		{"jwt session token", hdr("Authorization", "Bearer eyJhbGciOi.eyJzdWIiOi.c2ln"), false, "session_token"},
		{"basic", hdr("Authorization", "Basic abc"), false, "unknown_scheme"},
		{"empty bearer", hdr("Authorization", "Bearer "), false, "unknown_scheme"},
		{"none", hdr(), false, "no_credential"},
	}
	for _, tc := range tests {
		got := OpenAI(tc.h)
		if got.OK != tc.ok || got.Reason != tc.reason {
			t.Errorf("%s: got %+v", tc.name, got)
		}
	}
}

// Every claude.ai login type (Free, Pro, Max, Ultra, Team, Enterprise) is OAuth
// and looks the same on the wire; each shape must be refused on every route.
func TestClaudeLoginsRefusedEverywhere(t *testing.T) {
	logins := []struct {
		name string
		h    http.Header
	}{
		{"login, bearer + oauth beta", hdr("Authorization", "Bearer FAKE-OAT", "anthropic-beta", "claude-code-20250219,oauth-2025-04-20")},
		{"login, key header + oauth beta", hdr("x-api-key", "FAKE", "anthropic-beta", "oauth-2025-04-20")},
		{"setup-token in bearer", hdr("Authorization", "Bearer sk-ant-oat01-FAKE")},
		{"setup-token in x-api-key", hdr("x-api-key", "sk-ant-oat01-FAKE")},
		{"setup-token in both", hdr("x-api-key", "sk-ant-oat01-FAKE", "Authorization", "Bearer sk-ant-oat01-FAKE")},
	}
	routes := map[string]func(http.Header) Decision{
		"anthropic": Anthropic,
		"vertex":    Vertex,
		"foundry":   Foundry,
		"bedrock":   Bedrock,
		"compat":    func(h http.Header) Decision { return Compat(h, true) },
	}
	for rn, fn := range routes {
		for _, l := range logins {
			if got := fn(l.h); got.OK {
				t.Errorf("%s / %s: accepted a claude.ai login", rn, l.name)
			}
		}
	}
	// Bearer-only with no oauth marker is a Console OAuth or WIF token on the
	// Anthropic route: refused too.
	if Anthropic(hdr("Authorization", "Bearer FAKE-CONSOLE-OAUTH")).OK {
		t.Error("bearer-only accepted on the Anthropic route")
	}
}

func TestChatGPTLoginRefused(t *testing.T) {
	for _, h := range []http.Header{
		hdr("Authorization", "Bearer eyJhbGciOi.eyJzdWIiOi.c2ln"),
		hdr("Authorization", "Bearer FAKE", "chatgpt-account-id", "acct"),
	} {
		if OpenAI(h).OK {
			t.Errorf("openai accepted a ChatGPT login: %v", h)
		}
	}
	if Compat(hdr("Authorization", "Bearer FAKE", "chatgpt-account-id", "acct"), false).OK {
		t.Error("compat accepted a ChatGPT login")
	}
}

func TestCloudRoutes(t *testing.T) {
	tests := []struct {
		name   string
		d      Decision
		ok     bool
		reason string
	}{
		{"vertex google token", Vertex(hdr("Authorization", "Bearer ya29.FAKE")), true, ""},
		{"vertex jwt is fine", Vertex(hdr("Authorization", "Bearer eyJa.eyJb.c")), true, ""},
		{"vertex none", Vertex(hdr()), false, "no_credential"},
		{"foundry api key", Foundry(hdr("api-key", "FAKE")), true, ""},
		{"foundry x-api-key", Foundry(hdr("x-api-key", "FAKE")), true, ""},
		{"foundry entra jwt", Foundry(hdr("Authorization", "Bearer eyJa.eyJb.c")), true, ""},
		{"foundry none", Foundry(hdr()), false, "no_credential"},
		{"bedrock api key", Bedrock(hdr("Authorization", "Bearer FAKE-BEDROCK")), true, ""},
		{"bedrock sigv4", Bedrock(hdr("Authorization", "AWS4-HMAC-SHA256 Credential=FAKE/20261104/us-east-1/bedrock/aws4_request, SignedHeaders=host, Signature=00")), false, "sigv4"},
		{"bedrock none", Bedrock(hdr()), false, "no_credential"},
		{"compat bearer", Compat(hdr("Authorization", "Bearer FAKE"), false), true, ""},
		{"compat jwt key", Compat(hdr("Authorization", "Bearer eyJa.eyJb.c"), false), true, ""},
		{"compat none refused", Compat(hdr(), false), false, "no_credential"},
		{"compat none allowed", Compat(hdr(), true), true, ""},
		{"compat basic", Compat(hdr("Authorization", "Basic abc"), true), false, "unknown_scheme"},
	}
	for _, tc := range tests {
		if tc.d.OK != tc.ok || tc.d.Reason != tc.reason {
			t.Errorf("%s: got %+v", tc.name, tc.d)
		}
	}
}

func TestMessageGuidance(t *testing.T) {
	for _, want := range []string{"API-key authentication only", "remove the custom endpoint", "Trove plugin", "Enterprise"} {
		if !strings.Contains(Message, want) {
			t.Errorf("Message lacks %q", want)
		}
	}
}

func TestDecisionNeverContainsCredential(t *testing.T) {
	for _, d := range []Decision{
		Anthropic(hdr("Authorization", "Bearer SECRETVALUE")),
		Anthropic(hdr("x-api-key", "sk-ant-oat01-SECRETVALUE")),
		Bedrock(hdr("Authorization", "AWS4-HMAC-SHA256 Credential=SECRETVALUE")),
		OpenAI(hdr("Authorization", "Bearer eyJSECRETVALUE.a.b")),
	} {
		if d.Reason == "" || len(d.Reason) > 20 {
			t.Errorf("reason should be a short label: %q", d.Reason)
		}
	}
}
