package report

import (
	"fmt"
	"strings"

	"github.com/usetrove/trove/internal/measure"
)

// renderCheckpoint is the session checkpoint's section: what it saved and restored (measured), the tokens the
// restores added (estimated) and the windows after /compact and /clear with and without the restore (measured).
func renderCheckpoint(b *strings.Builder, c *measure.CheckpointWindows, o Options) {
	b.WriteString("\n" + heading("Session checkpoint (hook_session_checkpoint)"))
	line := func(s string) { b.WriteString(wrap(s, o.width(), "  ", "  ") + "\n") }
	line(fmt.Sprintf("Kept %d %s; after /compact or /clear, restored a note %d %s and added the one-line pointer %d %s (measured). The restores added about %s tokens (estimated).",
		c.Saved, plural(c.Saved, "note", "notes"), c.Restored, plural(c.Restored, "time", "times"), c.Pointed, plural(c.Pointed, "time", "times"),
		FormatTokens(float64(c.With.AddedTokens))))
	arm := func(label string, a measure.CheckpointArm) {
		if a.Windows == 0 {
			line(label + ": no windows yet.")
			return
		}
		rec := "no edit within 20 calls"
		if a.Recovered > 0 {
			rec = fmt.Sprintf("first edit after %.1f calls on average in %d of %d", a.MeanCallsToRecover(), a.Recovered, a.Windows)
		}
		line(fmt.Sprintf("%s: %d %s; %d re-reads of unchanged files, %d repeated failed commands, %s (measured, first 20 tool calls).",
			label, a.Windows, plural(a.Windows, "window", "windows"), a.Rereads, a.RepeatFailures, rec))
	}
	arm("With the restore", c.With)
	arm("Without", c.Without)
	if c.With.Windows < 10 || c.Without.Windows < 10 {
		line("Too few windows on one side to compare yet; the guardrail waits for enough of both.")
	}
}
