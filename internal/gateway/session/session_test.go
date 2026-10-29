package session

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestDeriveKey(t *testing.T) {
	anth := func(sys, user string) []byte {
		return []byte(`{"model":"m","system":"` + sys + `","messages":[{"role":"user","content":"` + user + `"},{"role":"assistant","content":"a"}]}`)
	}
	a := DeriveKey(http.Header{}, anth("sys", "hello"))
	if a == "" || !strings.HasPrefix(a, "h-") {
		t.Fatalf("got %q", a)
	}
	longer := []byte(`{"system":"sys","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"a"},{"role":"user","content":"more"}]}`)
	if DeriveKey(http.Header{}, longer) != a {
		t.Error("later turns must keep the key")
	}
	if DeriveKey(http.Header{}, anth("sys", "other")) == a || DeriveKey(http.Header{}, anth("sys2", "hello")) == a {
		t.Error("different prompts must differ")
	}
	chat := []byte(`{"messages":[{"role":"system","content":"s"},{"role":"user","content":"u"}]}`)
	resp := []byte(`{"instructions":"s","input":[{"role":"user","content":"u"}]}`)
	if DeriveKey(http.Header{}, chat) == "" || DeriveKey(http.Header{}, resp) == "" {
		t.Error("openai shapes should derive keys")
	}
	if DeriveKey(http.Header{}, []byte("not json")) != "" || DeriveKey(http.Header{}, nil) != "" {
		t.Error("undecodable bodies have no key")
	}
}

func TestClaudeHeaderWinsAndIsHashed(t *testing.T) {
	h := http.Header{}
	h.Set("X-Claude-Code-Session-Id", "raw-session-id")
	k := DeriveKey(h, []byte(`{"system":"s"}`))
	if !strings.HasPrefix(k, "c-") || strings.Contains(k, "raw-session-id") {
		t.Errorf("got %q", k)
	}
}

func TestTrackerIdleBoundary(t *testing.T) {
	tr := NewTracker(30 * time.Minute)
	t0 := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	k0 := tr.Key("h-x", t0)
	if k0 != "h-x" || tr.Key("h-x", t0.Add(29*time.Minute)) != "h-x" {
		t.Error("within the idle gap the session continues")
	}
	k1 := tr.Key("h-x", t0.Add(29*time.Minute+31*time.Minute))
	if k1 == k0 {
		t.Error("an idle gap longer than the threshold starts a new session")
	}
	if tr.Key("", t0) != "" {
		t.Error("empty base stays empty")
	}
}
