package measure

import "time"

// Label says how a number was obtained (CLAUDE.md rule 6). Nothing is shown to
// a user without one.
type Label string

const (
	// Measured means read straight from provider-reported usage or counted from
	// recorded requests.
	Measured Label = "measured"
	// Estimated means derived with a model, an assumption or a counterfactual.
	Estimated Label = "estimated"
)

// Figure is one number with its label and the sample it came from.
type Figure struct {
	Value float64 `json:"value"`
	Label Label   `json:"label"`
	// N is the number of requests (or sessions, as the field using it says).
	N int `json:"n"`
	// Unpriced counts items left out of a dollar figure because their cost
	// could not be priced exactly.
	Unpriced int `json:"unpriced,omitempty"`
}

// Window is a half-open time range [From, To).
type Window struct{ From, To time.Time }

// Settings keys in the settings table.
const (
	keyInstallAt = "measure.install_at"
)

// guardrailPrefix marks an optimizations.reason written by a rollback, so it is
// never confused with any other use of that table.
const guardrailPrefix = "guardrail: "

// Plain-English statements the report may print. They are constants so the
// wording stays honest everywhere it appears.
const (
	TextNotEnough = "not enough data yet"
	// TextNoRegression is the strongest thing local data can support. It
	// deliberately does not say quality is unchanged.
	TextNoRegression = "no regression detected locally; output quality was verified on benchmarks (Prompt 8), not on your data"
)
