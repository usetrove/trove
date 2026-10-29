package report

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/store"
)

// Pacing statuses.
const (
	PaceUnder   = "under"    // spending less than the share of the period gone
	PaceOnPace  = "on_pace"  // roughly in step
	PaceAhead   = "ahead"    // spending faster than the period is passing
	PaceEarly   = "early"    // too little of the period has passed to judge
	PaceNearCap = "near_cap" // at or past the warning level
	PaceOver    = "over"     // the cap is reached
)

// earlyFraction is the share of the period that must pass before pace is judged.
const earlyFraction = 0.05

// paceTolerance is how many percentage points ahead of the calendar still counts as on pace.
const paceTolerance = 2.0

// PeriodName maps a config budget period to a report period name.
func PeriodName(budgetPeriod string) string {
	switch budgetPeriod {
	case "daily":
		return Today
	case "weekly":
		return Week
	}
	return Month
}

// BudgetPeriod maps a flag value (day, week, month and the config spellings) to a
// config budget period. ok is false for anything else.
func BudgetPeriod(s string) (string, bool) {
	switch s {
	case "day", "daily", "today":
		return "daily", true
	case "week", "weekly":
		return "weekly", true
	case "month", "monthly":
		return "monthly", true
	}
	return "", false
}

// BuildPacing reports spend against the budget cap for the current budget
// period. It returns nil when no cap is set.
func BuildPacing(ctx context.Context, db *store.DB, cfg config.Config, now time.Time) (*Pacing, error) {
	if cfg.Budget.CapUSD <= 0 {
		return nil, nil
	}
	name := PeriodName(cfg.Budget.Period)
	from := StartOf(name, now)
	to := EndOf(name, from)
	rows, err := measure.TotalsBy(ctx, db, measure.Window{From: from, To: now.Add(time.Millisecond)}, measure.ByAgent)
	if err != nil {
		return nil, err
	}
	spent, _, _ := sumTotals(rows)
	p := Pace(cfg.Budget, name, spent, from, to, now)
	cats, err := measure.WasteByCategory(ctx, db, measure.Window{From: from, To: now.Add(time.Millisecond)})
	if err != nil {
		return nil, err
	}
	p.Lever = lever(cats, name)
	return p, nil
}

// lever names what would change the pace most: the leak with the most dollars
// in the budget period. It is empty when no leak has a dollar figure.
func lever(cats []measure.WasteRow, name string) string {
	var best *measure.WasteRow
	for i := range cats {
		if cats[i].USD.Value > 0 && (best == nil || cats[i].USD.Value > best.USD.Value) {
			best = &cats[i]
		}
	}
	if best == nil {
		return ""
	}
	title := best.Category
	if info, ok := leakInfos[best.Category]; ok {
		title = info.title
	}
	when := "this " + periodNoun(name)
	if name == Today {
		when = "today"
	}
	return fmt.Sprintf("The biggest leak is %s, about %s %s (estimated). Fixing it is the largest change available.", strings.ToLower(title), FormatUSD(best.USD.Value), when)
}

// Pace is the pure pacing calculation, shared with the status line.
func Pace(b config.Budget, name string, spent measure.Figure, from, to, now time.Time) *Pacing {
	p := &Pacing{
		Period: b.Period, Name: name, From: day(from), To: day(to.Add(-time.Millisecond)),
		CapUSD: b.CapUSD, Spent: spent,
	}
	total := to.Sub(from).Seconds()
	elapsed := math.Min(math.Max(now.Sub(from).Seconds(), 0), total)
	frac := elapsed / total
	p.PercentUsed = percent(spent.Value, b.CapUSD)
	p.PercentElapsed = 100 * frac
	p.CapReached = spent.Value >= b.CapUSD

	// A projection needs some spending and enough of the period to be steady.
	reachesCap := false
	if spent.Value > 0 && frac >= earlyFraction {
		proj := spent.Value / frac
		p.ProjectedTotal = &measure.Figure{Value: round6(proj), Label: measure.Estimated, N: spent.N, Unpriced: spent.Unpriced}
		if !p.CapReached && proj >= b.CapUSD {
			rate := spent.Value / elapsed // dollars per second
			at := now.Add(time.Duration((b.CapUSD - spent.Value) / rate * float64(time.Second)))
			if at.Before(to) {
				p.ProjectedCapDate = day(at)
				reachesCap = true
			}
		}
	}

	switch {
	case p.CapReached:
		p.Status = PaceOver
	case p.PercentUsed >= float64(b.WarnAtPercent):
		p.Status = PaceNearCap
	case frac < earlyFraction:
		p.Status = PaceEarly
	case reachesCap || p.PercentUsed > p.PercentElapsed+paceTolerance:
		p.Status = PaceAhead
	case p.PercentUsed >= p.PercentElapsed-paceTolerance:
		p.Status = PaceOnPace
	default:
		p.Status = PaceUnder
	}
	return p
}
