package experiment

import (
	"context"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/store"
)

// ShadowCaveat must accompany every shadow figure. Shadow mode forwards the
// original request, so it cannot see what an optimization changes about the
// agent's behaviour.
const ShadowCaveat = "estimated; shadow mode cannot see behaviour changes such as retries, extra turns or re-reads"

// Shadow is a shadow-mode estimate for one optimizer. It is never "measured".
type Shadow struct {
	Optimizer string         `json:"optimizer"`
	Tokens    measure.Figure `json:"tokens"`
	USD       measure.Figure `json:"usd"`
	Sessions  int            `json:"sessions"`
	Caveat    string         `json:"caveat"`
}

// ShadowEstimates wraps measure.ShadowEstimates (priced with cache effects)
// and attaches the caveat.
func ShadowEstimates(ctx context.Context, db *store.DB, w measure.Window, cfg config.Config) ([]Shadow, error) {
	rows, err := measure.ShadowEstimates(ctx, db, w, cfg)
	if err != nil {
		return nil, err
	}
	out := make([]Shadow, 0, len(rows))
	for _, r := range rows {
		out = append(out, Shadow{Optimizer: r.Optimizer, Tokens: r.Tokens, USD: r.USD, Sessions: r.Sessions, Caveat: ShadowCaveat})
	}
	return out, nil
}

// HarnessSavings is what trimming a tool result really saves when the agent
// already truncates large results. The agent would send min(size, cap) tokens
// without Trove; with the optimizer's output it sends min(trimmed, cap). Only
// the difference was ever going to be paid for. A cap of 0 means the agent does
// not truncate (or the cap is unknown), and the full difference counts. Trove
// ships no built-in caps: an agent's truncation limit is a documented agent
// detail that must be verified and set, never assumed.
func HarnessSavings(size, trimmed, harnessCap int64) int64 {
	if trimmed > size {
		trimmed = size
	}
	if harnessCap > 0 {
		size, trimmed = min(size, harnessCap), min(trimmed, harnessCap)
	}
	return size - trimmed
}
