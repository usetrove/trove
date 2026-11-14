package autopilot

import (
	"fmt"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/config"
)

// Summary is the weekly view of what Autopilot did. Names, states and plain-English
// reasons only; it states no saving figure, so there is nothing to label.
type Summary struct {
	Days int `json:"days"`
	// Changes are the state changes in the last Days days, oldest first.
	Changes []Change `json:"changes,omitempty"`
	// On lists the optimizers Autopilot has on now.
	On []string `json:"on,omitempty"`
	// Learning lists those still in shadow or waiting as a candidate.
	Learning []string `json:"learning,omitempty"`
	// RolledBack lists those a guardrail turned back, with the reason.
	RolledBack []Change `json:"rolled_back,omitempty"`
	// Plan is the shadow planner's advice, which changes nothing.
	Plan []config.AutopilotPlanItem `json:"plan,omitempty"`
}

// Change is one state change for display.
type Change struct {
	Date      string `json:"date"`
	Optimizer string `json:"optimizer"`
	State     string `json:"state"`
	Reason    string `json:"reason,omitempty"`
}

// Summarize builds the summary. It returns nil when Autopilot is not on, so a
// report for someone who never agreed to it says nothing about it.
func Summarize(f config.AutopilotFile, now time.Time, days int) *Summary {
	if !f.Enabled {
		return nil
	}
	s := &Summary{Days: days, Plan: f.Plan}
	cut := now.AddDate(0, 0, -days).UnixMilli()
	for _, e := range f.History {
		if e.AtMS >= cut {
			s.Changes = append(s.Changes, Change{Date: time.UnixMilli(e.AtMS).Format("2006-01-02"), Optimizer: e.Optimizer, State: e.To, Reason: e.Reason})
		}
	}
	for _, n := range f.Names() {
		e := f.Entries[n]
		switch e.State {
		case config.AutopilotOn:
			s.On = append(s.On, n)
		case config.AutopilotShadow, config.AutopilotCandidate, config.AutopilotProbation:
			s.Learning = append(s.Learning, n)
		case config.AutopilotRolledBack:
			s.RolledBack = append(s.RolledBack, Change{Date: time.UnixMilli(e.SinceMS).Format("2006-01-02"), Optimizer: n, State: e.State, Reason: e.Reason})
		}
	}
	return s
}

// Text renders the summary for `trove report`, under an "Autopilot" heading.
func (s *Summary) Text() string {
	if s == nil {
		return ""
	}
	return "Autopilot\n" + s.Body()
}

// Body is Text without the heading, for `trove autopilot`, which has its own.
func (s *Summary) Body() string {
	if s == nil {
		return ""
	}
	var b strings.Builder
	if len(s.On) > 0 {
		fmt.Fprintf(&b, "  On now: %s\n", strings.Join(s.On, ", "))
	} else {
		b.WriteString("  Nothing turned on yet.\n")
	}
	if len(s.Learning) > 0 {
		fmt.Fprintf(&b, "  Collecting shadow estimates: %s\n", strings.Join(s.Learning, ", "))
	}
	for _, r := range s.RolledBack {
		fmt.Fprintf(&b, "  Rolled back to shadow on %s: %s (%s)\n", r.Date, r.Optimizer, r.Reason)
	}
	return b.String() + s.Details()
}

// Details is the part of Body about recent changes and the planner's advice.
func (s *Summary) Details() string {
	if s == nil {
		return ""
	}
	var b strings.Builder
	if len(s.Changes) == 0 {
		fmt.Fprintf(&b, "  No changes in the last %d days.\n", s.Days)
	} else {
		fmt.Fprintf(&b, "  Changes in the last %d days:\n", s.Days)
		for _, c := range s.Changes {
			line := fmt.Sprintf("    %s  %s -> %s", c.Date, c.Optimizer, c.State)
			if c.Reason != "" {
				line += "  (" + c.Reason + ")"
			}
			b.WriteString(line + "\n")
		}
	}
	var advice []string
	for _, it := range s.Plan {
		if it.Verdict != VerdictOn {
			advice = append(advice, fmt.Sprintf("    %-28s %s: %s", it.Optimizer, strings.ReplaceAll(it.Verdict, "_", " "), it.Reason))
		}
	}
	if len(advice) > 0 {
		b.WriteString("  Planner advice (changes nothing; savings are estimated):\n" + strings.Join(advice, "\n") + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
