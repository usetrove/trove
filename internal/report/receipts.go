package report

import (
	"fmt"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/measure"
)

// ReceiptLine is the one-line form of a receipt, used by notifications and the
// status line. Dollar amounts are estimates and say so.
func ReceiptLine(r measure.Receipt) string {
	parts := []string{fmt.Sprintf("%s (estimated)", costText(r.Cost)), fmt.Sprintf("%d %s", r.Calls, plural(r.Calls, "call", "calls"))}
	if r.CacheHit != nil {
		parts = append(parts, fmt.Sprintf("%s cache hits (measured)", FormatPercent(r.CacheHit.Value)))
	}
	if r.Saved.Value >= 0.01 {
		parts = append(parts, fmt.Sprintf("saved %s (estimated)", Est(r.Saved)))
	}
	return strings.Join(parts, ", ")
}

func costText(f measure.Figure) string {
	s := Est(f)
	if f.Unpriced > 0 {
		s = "at least " + s
	}
	return s
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// RenderReceipts lists receipts, newest first. idleMinutes explains when a
// session counts as finished.
func RenderReceipts(rs []measure.Receipt, idleMinutes int, o Options) string {
	if len(rs) == 0 {
		return wrap(fmt.Sprintf("No finished sessions yet. A session finishes after %d minutes without requests, then its receipt appears here. Receipts are written while `trove gateway` is running, and `trove receipts` also catches up when you run it.", idleMinutes),
			o.width(), "", "")
	}
	loc := o.Loc
	if loc == nil {
		loc = time.Local
	}
	var b strings.Builder
	b.WriteString(wrap("Recent sessions. Dollar amounts are estimated from list prices; cache hits are measured.", o.width(), "", "") + "\n")
	for _, r := range rs {
		model := r.Model
		if r.Models > 1 {
			model += fmt.Sprintf(" (+%d more)", r.Models-1)
		}
		head := strings.Join([]string{r.EndedAt.In(loc).Format("Jan 2 15:04"), orUnknown(r.Agent), orUnknown(model)}, " · ")
		b.WriteString("\n" + wrap(head, o.width(), "", "  ") + "\n")
		b.WriteString(wrap(ReceiptLine(r), o.width(), "  ", "  ") + "\n")
	}
	return b.String()
}
