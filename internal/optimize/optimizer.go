// Package optimize rewrites outgoing requests to remove waste, safely.
//
// The contract every optimizer follows (CLAUDE.md rules 4, 5 and 6):
//
//   - Pure and deterministic. Transform's output depends only on the request
//     bytes it is given. The agent resends its whole history every turn, so
//     every historical block must be transformed identically each time, or the
//     provider's cached prefix is invalidated. No clocks, randomness, maps in
//     iteration order, or state that survives between requests.
//   - Fail open. Any panic, error, malformed request or time-budget overrun
//     makes the engine forward the original request, unmodified.
//   - Session-boundary switching. An optimizer's state is pinned when a session
//     starts and never changes until the next session.
//   - Prove before acting. off = does nothing, shadow = runs and reports what it
//     would remove but forwards the original, on = forwards the result.
//
// Pipeline order is fixed; see Pipeline.
package optimize

import (
	"context"
	"encoding/json"

	"github.com/usetrove/trove/internal/config"
)

// Tier says how much an optimizer risks losing.
type Tier string

const (
	// TierLossless means the model receives the same information.
	TierLossless Tier = "lossless"
	// TierRecoverable means information is removed but can be fetched back.
	TierRecoverable Tier = "recoverable"
	// TierLossy means information is removed for good.
	TierLossy Tier = "lossy"
)

// Session is what an optimizer may know about the session a request is in.
type Session struct {
	Key string
	// Agent is the label from gateway/agent: claude_code, codex, sdk or other.
	Agent string
	// Custom is true only if the user explicitly marked this agent label as a
	// custom agent in config. Optimizers that change prompt layout require it.
	Custom bool
}

// Report says what one optimizer did to one request.
type Report struct {
	Name  string `json:"name"`
	State string `json:"state"`
	// Changed is true if the optimizer's output differs from its input.
	Changed bool `json:"changed"`
	// BytesRemoved is how much smaller the request body became (never negative).
	BytesRemoved int `json:"bytes_removed"`
	// TokensRemoved is an estimate: BytesRemoved divided by the session's pinned
	// bytes-per-token ratio. It is the same in shadow and on mode.
	TokensRemoved int64 `json:"tokens_removed"`
	// Err is set when the optimizer could not finish; the engine then forwards
	// the original request.
	Err string `json:"error,omitempty"`
	// Originals holds the full text of every output the optimizer shortened, so
	// the engine can store it before the shortened request is forwarded. It is
	// never recorded or serialized.
	Originals []string `json:"-"`
}

// Optimizer is one request transformation.
type Optimizer interface {
	Name() string
	Tier() Tier
	// Applies reports whether Transform could change this request. It must be
	// cheap and must not modify anything.
	Applies(r *Request, s Session) bool
	// Transform returns the new request and a report. It must return r itself
	// (same bytes) when there is nothing to change. It should stop early when
	// ctx is done.
	Transform(ctx context.Context, r *Request) (*Request, Report)
}

// Failure records an optimizer that did not complete. Only the optimizer name
// and a class are kept, never request content.
type Failure struct {
	Optimizer string `json:"optimizer"`
	// Class is one of: panic, timeout, error, malformed.
	Class string `json:"class"`
}

// Applied is the per-request record stored in optimizations_applied.
type Applied struct {
	Name          string `json:"name"`
	State         string `json:"state"`
	BytesRemoved  int    `json:"bytes_removed"`
	TokensRemoved int64  `json:"tokens_removed"`
	Failed        string `json:"failed,omitempty"`
}

// AppliedJSON encodes the applied list for the optimizations_applied column.
// Token numbers are estimates and the column says so by name.
func AppliedJSON(reports []Report, failures []Failure) []byte {
	if len(reports) == 0 && len(failures) == 0 {
		return nil
	}
	out := make([]Applied, 0, len(reports)+len(failures))
	for _, r := range reports {
		out = append(out, Applied{Name: r.Name, State: r.State, BytesRemoved: r.BytesRemoved, TokensRemoved: r.TokensRemoved})
	}
	for _, f := range failures {
		out = append(out, Applied{Name: f.Optimizer, State: "failed", Failed: f.Class})
	}
	b, err := json.Marshal(struct {
		Label     string    `json:"label"`
		Optimizer []Applied `json:"optimizers"`
	}{Label: "estimated", Optimizer: out})
	if err != nil {
		return nil
	}
	return b
}

// Pipeline returns the optimizers in their fixed run order:
//
//  1. volatile_prefix_mover      system prompt layout, before anything reads it
//  2. noise_normalizer           clean tool output first...
//  3. output_trimmer             ...then trim what is left...
//  4. duplicate_result_collapser ...then compare cleaned results for exact repeats
//  5. cache_breakpoints          last, so the size check sees the final prompt
//
// The order is part of the cache-safety contract: changing it changes bytes.
func Pipeline() []Optimizer {
	return []Optimizer{
		VolatilePrefixMover{},
		NoiseNormalizer{memo: newSpanMemo()},
		OutputTrimmer{},
		DuplicateCollapser{},
		CacheBreakpoints{},
	}
}

// PipelineNames is the fixed order of names, for documentation and status output.
var PipelineNames = []string{
	"volatile_prefix_mover",
	"noise_normalizer",
	config.OptimizerOutputTrimmer,
	"duplicate_result_collapser",
	"cache_breakpoints",
}
