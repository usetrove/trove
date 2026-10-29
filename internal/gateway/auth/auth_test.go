package auth

import (
	"net/http"
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

func TestDecisionNeverContainsCredential(t *testing.T) {
	for _, d := range []Decision{
		Anthropic(hdr("Authorization", "Bearer SECRETVALUE")),
		OpenAI(hdr("Authorization", "Bearer eyJSECRETVALUE.a.b")),
	} {
		if d.Reason == "" || len(d.Reason) > 20 {
			t.Errorf("reason should be a short label: %q", d.Reason)
		}
	}
}
