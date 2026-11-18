package optimize

import "context"

// OptimizerEffortToolTurns is the name of the tool-turn effort optimizer (plan item 7.4).
const OptimizerEffortToolTurns = "effort_tool_turns"

// ToolTurnEffort is the effort level effort_tool_turns asks for on a tool turn. It is a
// constant, so the same history always produces the same bytes (CLAUDE.md rule 5).
const ToolTurnEffort = "low"

// effortRank orders the documented output_config.effort levels (low, medium, high,
// xhigh, max; platform.claude.com/docs/en/build-with-claude/effort, verified 2026-11-17).
var effortRank = map[string]int{"low": 1, "medium": 2, "high": 3, "xhigh": 4, "max": 5}

// EffortCacheKeeps lists the models on which changing output_config.effort between
// requests was measured to keep the cached prefix: `trove optimize effort-check` gave
// "keeps" on 10 changed and 10 control turn pairs per model (0% lost against 0%), from
// live API-key sessions on 2026-11-18 (docs/effort-check.md). The provider only says a
// change "can invalidate" cached blocks, so every other model is left alone until it is
// measured the same way.
var EffortCacheKeeps = map[string]bool{
	"claude-sonnet-5-5": true,
	"claude-haiku-5-5":  true,
}

// EffortToolTurns lowers output_config.effort to ToolTurnEffort on tool turns: requests
// whose newest user message only returns tool results, where the model mostly decides
// the next tool call rather than writing an answer. It only ever lowers a level the
// request already set, never adds the field, and never touches a user turn, so the
// agent's own effort holds wherever a person is waiting for a reply.
//
// It changes how hard the same model thinks (optimizer tier behavior; technique class
// effort, so T3 in the plan): judged by session
// A/B on cost, test failures and turns, never assumed to save anything.
type EffortToolTurns struct{}

// Name implements Optimizer.
func (EffortToolTurns) Name() string { return OptimizerEffortToolTurns }

// Tier implements Optimizer.
func (EffortToolTurns) Tier() Tier { return TierBehavior }

// Applies implements Optimizer: a measured model, an explicit effort above the target,
// and a tool turn.
func (EffortToolTurns) Applies(r *Request, _ Session) bool {
	return r.EffortSpan.Len() > 0 && EffortCacheKeeps[r.Model] &&
		effortRank[r.Effort] > effortRank[ToolTurnEffort] && lastTurnIsTool(r)
}

// Transform implements Optimizer.
func (e EffortToolTurns) Transform(ctx context.Context, r *Request) (*Request, Report) {
	return e.TransformSession(ctx, r, Session{})
}

// TransformSession implements SessionTransformer. The session adds nothing: the target
// is a constant.
func (e EffortToolTurns) TransformSession(_ context.Context, r *Request, s Session) (*Request, Report) {
	if err := r.Ensure(); err != nil {
		return r, Report{Err: err.Error()}
	}
	if !e.Applies(r, s) {
		return r, Report{}
	}
	out, err := r.Apply([]Edit{{Span: r.EffortSpan, New: EncodeString(ToolTurnEffort)}})
	if err != nil {
		return r, Report{Err: err.Error()}
	}
	return out, Report{}
}

// lastTurnIsTool reports whether the newest user message only returns tool results.
func lastTurnIsTool(r *Request) bool {
	turns := userTurns(r)
	return len(turns) > 0 && turns[len(turns)-1]
}

// Points implements Capable: it rewrites a request field, which only the gateway sees.
func (EffortToolTurns) Points() []Point { return []Point{PointRequest} }

// OverheadTokens implements Capable: it adds no text.
func (EffortToolTurns) OverheadTokens() int { return 0 }

// Caveat implements Caveater.
func (EffortToolTurns) Caveat() string {
	return "API-key gateway traffic only, on models whose effort cache check said keeps; lowers an effort the agent set, on tool turns only; judged by session A/B"
}
