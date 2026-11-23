package report

import (
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
