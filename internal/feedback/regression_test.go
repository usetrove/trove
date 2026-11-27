package feedback

import (
	"net/url"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/regress"
)

func TestRegressionIssueHasNumbersOnly(t *testing.T) {
	a := regress.Alert{Check: regress.Check{Kind: regress.KindVersion, Key: "2.1.89", Metric: regress.MetricThinkTokens,
		Value: 1712.4, Baseline: 1003.2, MADs: 9.14, Change: 0.7069, N: 300, Sessions: 8, Blocks: 20, Label: regress.LabelMeasured,
		Stratum: "claude-opus-5-5, high effort"},
		Cause: regress.CauseClient, Version: "2.1.89", Previous: "2.1.88"}
	a.Sentence, a.Advice = regress.Sentence(a), regress.Advice(a)
	d := Diagnostics{TroveVersion: "0.9.0", OS: "linux", Arch: "amd64"}
	body := RegressionBody([]regress.Alert{a}, d, "")
	if body != RegressionBody([]regress.Alert{a}, d, "") {
		t.Fatal("body is not deterministic")
	}
	for _, want := range []string{"Claude Code version: 2.1.89 (previous: 2.1.88)", "compared within: claude-opus-5-5, high effort", "now: 1,712 tokens, usual: 1,003 tokens (median of 20 earlier days)",
		"change: +71%, 9.1 robust deviations", "sample: 300 requests in 8 sessions", "label: measured; likely cause: this Claude Code version", "linux/amd64, Trove 0.9.0"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	// The advice is for the user, not for the issue.
	if strings.Contains(body, "DISABLE_AUTOUPDATER") || strings.Contains(body, "install.sh") {
		t.Errorf("body carries advice:\n%s", body)
	}
	title := RegressionTitle(a)
	if title != "Token use regression: thinking per request up 71% in Claude Code 2.1.89" {
		t.Errorf("title = %q", title)
	}
	link, _ := RegressionURL(title, body)
	u, err := url.Parse(link)
	if err != nil || u.Host != "github.com" || u.Path != "/anthropics/claude-code/issues/new" || u.Query().Get("labels") != "" {
		t.Errorf("link = %s", link)
	}
}
