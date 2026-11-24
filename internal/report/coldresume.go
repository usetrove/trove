package report

import (
	"fmt"
	"strings"

	"github.com/usetrove/trove/internal/measure"
)

// guardWords are what differs between the rebuild guards' sections.
type guardWords struct {
	title        string // section heading
	one, many    string // what the guard stops: "prompt", "prompts"
	again        string // what "overridden" means: "sent %d again"
	shadowObject string // in the hour-after line: "a prompt"
}

var (
	coldResumeWords  = guardWords{"Cold-resume guard (hook_cold_resume)", "prompt", "prompts", "You sent %d again", "a prompt"}
	coldCompactWords = guardWords{"Cold-compact guard (hook_cold_compact)", "/compact", "/compacts", "You ran %d again", "a /compact"}
)

// renderColdResume is the cold-resume guard's section: what it did (measured), what users did next (measured),
// the rebuilds it avoided (estimated) and the hour-after comparison of the 5-hour meter (measured, not randomized).
func renderColdResume(b *strings.Builder, c *measure.ColdResume, o Options) {
	renderGuard(b, coldResumeWords, c, o)
}

// renderColdCompact is the same section for the cold-compact guard.
func renderColdCompact(b *strings.Builder, c *measure.ColdResume, o Options) {
	renderGuard(b, coldCompactWords, c, o)
}

func renderGuard(b *strings.Builder, w guardWords, c *measure.ColdResume, o Options) {
	b.WriteString("\n" + heading(w.title))
	line := func(s string) { b.WriteString(wrap(s, o.width(), "  ", "  ") + "\n") }
	if c.Blocked > 0 {
		line(fmt.Sprintf("Stopped %d %s once (measured). "+w.again+" (%s) and cleared %d (%s); the rest ended or went quiet.",
			c.Blocked, plural(c.Blocked, w.one, w.many), c.Overridden, pct0(c.OverriddenShare), c.Cleared, pct0(c.ClearedShare)))
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
		line(fmt.Sprintf("In the hour after, the 5-hour meter moved %s points after a stop (%d) and %s after %s shadow would have stopped (%d); difference %s points, 95%% interval %s to %s (measured, matched by rebuild size).",
			signed(h.Guard.Value), h.Guard.N, signed(h.Shadow.Value), w.shadowObject, h.Shadow.N, signed(h.Diff.Value), signed(h.DiffLow), signed(h.DiffHi)))
	}
	if c.HourAfterNote != "" {
		line(c.HourAfterNote)
	}
}

// renderModelSwitch is the model-switch guard's section. It only ever warns, so there is nothing stopped and no
// hour-after comparison: what matters is how often the switch went ahead anyway.
func renderModelSwitch(b *strings.Builder, c *measure.ColdResume, o Options) {
	b.WriteString("\n" + heading("Model-switch guard (hook_model_switch)"))
	line := func(s string) { b.WriteString(wrap(s, o.width(), "  ", "  ") + "\n") }
	if c.Advised > 0 {
		line(fmt.Sprintf("Showed the cost of a switch %d %s (measured). You switched anyway %d %s (%s); Claude Code's own confirmation decided.",
			c.Advised, plural(c.Advised, "time", "times"), c.Overridden, plural(c.Overridden, "time", "times"), pct0(c.OverriddenShare)))
	}
	if c.WouldAdvise > 0 {
		line(fmt.Sprintf("In shadow would have shown it %d %s (measured).", c.WouldAdvise, plural(c.WouldAdvise, "time", "times")))
	}
}

func pct0(f *measure.Figure) string {
	if f == nil {
		return "-"
	}
	return fmt.Sprintf("%.0f%%", f.Value)
}

func signed(v float64) string { return fmt.Sprintf("%+.1f", v) }
