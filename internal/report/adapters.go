package report

import (
	"fmt"
	"strings"
)

// writeAdapters writes the plugin-adapter section, if a plugin recorded anything.
func writeAdapters(b *strings.Builder, r Report, o Options) {
	if r.Adapters == nil {
		return
	}
	b.WriteString("\n" + heading("Harness plugins"))
	for _, l := range adapterLines(r.Adapters) {
		b.WriteString(wrap(l, o.width(), "  ", "    ") + "\n")
	}
}

// loginText names a login in plain words.
func loginText(login string) string {
	switch login {
	case "api_key":
		return "API key"
	case "copilot":
		return "GitHub Copilot login"
	}
	return "a login Trove can only watch (a subscription, or one it could not classify)"
}

// adapterLines describes what plugins for other harnesses did. How many times an
// optimizer made a decision is counted by Trove (measured); the tokens it kept out
// of the context come from sizes (estimated), with the optimizer's own overhead
// shown beside them (CLAUDE.md rule 15). Usage and cost are what the harness itself
// reported, so they are labelled that way, and a cost total is only given when
// every call reported one.
func adapterLines(a *Adapters) []string {
	var out []string
	for _, d := range a.Decisions {
		l := fmt.Sprintf("%s, %s: %s (%s) made %d decisions (measured counts)", d.Harness, loginText(d.Login), d.Optimizer, d.State, d.Events)
		if d.State == "on" {
			l += fmt.Sprintf(", %d of which changed a call or its output", d.Changed)
		} else {
			l += ", none of which changed anything"
		}
		if d.Tokens > 0 {
			word := "kept out of the context"
			if d.State != "on" {
				word = "it would have kept out of the context"
			}
			l += fmt.Sprintf("; about %s tokens %s (estimated from sizes", FormatTokens(float64(d.Tokens)), word)
			if d.OverheadTokens > 0 {
				l += fmt.Sprintf("; its own overhead was %s tokens, also estimated", FormatTokens(float64(d.OverheadTokens)))
			}
			l += ")"
		}
		out = append(out, l+".")
	}
	for _, u := range a.Usage {
		l := fmt.Sprintf("%s, %s: %d model calls in %d sessions: %s input, %s output, %s cache-read tokens (as the harness reported them)",
			u.Harness, loginText(u.Login), u.Calls, u.Sessions, FormatTokens(float64(u.In)), FormatTokens(float64(u.Out)), FormatTokens(float64(u.CacheR)))
		switch {
		case u.Login != "api_key":
			l += "; cost is not shown for this login"
		case u.CostKnown == u.Calls && u.Calls > 0:
			l += fmt.Sprintf("; cost %s (as the harness reported it)", FormatUSD(u.CostUSD))
		default:
			l += fmt.Sprintf("; cost reported for only %d of %d calls, so no total is shown", u.CostKnown, u.Calls)
		}
		out = append(out, l+".")
	}
	return out
}
