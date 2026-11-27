package report

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/measure"
)

// PlanUsageSource says where the plan-usage meter comes from: Claude Code passes it to the status line
// command on stdin. Trove never calls a usage endpoint or reads a web page for it (PRINCIPLES.md rule 11).
const PlanUsageSource = "claude_code"

// PlanUsage is the subscription meter Claude Code reports: the share of the 5-hour and weekly limits used.
// Either window may be nil; Claude Code sends them only to Pro, Max and Ultra subscribers, after the
// session's first response.
type PlanUsage struct {
	Source   string       `json:"source"`
	FiveHour *LimitWindow `json:"five_hour,omitempty"`
	SevenDay *LimitWindow `json:"seven_day,omitempty"`
}

// LimitWindow is one window of the meter. UsedPercent is as Claude Code reported it, so it is labeled
// measured. ResetsAt is nil when Claude Code sent no valid reset time.
type LimitWindow struct {
	UsedPercent float64       `json:"used_percent"`
	Label       measure.Label `json:"label"`
	ResetsAt    *time.Time    `json:"resets_at,omitempty"`
}

// current reports whether the window is still open at now. Claude Code drops a window once its reset
// time passes; a stale one is never shown (rule 9). A window without a reset time is taken as sent.
func (w *LimitWindow) current(now time.Time) bool {
	return w != nil && (w.ResetsAt == nil || w.ResetsAt.After(now))
}

// CurrentPlanUsage returns u without windows whose reset time has passed, or nil when none is left.
func CurrentPlanUsage(u *PlanUsage, now time.Time) *PlanUsage {
	if u == nil {
		return nil
	}
	out := PlanUsage{Source: u.Source}
	if u.FiveHour.current(now) {
		out.FiveHour = u.FiveHour
	}
	if u.SevenDay.current(now) {
		out.SevenDay = u.SevenDay
	}
	if out.FiveHour == nil && out.SevenDay == nil {
		return nil
	}
	return &out
}

// RenderPlanUsage is the meter for the status line, for example "5h 23% · wk 41%". Percentages are
// rounded down, so 99.6% shows as 99%: the line never says a limit is reached before it is. No "~":
// the numbers are Claude Code's own, not Trove's estimates.
func RenderPlanUsage(u PlanUsage) string {
	var parts []string
	if u.FiveHour != nil {
		parts = append(parts, "5h "+floorPercent(u.FiveHour.UsedPercent))
	}
	if u.SevenDay != nil {
		parts = append(parts, "wk "+floorPercent(u.SevenDay.UsedPercent))
	}
	return strings.Join(parts, " · ")
}

func floorPercent(v float64) string {
	if v > 0 && v < 1 {
		return "<1%"
	}
	return fmt.Sprintf("%d%%", int(math.Floor(v)))
}
