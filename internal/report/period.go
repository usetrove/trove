package report

import (
	"fmt"
	"time"

	"github.com/usetrove/trove/internal/measure"
)

// Period names. They match the budget periods, so a report and a budget cover
// the same days.
const (
	Today = "today"
	Week  = "week"
	Month = "month"
	Since = "since"
)

// Period is the span a report covers: [From, To) in local time.
type Period struct {
	Name string    `json:"name"`
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// Window converts the period to the measurement layer's window.
func (p Period) Window() measure.Window { return measure.Window{From: p.From, To: p.To} }

// Label is a short description such as "this month (since Oct 1)".
func (p Period) Label() string {
	from := p.From.Format("Jan 2")
	switch p.Name {
	case Today:
		return "today"
	case Week:
		return "this week (since " + from + ")"
	case Month:
		return "this month (since " + from + ")"
	default:
		return "since " + p.From.Format("Jan 2, 2006")
	}
}

// StartOf returns the first instant of the named period containing now, in now's
// location. Weeks start on Monday.
func StartOf(name string, now time.Time) time.Time {
	y, m, d := now.Date()
	day := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	switch name {
	case Week:
		back := (int(day.Weekday()) + 6) % 7
		return day.AddDate(0, 0, -back)
	case Month:
		return time.Date(y, m, 1, 0, 0, 0, 0, now.Location())
	default:
		return day
	}
}

// EndOf returns the first instant after the named period that starts at from.
func EndOf(name string, from time.Time) time.Time {
	switch name {
	case Week:
		return from.AddDate(0, 0, 7)
	case Month:
		return from.AddDate(0, 1, 0)
	default:
		return from.AddDate(0, 0, 1)
	}
}

// NewPeriod builds the period for a report. name is today, week, month or since;
// since is a date (YYYY-MM-DD) used only when name is since. The window ends just
// after now so the newest request is included.
func NewPeriod(name, since string, now time.Time) (Period, error) {
	end := now.Add(time.Millisecond)
	switch name {
	case Today, Week, Month:
		return Period{Name: name, From: StartOf(name, now), To: end}, nil
	case Since:
		t, err := time.ParseInLocation(time.DateOnly, since, now.Location())
		if err != nil {
			return Period{}, fmt.Errorf("--since is %q; use a date such as 2026-10-01", since)
		}
		if t.After(now) {
			return Period{}, fmt.Errorf("--since %s is in the future; use a date on or before today", since)
		}
		return Period{Name: Since, From: t, To: end}, nil
	}
	return Period{}, fmt.Errorf("unknown period %q; use today, week, month or since", name)
}
