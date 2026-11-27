package report

import (
	"fmt"
	"strings"

	"github.com/usetrove/trove/internal/regress"
)

// Regressions is the regression watch (docs/regress.md): recent Claude Code versions and days compared with
// the user's own history. Advice only; Trove changes nothing because of it.
type Regressions struct {
	Alerts []regress.Alert `json:"alerts"`
	Checks []regress.Check `json:"checks"`
	// Checked counts the versions and days compared; TooLittle those without enough data or baseline yet.
	Versions  int `json:"versions_checked"`
	Days      int `json:"days_checked"`
	TooLittle int `json:"checks_without_enough_data"`
}

func regressionsFrom(r *regress.Result) *Regressions {
	if r == nil || len(r.Checks) == 0 {
		return nil
	}
	out := &Regressions{Alerts: r.Alerts, Checks: r.Checks}
	if out.Alerts == nil {
		out.Alerts = []regress.Alert{}
	}
	seen := map[string]bool{}
	for _, c := range r.Checks {
		if k := c.Kind + "/" + c.Key; !seen[k] {
			seen[k] = true
			if c.Kind == regress.KindVersion {
				out.Versions++
			} else {
				out.Days++
			}
		}
		if c.Status == regress.StatusTooLittleData || c.Status == regress.StatusNoBaseline {
			out.TooLittle++
		}
	}
	return out
}

func renderRegressions(b *strings.Builder, rg *Regressions, o Options) {
	b.WriteString("\n" + heading("Regression watch (Claude Code versions and days)"))
	for _, a := range rg.Alerts {
		b.WriteString(wrap(a.Sentence, o.width(), "  ", "  ") + "\n")
		b.WriteString(wrap(a.Advice, o.width(), "    ", "    ") + "\n")
	}
	if len(rg.Alerts) == 0 {
		b.WriteString(wrap(fmt.Sprintf("No regression found in %d %s and %d %s, on unexplained rebuilds, thinking per request, requests with thinking and 5-hour limit per 1k output-equivalent tokens.",
			rg.Versions, plural(rg.Versions, "version", "versions"), rg.Days, plural(rg.Days, "day", "days")), o.width(), "  ", "  ") + "\n")
	}
	notes := []string{fmt.Sprintf("An alert needs at least %d requests in %d sessions, a value %.1f robust deviations and %.0f%% above your own baseline. Trove never pins a version, turns auto-update off or changes effort for you.",
		regress.MinRequests, regress.MinSessions, regress.MinMADs, 100*regress.MinRelChange)}
	if rg.TooLittle > 0 {
		notes = append(notes, fmt.Sprintf("%d %s had too little data or no baseline yet.", rg.TooLittle, plural(rg.TooLittle, "check", "checks")))
	}
	for _, n := range notes {
		b.WriteString(wrap(n, o.width(), "  ", "  ") + "\n")
	}
}
