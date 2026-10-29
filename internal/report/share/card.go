// Package share draws the shareable summary card as an SVG and a PNG.
//
// The card is built from a Card value and nothing else. Card has no field that
// can hold a repository, file, prompt, model or agent name, so none can appear
// on the image. Nothing here touches the network.
package share

import (
	"fmt"
	"time"

	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/report"
)

// Card is everything printed on the card. All dollar amounts are estimates from
// list prices, and the card says so.
type Card struct {
	// Period is a short label such as "Oct 2026" or "Since Sep 15".
	Period string `json:"period"`
	// SpendUSD, WasteUSD and SavedUSD are estimated. WastePercent is waste as a
	// percent of spend (estimated). Waste is a lower bound.
	SpendUSD     float64 `json:"spend_usd"`
	WasteUSD     float64 `json:"waste_usd"`
	WastePercent float64 `json:"waste_percent"`
	SavedUSD     float64 `json:"saved_usd"`
	// TopLeak is the name of the biggest waste category, from a fixed list of
	// plain-English names; empty when there is none.
	TopLeak string `json:"top_leak"`
	// Label is "estimated"; it is printed on the card.
	Label measure.Label `json:"label"`
}

// FromReport builds the card from a report. It copies the numbers unchanged.
// The second result is false when there is nothing worth sharing yet.
func FromReport(r report.Report) (Card, bool) {
	if r.Requests == 0 || r.Headline.Spend.Value <= 0 {
		return Card{}, false
	}
	c := Card{
		Period:       periodLabel(r.Period),
		SpendUSD:     r.Headline.Spend.Value,
		WasteUSD:     r.Headline.Waste.Value,
		WastePercent: r.Headline.WastePercent.Value,
		SavedUSD:     r.Headline.Savings.Value,
		Label:        measure.Estimated,
	}
	for _, l := range r.Leaks { // biggest first; skip anything not on the fixed list
		if title, ok := report.LeakTitle(l.Category); ok {
			c.TopLeak = title
			break
		}
	}
	return c, true
}

func periodLabel(p report.PeriodInfo) string {
	from, err := time.Parse(time.DateOnly, p.From)
	if err != nil {
		return "this period"
	}
	switch p.Name {
	case report.Today:
		return from.Format("Jan 2, 2006")
	case report.Week:
		return "Week of " + from.Format("Jan 2")
	case report.Month:
		return from.Format("Jan 2006")
	}
	return "Since " + from.Format("Jan 2, 2006")
}

// Lines is the exact text the card will show, for the preview the user approves
// before anything is saved.
func (c Card) Lines() []string {
	lines := []string{
		"Period        " + c.Period,
		"Spend         " + c.spend() + " (estimated)",
		"Waste found   " + c.waste() + " (estimated)",
		"Saved         " + c.saved() + " (estimated)",
	}
	lines = append(lines, "Biggest leak  "+c.leak())
	return lines
}

func (c Card) spend() string { return est(c.SpendUSD) }

func (c Card) waste() string {
	if c.WasteUSD <= 0 {
		return "none yet"
	}
	s := "at least " + est(c.WasteUSD)
	if c.WastePercent > 0 {
		s += ", " + report.FormatPercent(c.WastePercent) + " of spend"
	}
	return s
}

func (c Card) saved() string {
	if c.SavedUSD < 0.01 {
		return "none yet"
	}
	return est(c.SavedUSD)
}

func (c Card) leak() string {
	if c.TopLeak == "" {
		return "none found yet"
	}
	return c.TopLeak
}

func reportPercent(v float64) string { return report.FormatPercent(v) }

// est is a dollar estimate marked with "~". On the card, amounts from $10 up are
// whole dollars, so three numbers side by side do not look falsely precise.
func est(v float64) string {
	switch {
	case v < 0.01:
		return report.FormatUSD(v)
	case v >= 10 && v < 100:
		return fmt.Sprintf("~$%.0f", v)
	}
	return "~" + report.FormatUSD(v)
}
