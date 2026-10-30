package feedback

import (
	"net/url"
	"strings"
	"testing"
)

func TestBodyIsDeterministicAndHasNoContentFields(t *testing.T) {
	d := Diagnostics{
		TroveVersion: "0.1.0", Commit: "abc", GoVersion: "go1.27.3", OS: "linux", Arch: "amd64", PricingData: "2026-10-28.3",
		AgentsDetected: []string{"Claude Code"}, Requests: 12, Sessions: 3, FixesApplied: 2, FixesReverted: 1, GuardrailTrips: 1,
		ErrorClasses: map[string]int{"upstream_5xx": 2, "auth": 1, "timeout": 4},
	}
	b1, b2 := Body(d, ""), Body(d, "")
	if b1 != b2 {
		t.Error("body is not deterministic")
	}
	for _, want := range []string{"trove: 0.1.0", "agents detected: Claude Code", "recorded requests: 12", "error classes: auth=1, timeout=4, upstream_5xx=2", "reverted: 1"} {
		if !strings.Contains(b1, want) {
			t.Errorf("body lacks %q:\n%s", want, b1)
		}
	}
	if !strings.Contains(Body(d, "it crashed"), "it crashed") {
		t.Error("the user's note is missing")
	}
}

func TestIssueURLShortAndTruncated(t *testing.T) {
	link, trunc := IssueURL("Beta feedback: ", "short")
	if trunc || !strings.HasPrefix(link, "https://github.com/"+Repo+"/issues/new?") {
		t.Fatalf("unexpected: %v %s", trunc, link)
	}
	u, err := url.Parse(link)
	if err != nil || u.Query().Get("body") != "short" || u.Query().Get("labels") != "beta-feedback" {
		t.Errorf("bad query: %v %v", err, u)
	}

	long := strings.Repeat("é line of text\n", 2000)
	link, trunc = IssueURL("t", long)
	if !trunc || len(link) > MaxURLLen {
		t.Fatalf("truncated=%v len=%d, want truncated within %d", trunc, len(link), MaxURLLen)
	}
	u, _ = url.Parse(link)
	if !strings.Contains(u.Query().Get("body"), "shortened") {
		t.Error("truncated body lacks the marker")
	}
}
