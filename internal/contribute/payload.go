// Package contribute builds the anonymous, aggregate summary a user may choose to
// share to help validate Trove's savings claims.
//
// Nothing here touches the network. Build is a pure function of a report, and
// every field of Payload is listed in payload_test.go so that adding one is a
// reviewed decision. The payload holds only counts, percentages and fixed
// vocabulary: no prompts, paths, hashes, model names, dollar amounts or exact
// token counts.
package contribute

import (
	"strings"

	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/report"
)

// SchemaVersion changes whenever a field is added, removed or reinterpreted.
const SchemaVersion = 1

// Payload is everything that would be shared. Nothing outside it is ever sent.
type Payload struct {
	SchemaVersion int    `json:"schema_version"`
	TroveVersion  string `json:"trove_version"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	// PeriodDays is the length of the reported period, not its dates.
	PeriodDays int `json:"period_days"`
	// RequestsBucket is a coarse request count: "0", "1-19", "20-99", "100-999" or "1000+".
	RequestsBucket string `json:"requests_bucket"`
	// Agents are the agents seen, from a fixed list ("claude-code", "codex", "other").
	Agents []string `json:"agents"`
	// WastePercent is detected waste as a share of spend, rounded to a whole
	// percent. Label is "estimated".
	WastePercent *LabeledPercent `json:"waste_percent,omitempty"`
	// Optimizers has one row per optimizer, from a fixed list of names.
	Optimizers     []OptimizerSummary `json:"optimizers"`
	GuardrailTrips int                `json:"guardrail_trips"`
}

// LabeledPercent is a whole-number percent with its measured/estimated label.
type LabeledPercent struct {
	Percent int           `json:"percent"`
	Label   measure.Label `json:"label"`
}

// OptimizerSummary is one optimizer's state and, when enough data exists, its
// measured effect.
type OptimizerSummary struct {
	Name       string `json:"name"`
	Effective  string `json:"effective"` // on, shadow or off
	RolledBack bool   `json:"rolled_back"`
	// PercentFewer is the measured drop in prompt tokens per request, rounded to
	// a whole percent. Nil unless the on-versus-off comparison had enough data.
	PercentFewer *int `json:"percent_fewer_measured,omitempty"`
}

// Meta is what the report does not carry.
type Meta struct {
	TroveVersion string
	OS, Arch     string
}

// Build turns a report into the payload. It is deterministic.
func Build(r report.Report, m Meta) Payload {
	p := Payload{
		SchemaVersion:  SchemaVersion,
		TroveVersion:   m.TroveVersion,
		OS:             m.OS,
		Arch:           m.Arch,
		PeriodDays:     periodDays(r.Period),
		RequestsBucket: bucket(r.Requests),
		Agents:         agents(r.Spend.ByAgent),
		Optimizers:     []OptimizerSummary{},
		GuardrailTrips: len(r.Optimizers.Guardrails),
	}
	if r.Requests > 0 && r.Headline.WastePercent.Label != "" {
		p.WastePercent = &LabeledPercent{Percent: round(r.Headline.WastePercent.Value), Label: r.Headline.WastePercent.Label}
	}
	for _, o := range r.Optimizers.Rows {
		s := OptimizerSummary{Name: o.Name, Effective: o.Effective, RolledBack: o.RolledBack != ""}
		if o.Measured != nil && o.Measured.Enough && o.Measured.Reduced {
			v := round(o.Measured.Percent)
			s.PercentFewer = &v
		}
		p.Optimizers = append(p.Optimizers, s)
	}
	return p
}

func round(v float64) int {
	if v < 0 {
		return -int(-v + 0.5)
	}
	return int(v + 0.5)
}

func bucket(n int) string {
	switch {
	case n <= 0:
		return "0"
	case n < 20:
		return "1-19"
	case n < 100:
		return "20-99"
	case n < 1000:
		return "100-999"
	}
	return "1000+"
}

// agents maps whatever label the data holds onto a fixed vocabulary, so an
// unusual User-Agent can never leak through this field.
func agents(rows []measure.TotalsRow) []string {
	seen := map[string]bool{}
	for _, r := range rows {
		k := strings.ToLower(r.Key)
		switch {
		case strings.Contains(k, "claude"):
			seen["claude-code"] = true
		case strings.Contains(k, "codex"):
			seen["codex"] = true
		default:
			seen["other"] = true
		}
	}
	out := []string{}
	for _, n := range []string{"claude-code", "codex", "other"} {
		if seen[n] {
			out = append(out, n)
		}
	}
	return out
}

func periodDays(p report.PeriodInfo) int {
	from, err1 := parseDay(p.From)
	to, err2 := parseDay(p.To)
	if err1 != nil || err2 != nil || to.Before(from) {
		return 0
	}
	return int(to.Sub(from).Hours()/24) + 1
}
