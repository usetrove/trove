package report

import (
	"testing"
	"time"

	"github.com/usetrove/trove/internal/measure"
)

func TestRenderPlanUsage(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	later, earlier := now.Add(time.Hour), now.Add(-time.Minute)
	win := func(pct float64, resets *time.Time) *LimitWindow {
		return &LimitWindow{UsedPercent: pct, Label: measure.Measured, ResetsAt: resets}
	}
	tests := []struct {
		name string
		in   *PlanUsage
		want string // "" = nothing shown
	}{
		{"both windows", &PlanUsage{FiveHour: win(23.5, &later), SevenDay: win(41.2, &later)}, "5h 23% · wk 41%"},
		{"five-hour only", &PlanUsage{FiveHour: win(23.5, &later)}, "5h 23%"},
		{"weekly only", &PlanUsage{SevenDay: win(41.2, &later)}, "wk 41%"},
		{"no windows", &PlanUsage{}, ""},
		{"no meter", nil, ""},
		{"five-hour window already reset", &PlanUsage{FiveHour: win(97, &earlier), SevenDay: win(41.2, &later)}, "wk 41%"},
		{"every window already reset", &PlanUsage{FiveHour: win(97, &earlier)}, ""},
		{"no reset time is shown as sent", &PlanUsage{FiveHour: win(5, nil)}, "5h 5%"},
		{"rounded down: not at the limit yet", &PlanUsage{FiveHour: win(99.6, &later)}, "5h 99%"},
		{"at the limit", &PlanUsage{FiveHour: win(100, &later)}, "5h 100%"},
		{"under one percent", &PlanUsage{FiveHour: win(0.4, &later), SevenDay: win(0, &later)}, "5h <1% · wk 0%"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			u := CurrentPlanUsage(tc.in, now)
			got := ""
			if u != nil {
				got = RenderPlanUsage(*u)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
