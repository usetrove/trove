package feedback

import (
	"fmt"
	"strings"

	"github.com/usetrove/trove/internal/regress"
)

// RegressionRepo is where a Claude Code regression is reported: the Claude Code docs point there for known
// issues and bug reports (https://code.claude.com/docs/en/troubleshooting, "Get more help", checked
// 2026-11-26), next to the /feedback command inside Claude Code.
const RegressionRepo = "anthropics/claude-code"

// RegressionTitle names the strongest alert.
func RegressionTitle(a regress.Alert) string {
	where := "Claude Code " + a.Key
	if a.Kind == regress.KindDay {
		where = "requests on " + a.Key
	}
	change := "up from none"
	if a.Baseline > 0 {
		change = fmt.Sprintf("up %.0f%%", 100*a.Change)
	}
	return fmt.Sprintf("Token use regression: %s %s in %s", strings.ToLower(regress.MetricTitle(a.Metric)), change, where)
}

// RegressionBody is the issue text: the numbers of each alert and nothing else (no prompts, code, file
// paths, tool output, session ids or keys). It is deterministic.
func RegressionBody(alerts []regress.Alert, d Diagnostics, note string) string {
	var b strings.Builder
	if n := strings.TrimSpace(note); n != "" {
		b.WriteString(n + "\n\n")
	} else {
		b.WriteString("<!-- Add what you noticed, if anything. Everything below is numbers only. -->\n\n")
	}
	b.WriteString("Compared with my own earlier usage on the same machine, the same kind of work started to use more. " +
		"Numbers from Claude Code's local session logs (token counts only), compared by Trove's regression watch.\n\n")
	for i, a := range alerts {
		fmt.Fprintf(&b, "### %d. %s\n\n", i+1, regress.MetricTitle(a.Metric))
		if a.Kind == regress.KindVersion {
			fmt.Fprintf(&b, "- Claude Code version: %s", a.Key)
			if a.Previous != "" {
				fmt.Fprintf(&b, " (previous: %s)", a.Previous)
			}
			b.WriteString("\n")
		} else {
			fmt.Fprintf(&b, "- day: %s, Claude Code versions active: %s\n", a.Key, listOrNone(a.Active))
		}
		fmt.Fprintf(&b, "- now: %s, usual: %s (median of %d earlier days)\n", regress.FormatValue(a.Metric, a.Value), regress.FormatValue(a.Metric, a.Baseline), a.Blocks)
		change := "from zero"
		if a.Baseline > 0 {
			change = fmt.Sprintf("+%.0f%%", 100*a.Change)
		}
		fmt.Fprintf(&b, "- change: %s, %.1f robust deviations\n", change, a.MADs)
		unit := "requests"
		if a.Metric == regress.MetricLimitPerKOE {
			unit = "5-hour meter intervals"
		}
		fmt.Fprintf(&b, "- sample: %d %s in %d sessions\n", a.N, unit, a.Sessions)
		lbl := a.Label
		if a.Basis != "" {
			lbl += ", " + a.Basis
		}
		fmt.Fprintf(&b, "- label: %s; likely cause: %s\n\n", lbl, causeText[a.Cause])
	}
	fmt.Fprintf(&b, "Environment: %s/%s, Trove %s.\n", d.OS, d.Arch, d.TroveVersion)
	return b.String()
}

var causeText = map[string]string{
	regress.CauseClient:  "this Claude Code version (other versions at the same time did not change)",
	regress.CauseServer:  "server side (more than one Claude Code version changed at the same time)",
	regress.CauseUnclear: "unclear (only one version to compare)",
}

// RegressionURL builds the prefilled new-issue link for RegressionRepo, without labels.
func RegressionURL(title, body string) (string, bool) {
	return issueURL(RegressionRepo, "", title, body)
}
