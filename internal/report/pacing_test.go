package report

import (
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/measure"
)

func TestPace(t *testing.T) {
	b := config.Budget{Period: "monthly", CapUSD: 1000, WarnAtPercent: 80}
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	to := EndOf(Month, from) // 31 days
	spent := func(v float64) measure.Figure { return measure.Figure{Value: v, Label: measure.Estimated, N: 10} }
	at := func(days float64) time.Time { return from.Add(time.Duration(days * 24 * float64(time.Hour))) }

	tests := []struct {
		name    string
		spent   float64
		day     float64
		status  string
		capDate string // "" means none projected
		reached bool
	}{
		{"first hours of the month", 40, 0.5, PaceEarly, "", false},
		{"under pace", 200, 15.5, PaceUnder, "", false},
		{"on pace", 500, 15.5, PaceOnPace, "", false},
		{"ahead of pace reaches the cap before month end", 600, 15.5, PaceAhead, "2026-10-26", false},
		{"near the warning level", 850, 20, PaceNearCap, "2026-10-24", false},
		{"cap reached", 1000, 10, PaceOver, "", true},
		{"over the cap", 1200, 10, PaceOver, "", true},
		{"nothing spent yet", 0, 10, PaceUnder, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := Pace(b, Month, spent(tc.spent), from, to, at(tc.day))
			if p.Status != tc.status {
				t.Errorf("status %q, want %q (used %.1f%%, elapsed %.1f%%)", p.Status, tc.status, p.PercentUsed, p.PercentElapsed)
			}
			if p.CapReached != tc.reached {
				t.Errorf("cap reached %v, want %v", p.CapReached, tc.reached)
			}
			if tc.capDate != "" && p.ProjectedCapDate != tc.capDate {
				t.Errorf("projected cap date %q, want %q", p.ProjectedCapDate, tc.capDate)
			}
			if tc.capDate == "" && p.ProjectedCapDate != "" {
				t.Errorf("unexpected projected cap date %q", p.ProjectedCapDate)
			}
			if p.ProjectedTotal != nil && p.ProjectedTotal.Label != measure.Estimated {
				t.Error("a projection must be labeled estimated")
			}
		})
	}
}

func TestPacingLineColorMeansSomething(t *testing.T) {
	mk := func(status string) *Pacing {
		return &Pacing{Name: Month, CapUSD: 100, Spent: measure.Figure{Value: 50, Label: measure.Estimated}, PercentUsed: 50, PercentElapsed: 30, Status: status}
	}
	for status, code := range map[string]string{PaceAhead: "\x1b[33m", PaceNearCap: "\x1b[31m", PaceOver: "\x1b[31m"} {
		if got := RenderPacingLine(mk(status), Options{Color: true}); !strings.HasPrefix(got, code) {
			t.Errorf("%s: line is not colored %q: %q", status, code, got)
		}
	}
	for _, status := range []string{PaceUnder, PaceOnPace, PaceEarly} {
		if got := RenderPacingLine(mk(status), Options{Color: true}); strings.Contains(got, "\x1b[") {
			t.Errorf("%s must not be colored: %q", status, got)
		}
	}
	if got := RenderPacingLine(mk(PaceAhead), Options{}); strings.Contains(got, "\x1b[") {
		t.Errorf("no color was asked for: %q", got)
	}
}

func TestRenderPacingGoldens(t *testing.T) {
	b := config.Budget{Period: "monthly", CapUSD: 1500, WarnAtPercent: 80}
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	to := EndOf(Month, from)
	spent := func(v float64, unpriced int) measure.Figure {
		return measure.Figure{Value: v, Label: measure.Estimated, N: 40, Unpriced: unpriced}
	}
	var out strings.Builder
	cases := []struct {
		name string
		p    *Pacing
	}{
		{"none", nil},
		{"on pace", Pace(b, Month, spent(660, 0), from, to, from.AddDate(0, 0, 14))},
		{"close to ahead: projection reaches the cap", Pace(b, Month, spent(700, 0), from, to, from.AddDate(0, 0, 14))},
		{"ahead", func() *Pacing {
			p := Pace(b, Month, spent(1000, 3), from, to, from.AddDate(0, 0, 14))
			p.Lever = "The biggest leak, Repeated tool results, is estimated at $120 in this month (since Oct 1)."
			return p
		}()},
		{"over", Pace(b, Month, spent(1600, 0), from, to, from.AddDate(0, 0, 14))},
		{"early", Pace(b, Month, spent(10, 0), from, to, from.Add(10*time.Hour))},
	}
	for _, c := range cases {
		out.WriteString("== " + c.name + "\n" + RenderPacing(c.p, Options{}) + "\n")
	}
	golden(t, "pacing.txt", out.String())
}

// The gateway's spend cap and the report must agree on where a budget period starts and ends,
// so a cap measured by one matches the pacing shown by the other.
func TestBudgetWindowAgreesWithTheReportPeriods(t *testing.T) {
	loc := time.FixedZone("test", 5*3600+1800)
	for _, n := range []time.Time{
		time.Date(2026, 11, 13, 14, 30, 0, 0, loc), time.Date(2026, 11, 9, 0, 0, 0, 0, loc), time.Date(2026, 11, 15, 23, 59, 0, 0, loc),
		time.Date(2026, 12, 31, 23, 59, 0, 0, loc), time.Date(2027, 3, 1, 0, 0, 0, 0, loc),
	} {
		for _, period := range []string{"daily", "weekly", "monthly"} {
			from, to := config.Budget{Period: period}.Window(n)
			name := PeriodName(period)
			wantFrom := StartOf(name, n)
			wantTo := EndOf(name, wantFrom)
			if !from.Equal(wantFrom) || !to.Equal(wantTo) {
				t.Errorf("%s at %v: budget window %v to %v, report period %v to %v", period, n, from, to, wantFrom, wantTo)
			}
		}
	}
}
