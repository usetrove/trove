// Package pricing holds Trove's versioned price table and cost engine.
//
// Prices come from pricing.json, compiled into the binary; nothing here touches
// the network. Costs are exact decimals computed from published list prices, so
// anything built on them must label the result "estimated" unless it comes from
// a provider's own invoice or usage report ("measured" refers to token counts).
// When a price is missing the engine says so; it never substitutes a guess.
package pricing

// DataVersion identifies the bundled price table.
var DataVersion = func() string {
	t, err := Default()
	if err != nil {
		return "invalid"
	}
	return t.Version()
}()
