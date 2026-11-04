package external

import "math"

// bytesPerToken is the same rough conversion the optimizers use before a
// session supplies its own. Every figure derived from it is an estimate.
const bytesPerToken = 3.5

// EstimateTokens estimates the tokens in injected text.
func EstimateTokens(text string) int {
	return int(math.Ceil(float64(len(text)) / bytesPerToken))
}

// Charge is what a tool adds to every request while on, and where each figure
// came from. All figures are estimates until a tool-specific adapter measures
// them (for an MCP server, the size of its real tools/list).
type Charge struct {
	ToolDefTokens  int     `json:"tool_def_tokens"`
	InjectedTokens int     `json:"injected_tokens"`
	PerRequest     int     `json:"per_request_tokens"`
	ExtraTurns     float64 `json:"extra_turns_estimate"`
	Source         string  `json:"source"` // "declared" or "counted from the rules text"
	Label          string  `json:"label"`  // always "estimated"
}

// ChargeFor returns the per-request charge. For a rules file the injected size
// is counted from the rules text itself, never trusted from the manifest, so a
// manifest cannot understate what it adds. Otherwise the declared figures are
// used and labelled as declared.
func ChargeFor(m Manifest) Charge {
	c := Charge{ToolDefTokens: m.Overhead.ToolDefTokens, InjectedTokens: m.Overhead.InjectedTokens,
		ExtraTurns: m.Overhead.ExtraTurnsEstimate, Source: "declared", Label: "estimated"}
	if m.ActsAs == ActsRulesFile {
		c.InjectedTokens = EstimateTokens(m.Rules)
		c.Source = "counted from the rules text"
	}
	c.PerRequest = c.ToolDefTokens + c.InjectedTokens
	return c
}
