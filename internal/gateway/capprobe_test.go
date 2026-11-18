package gateway

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/gateway/agent"
)

func probeHit(t *testing.T, p *RefusalProbe, path, ua string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(`{"model":"m","messages":[{"role":"user","content":"PROBESENTINEL"}]}`)))
	r.Header.Set("User-Agent", ua)
	r.Header.Set("x-api-key", fakeKey)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	return w
}

// The probe answers exactly as a refusing cap would, counts attempts per agent, and keeps
// nothing but the agent, its version and the count.
func TestRefusalProbeAnswersLikeTheCapAndCounts(t *testing.T) {
	p := NewRefusalProbe(time.Now)
	w := probeHit(t, p, PrefixAnthropic+"/v1/messages", "claude-cli/2.1.300 (external, cli)")
	if w.Code != 402 || !strings.Contains(w.Body.String(), "billing_error") || !strings.Contains(w.Body.String(), "Trove spend cap") {
		t.Fatalf("anthropic probe answer %d %s", w.Code, w.Body)
	}
	probeHit(t, p, PrefixAnthropic+"/v1/messages", "claude-cli/2.1.300 (external, cli)")
	if w := probeHit(t, p, PrefixOpenAI+"/v1/responses", "codex_cli_rs/0.9.0"); w.Code != 429 || !strings.Contains(w.Body.String(), "trove_spend_cap_reached") {
		t.Fatalf("openai probe answer %d %s", w.Code, w.Body)
	}
	seen := p.Seen()
	if len(seen) != 2 || seen[0].Agent != agent.ClaudeCode || seen[0].Attempts != 2 || seen[0].AgentVersion != "2.1.300" || seen[1].Agent != agent.Codex || seen[1].Attempts != 1 {
		t.Fatalf("seen = %+v", seen)
	}
	for _, s := range seen {
		if strings.Contains(s.Agent+s.AgentVersion, "PROBESENTINEL") || strings.Contains(s.Agent+s.AgentVersion, fakeKey) {
			t.Fatalf("probe kept request content: %+v", s)
		}
	}
}

func TestProbeVerdicts(t *testing.T) {
	cases := []struct {
		attempts int
		shown    bool
		want     string
	}{
		{1, true, VerdictStops},
		{1, false, VerdictNotShown},
		{3, true, VerdictRetries},
		{0, true, VerdictNoRequest},
	}
	for _, c := range cases {
		if got := ProbeVerdict(c.attempts, c.shown); got != c.want {
			t.Errorf("ProbeVerdict(%d, %v) = %q, want %q", c.attempts, c.shown, got, c.want)
		}
	}
}

// Done when: stopping is enabled only after a probe verifies how each agent handles a
// refused request. Only a "stops" record lets the cap refuse that agent.
func TestCapRefusesOnlyAgentsWithAPassingProbe(t *testing.T) {
	dir := t.TempDir()
	if CapRefusesFrom(dir)(agent.ClaudeCode) {
		t.Fatal("no probe record: nobody may be refused")
	}
	now := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	if err := SaveProbe(dir, ProbeRecord{Agent: agent.ClaudeCode, AgentVersion: "2.1.300", Attempts: 1, Verdict: VerdictStops, CheckedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := SaveProbe(dir, ProbeRecord{Agent: agent.Codex, Attempts: 4, Verdict: VerdictRetries, CheckedAt: now}); err != nil {
		t.Fatal(err)
	}
	refuses := CapRefusesFrom(dir)
	if !refuses(agent.ClaudeCode) || refuses(agent.Codex) || refuses(agent.SDK) {
		t.Error("only the agent whose probe showed it stops may be refused")
	}
	// A later failing probe replaces the passing one.
	if err := SaveProbe(dir, ProbeRecord{Agent: agent.ClaudeCode, AgentVersion: "2.2.0", Attempts: 2, Verdict: VerdictRetries, CheckedAt: now}); err != nil {
		t.Fatal(err)
	}
	if CapRefusesFrom(dir)(agent.ClaudeCode) {
		t.Error("a newer failing probe must withdraw the permission")
	}
	recs, err := LoadProbes(dir)
	if err != nil || len(recs) != 2 {
		t.Fatalf("records %+v %v", recs, err)
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(filepath.Join(dir, ProbeFile)); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("probe file mode %v %v, want 0600", fi.Mode(), err)
		}
	}
	// A broken file refuses no one.
	if err := os.WriteFile(filepath.Join(dir, ProbeFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if CapRefusesFrom(dir)(agent.ClaudeCode) {
		t.Error("an unreadable probe file must refuse no one")
	}
}
