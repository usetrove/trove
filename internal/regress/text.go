package regress

import (
	"fmt"
	"math"
	"strings"
)

// Advice facts, checked 2026-11-26 on https://code.claude.com/docs/en/setup: the native installer takes a
// version number ("curl -fsSL https://claude.ai/install.sh | bash -s 2.1.89"; on Windows
// "& ([scriptblock]::Create((irm https://claude.ai/install.ps1))) 2.1.89"), and setting DISABLE_AUTOUPDATER to
// "1" in the env key of settings.json stops the background update check (DISABLE_UPDATES blocks manual
// updates too). Trove never runs any of it: the advice is shown only.
const (
	SetupDocs   = "https://code.claude.com/docs/en/setup"
	DocsChecked = "2026-11-26"
)

// metricPhrase is what each metric reads as in a sentence.
var metricPhrase = map[string]string{
	MetricRebuilds:    "unexplained cache rebuilds",
	MetricThinkTokens: "thinking per request",
	MetricThinkShare:  "requests with thinking",
	MetricLimitPerKOE: "5-hour limit used per 1k output-equivalent tokens",
}

// MetricTitle is the metric's name in tables.
func MetricTitle(m string) string {
	switch m {
	case MetricRebuilds:
		return "Unexplained rebuilds"
	case MetricThinkTokens:
		return "Thinking per request"
	case MetricThinkShare:
		return "Requests with thinking"
	case MetricLimitPerKOE:
		return "5h limit per 1k out-eq."
	}
	return m
}

// FormatValue shows a metric's value with its unit.
func FormatValue(m string, v float64) string {
	switch m {
	case MetricRebuilds, MetricThinkShare:
		return fmt.Sprintf("%.1f%%", 100*v)
	case MetricThinkTokens:
		return groupDigits(int64(math.Round(v))) + " tokens"
	case MetricLimitPerKOE:
		return fmt.Sprintf("%.3g points", v)
	}
	return fmt.Sprintf("%g", v)
}

func groupDigits(n int64) string {
	s := fmt.Sprintf("%d", n)
	if n < 0 {
		return "-" + groupDigits(-n)
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func unitOf(m string) string {
	if m == MetricLimitPerKOE {
		return "meter intervals"
	}
	return "requests"
}

// Sentence is the alert in one plain sentence with its numbers.
func Sentence(a Alert) string {
	subject := "Claude Code " + a.Key
	if a.Kind == KindDay {
		subject = "On " + a.Key
	}
	change := "up from none"
	if a.Baseline > 0 {
		change = fmt.Sprintf("up %.0f%%", 100*a.Change)
	}
	lbl := a.Label
	if a.Basis != "" {
		lbl = a.Basis
	}
	what := metricPhrase[a.Metric]
	if a.Stratum != "" {
		what += " (" + a.Stratum + ")"
	}
	return fmt.Sprintf("%s: %s %s on your usual (%s vs %s, %d %s in %d sessions, %s).",
		subject, what, change, FormatValue(a.Metric, a.Value), FormatValue(a.Metric, a.Baseline),
		a.N, unitOf(a.Metric), a.Sessions, lbl)
}

// Advice is what the user may do about it. Shown only: Trove pins nothing, turns nothing off and changes
// no effort setting itself.
func Advice(a Alert) string {
	pin := ""
	if a.Previous != "" {
		pin = fmt.Sprintf("pin Claude Code %s (native installer: `curl -fsSL https://claude.ai/install.sh | bash -s %s`) and turn auto-update off with \"DISABLE_AUTOUPDATER\": \"1\" in the env block of settings.json (%s)",
			a.Previous, a.Previous, SetupDocs)
	}
	switch a.Cause {
	case CauseClient:
		if pin == "" {
			return fmt.Sprintf("Likely Claude Code %s itself; there is no earlier version in your history to go back to.", a.Version)
		}
		return fmt.Sprintf("Likely Claude Code %s itself, since other versions running at the same time did not change: %s.", a.Version, pin)
	case CauseServer:
		return "Likely a change on the provider's side, since more than one Claude Code version shows it at the same time: drop effort one level until it is fixed."
	}
	why := "only one Claude Code version was active"
	if a.Kind == KindDay && len(a.Active) == 0 {
		why = "no Claude Code version had enough requests that day to compare"
	} else if a.Kind == KindDay && len(a.Active) >= 2 {
		why = "the versions active that day did not clearly differ"
	}
	if pin != "" && a.Kind == KindVersion {
		return fmt.Sprintf("Cause unclear: %s. If it started with the update, %s; if not, drop effort one level until it is fixed.", why, pin)
	}
	return fmt.Sprintf("Cause unclear: %s. Check again tomorrow, or drop effort one level until it settles.", why)
}
