package optimize

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/pricing"
)

// OptimizerModelRouter is the name of the per-request model step-down (plan item 7.2).
const OptimizerModelRouter = "model_router"

// routeCacheTTL is the cache lifetime model_router prices a switch at. Agents mark
// their cache with the provider's default, 5 minutes.
const routeCacheTTL = "5m"

// ModelRouter steps a session down to a cheaper model (Session.RouteTo) on tool
// turns, and keeps it there for at least StickyTurns turns, because every switch
// writes the whole conversation into the new model's cache.
//
// Verified 2026-11-17 on platform.claude.com/docs/en/build-with-claude/prompt-caching:
// a 5-minute cache write is 1.25 times the base input price and a cache read is 0.1
// times (0.05 on some models, so prices come from the pricing table, never from these
// constants). Whether a different model can read another model's cache is not stated
// there; model_router assumes it cannot, which only makes it more cautious.
//
// The decision is a pure function of the request's message history and the session's
// pin (RouteTo and the prices at PinnedAt), so the same history always goes to the same
// model (PRINCIPLES.md rule 5). It is T3: off by default, and judged only by session A/B
// under the strictest guardrails.
type ModelRouter struct {
	// Prices overrides the pricing table; tests use it. Nil means pricing.Default().
	Prices *pricing.Table
}

// Name implements Optimizer.
func (ModelRouter) Name() string { return OptimizerModelRouter }

// Tier implements Optimizer.
func (ModelRouter) Tier() Tier { return TierRouting }

// Applies implements Optimizer: a target is pinned and the request names another model.
func (ModelRouter) Applies(r *Request, s Session) bool {
	return s.RouteTo != "" && r.Model != "" && r.Model != s.RouteTo
}

// Transform implements Optimizer. Without a session there is no target, so nothing changes.
func (m ModelRouter) Transform(ctx context.Context, r *Request) (*Request, Report) {
	return m.TransformSession(ctx, r, Session{})
}

// RouteEstimate is what model_router reports for one request. Money is estimated:
// prompt tokens come from the request's size, not from the provider.
type RouteEstimate struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Model string `json:"model"` // the model this request goes to
	// Ratio is To's input price divided by From's.
	Ratio float64 `json:"price_ratio"`
	// StickyTurns is k, the fewest turns the cheaper model must stay on to repay the
	// cache write a switch costs.
	StickyTurns int `json:"sticky_turns"`
	// Switch is true when this request goes to a different model than the previous
	// turn did, so its whole prompt is written into that model's cache.
	Switch bool `json:"switch"`
	// PromptTokens is the estimated prompt size (request bytes / bytes per token).
	PromptTokens int64 `json:"prompt_tokens_estimated"`
	// CacheWriteUSD is the predicted cost of writing the prompt into the new model's
	// cache on a switch turn; zero on other turns.
	CacheWriteUSD float64 `json:"cache_write_usd_estimated"`
	Label         string  `json:"label"`
}

// TransformSession implements SessionTransformer.
func (m ModelRouter) TransformSession(_ context.Context, r *Request, s Session) (*Request, Report) {
	if err := r.Ensure(); err != nil {
		return r, Report{Err: err.Error()}
	}
	if s.RouteTo == "" || r.ModelSpan.Len() == 0 || r.Model == s.RouteTo {
		return r, Report{}
	}
	p, ok := m.policy(r.Model, s)
	if !ok {
		// Unpriced or not cheaper: never route on a number Trove cannot stand behind.
		return r, Report{}
	}
	turns := userTurns(r)
	down := StepDown(turns, p.k)
	n := len(down)
	if n == 0 {
		return r, Report{}
	}
	est := &RouteEstimate{From: r.Model, To: s.RouteTo, Model: r.Model, Ratio: p.ratio, StickyTurns: p.k, Label: "estimated"}
	est.PromptTokens = int64(float64(len(r.Raw))/s.bytesPerToken() + 0.5)
	if down[n-1] {
		est.Model = s.RouteTo
	}
	prev := false
	if n > 1 {
		prev = down[n-2]
	}
	if est.Switch = n > 1 && prev != down[n-1]; est.Switch {
		write := p.fromWrite
		if down[n-1] {
			write = p.toWrite
		}
		est.CacheWriteUSD = float64(est.PromptTokens) * write / 1e6
	}
	rep := Report{Route: est}
	if !down[n-1] {
		return r, rep
	}
	out, err := r.Apply([]Edit{{Span: r.ModelSpan, New: EncodeString(s.RouteTo)}})
	if err != nil {
		return r, Report{Err: err.Error()}
	}
	return out, rep
}

// StickyTurns is k ≥ (W−R_to)/(R_from−R_to): the turns a cheaper model must stay on
// before switching back, so the reads it saves (R_from−R_to per prompt token per turn)
// repay writing the prompt into its cache instead of reading it (W−R_to, once). With
// the documented multipliers (W = 1.25, R = 0.1 of input) this is 11.5·r/(1−r): about 6
// at one third of the price and about 17 at 60%. Output and new-input savings are left
// out, which can only make k larger. ok is false when the cheaper model saves nothing.
func StickyTurns(toWrite, toRead, fromRead float64) (int, bool) {
	gain := fromRead - toRead
	if gain <= 0 || toWrite <= toRead {
		return 0, false
	}
	return int(math.Ceil((toWrite-toRead)/gain - 1e-9)), true
}

// StepDown returns, for each user turn, whether that turn goes to the cheaper model.
// A tool turn (one that only returns tool results) may step down; once down, the
// session stays down for at least k turns and then returns on the first turn that is
// not a tool turn. It reads only the history, so it is deterministic.
func StepDown(toolTurns []bool, k int) []bool {
	out := make([]bool, len(toolTurns))
	down, held := false, 0
	for i, tool := range toolTurns {
		switch {
		case !down && tool:
			down, held = true, 1
		case down && held >= k && !tool:
			down, held = false, 0
		case down:
			held++
		}
		out[i] = down
	}
	return out
}

// userTurns lists the request's user messages in order, true for a tool turn.
func userTurns(r *Request) []bool {
	var out []bool
	for _, msg := range r.Messages {
		if msg.Role != "user" {
			continue
		}
		tool := len(msg.Blocks) > 0
		for _, b := range msg.Blocks {
			if b.Type != "tool_result" {
				tool = false
				break
			}
		}
		out = append(out, tool)
	}
	return out
}

type routePolicy struct {
	ratio              float64
	k                  int
	fromWrite, toWrite float64 // USD per million tokens, 5-minute cache write
}

func (m ModelRouter) policy(from string, s Session) (routePolicy, bool) {
	t := m.Prices
	if t == nil {
		var err error
		if t, err = pricing.Default(); err != nil {
			return routePolicy{}, false
		}
	}
	at := s.PinnedAt
	if at.IsZero() {
		at = s.Now
	}
	if at.IsZero() {
		at = time.Now()
	}
	fe, _ := t.Lookup(pricing.Anthropic, from, at)
	te, _ := t.Lookup(pricing.Anthropic, s.RouteTo, at)
	if fe == nil || te == nil || fe.Model == te.Model {
		return routePolicy{}, false
	}
	fIn, fR, fW, ok1 := rates(fe.Standard)
	tIn, tR, tW, ok2 := rates(te.Standard)
	if !ok1 || !ok2 || fIn <= 0 || tIn >= fIn {
		return routePolicy{}, false
	}
	k, ok := StickyTurns(tW, tR, fR)
	if !ok {
		return routePolicy{}, false
	}
	return routePolicy{ratio: tIn / fIn, k: k, fromWrite: fW, toWrite: tW}, true
}

func rates(t pricing.Tier) (in, read, write float64, ok bool) {
	w, hasW := t.CacheWrite[routeCacheTTL]
	if !t.Input.IsSet() || t.CacheRead == nil || !hasW {
		return 0, 0, 0, false
	}
	in, e1 := strconv.ParseFloat(t.Input.String(), 64)
	read, e2 := strconv.ParseFloat(t.CacheRead.String(), 64)
	write, e3 := strconv.ParseFloat(strings.TrimSpace(w.String()), 64)
	return in, read, write, e1 == nil && e2 == nil && e3 == nil
}

// Points implements Capable: it rewrites the request's model field, which only the
// gateway sees.
func (ModelRouter) Points() []Point { return []Point{PointRequest} }

// OverheadTokens implements Capable: it adds no text. Its cost is the cache write on
// each switch, which every request's report estimates in money.
func (ModelRouter) OverheadTokens() int { return 0 }

// Caveat implements Caveater.
func (ModelRouter) Caveat() string {
	return "API-key gateway traffic only, and only when optimize.route_to names a cheaper priced model; judged by session A/B"
}
