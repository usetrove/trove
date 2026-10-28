package pricing

import "time"

// APIEquivalent is what a set of usage would have cost at API list prices. For
// subscription users (Phase B) this is an "estimated" value, not a bill: they do
// not pay per token.
type APIEquivalent struct {
	Cost  Cost   `json:"cost"`
	Label string `json:"label"` // always "estimated"
}

// APIEquivalentValue prices usage at standard API list prices as of at.
func (t *Table) APIEquivalentValue(provider, model string, at time.Time, u Usage) APIEquivalent {
	return APIEquivalent{
		Cost:  t.Cost(Request{Provider: provider, Model: model, Time: at, Usage: u}),
		Label: "estimated",
	}
}
