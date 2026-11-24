package report

import (
	"fmt"
	"strings"

	"github.com/usetrove/trove/internal/measure"
)

// renderColdResume is the cold-resume guard's section: what it did (measured), what users did next (measured),
// the rebuilds it avoided (estimated) and the hour-after comparison of the 5-hour meter (measured, not randomized).
func renderColdResume(b *strings.Builder, c *measure.ColdResume, o Options) {
	b.WriteString("\n" + heading("Cold-resume guard (hook_cold_resume)"))
	line := func(s string) { b.WriteString(wrap(s, o.width(), "  ", "  ") + "\n") }
	if c.Blocked > 0 {
		line(fmt.Sprintf("Stopped %d %s once (measured). You sent %d again (%s) and cleared %d (%s); the rest ended or went quiet.",
			c.Blocked, plural(c.Blocked, "prompt", "prompts"), c.Overridden, pct0(c.OverriddenShare), c.Cleared, pct0(c.ClearedShare)))
	}
	var other []string
	if c.Advised > 0 {
		other = append(other, fmt.Sprintf("warned %d %s", c.Advised, plural(c.Advised, "time", "times")))
	}
	if c.WouldBlock > 0 {
		other = append(other, fmt.Sprintf("in shadow would have stopped %d", c.WouldBlock))
	}
	if c.WouldAdvise > 0 {
		other = append(other, fmt.Sprintf("in shadow would have warned %d %s", c.WouldAdvise, plural(c.WouldAdvise, "time", "times")))
	}
	if len(other) > 0 {
		s := strings.Join(other, ", ")
		line(strings.ToUpper(s[:1]) + s[1:] + " (measured).")
	}
	if c.Cleared > 0 {
		line(fmt.Sprintf("Context not written to the cache again after a clear: about %s tokens (estimated, less %d per note you may have pasted).",
			FormatTokens(c.TokensAvoided.Value), measure.ColdNoteTokens))
	}
	if h := c.HourAfter; h != nil {
		line(fmt.Sprintf("In the hour after, the 5-hour meter moved %s points after a stop (%d) and %s after a prompt shadow would have stopped (%d); difference %s points, 95%% interval %s to %s (measured, matched by rebuild size).",
			signed(h.Guard.Value), h.Guard.N, signed(h.Shadow.Value), h.Shadow.N, signed(h.Diff.Value), signed(h.DiffLow), signed(h.DiffHi)))
	}
	if c.HourAfterNote != "" {
		line(c.HourAfterNote)
	}
}

func pct0(f *measure.Figure) string {
	if f == nil {
		return "-"
	}
	return fmt.Sprintf("%.0f%%", f.Value)
}

func signed(v float64) string { return fmt.Sprintf("%+.1f", v) }
