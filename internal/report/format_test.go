package report

import (
	"testing"
	"time"

	"github.com/usetrove/trove/internal/measure"
)

func TestFormatUSD(t *testing.T) {
	for in, want := range map[float64]string{
		0: "$0", 0.004: "<$0.01", 0.45: "$0.45", 12.345: "$12.35", 99.99: "$99.99",
		100.4: "$100", 612.5: "$613", 1500: "$1,500", 1234567.8: "$1,234,568",
	} {
		if got := FormatUSD(in); got != want {
			t.Errorf("FormatUSD(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatTokensAndPercent(t *testing.T) {
	for in, want := range map[float64]string{87: "87", 1200: "1.2K", 1000: "1K", 12400: "12K", 340000: "340K", 1.6e6: "1.6M", 2e9: "2B"} {
		if got := FormatTokens(in); got != want {
			t.Errorf("FormatTokens(%v) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[float64]string{0: "0%", 0.4: "<1%", 41.4: "41%", 99.6: "100%"} {
		if got := FormatPercent(in); got != want {
			t.Errorf("FormatPercent(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestEstMarksOnlyEstimates(t *testing.T) {
	if got := Est(measure.Figure{Value: 47, Label: measure.Estimated}); got != "~$47.00" {
		t.Errorf("got %q", got)
	}
	if got := Est(measure.Figure{Value: 47, Label: measure.Measured}); got != "$47.00" {
		t.Errorf("got %q", got)
	}
	if got := Est(measure.Figure{Value: 0, Label: measure.Estimated}); got != "$0" {
		t.Errorf("a zero is not an estimate: got %q", got)
	}
}

func TestPeriods(t *testing.T) {
	n := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC) // a Thursday
	for name, want := range map[string]string{Today: "2026-10-29", Week: "2026-10-26", Month: "2026-10-01"} {
		p, err := NewPeriod(name, "", n)
		if err != nil || p.From.Format(time.DateOnly) != want {
			t.Errorf("%s starts %v (err %v), want %s", name, p.From, err, want)
		}
		if !p.To.After(n) {
			t.Errorf("%s ends before now, so the newest request would be missed", name)
		}
	}
	if p, err := NewPeriod(Since, "2026-09-15", n); err != nil || p.Label() != "since Sep 15, 2026" {
		t.Errorf("since: %+v %v", p, err)
	}
	for _, bad := range []string{"15/09/2026", "2027-01-01", ""} {
		if _, err := NewPeriod(Since, bad, n); err == nil {
			t.Errorf("--since %q should be rejected", bad)
		}
	}
	if _, err := NewPeriod("year", "", n); err == nil {
		t.Error("unknown period should be rejected")
	}
	sunday := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	if got := StartOf(Week, sunday).Format(time.DateOnly); got != "2026-10-26" {
		t.Errorf("a Sunday belongs to the week that started Monday Oct 26, got %s", got)
	}
}
