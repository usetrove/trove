package optimize

import (
	"context"
	"strings"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/technique"
)

// OptimizerVerbositySteer is the name of the verbosity steering optimizer.
const OptimizerVerbositySteer = "verbosity_steer"

// Verbosity levels (config.VerbosityLevels). The level is pinned with the session,
// so the note a session gets never changes while it runs.
const (
	VerbosityConcise = config.VerbosityConcise
	VerbosityTerse   = config.VerbosityTerse
)

// verbosityNotes are the fixed notes, one per level. They are constants on purpose:
// the same note on every request of a session keeps each turn's prefix byte-identical.
var verbosityNotes = map[string]string{
	VerbosityConcise: "Keep replies short: lead with the answer, and skip preambles, recaps of the task or diff, and closing offers of more help. Keep code, commands, paths, numbers and error text exact.",
	VerbosityTerse: "Reply tersely: answer first in as few words as stay clear, with no preamble, recap or offer of more help; fragments are fine. " +
		"Keep code, commands, paths, numbers and error text exact, never drop a negation, and use full sentences for warnings and for questions to the user.",
}

// VerbosityNote returns the note for a level; an unknown or empty level is concise.
func VerbosityNote(level string) string {
	if n, ok := verbosityNotes[level]; ok {
		return n
	}
	return verbosityNotes[VerbosityConcise]
}

// VerbositySteer is the verbosity_steer optimizer. It appends one short, fixed note
// asking for shorter replies at the very end of the system prompt: after the last
// system block of an array, or after the text of a string system prompt. Every byte
// before it is left as it was, so the agent's own cache markers keep covering the same
// text, and because the note is constant per session, each turn's request is the
// previous one's prefix plus new messages (Anthropic builds the cache prefix from
// tools, then system, then messages; verified 2026-11-17 on
// platform.claude.com/docs/en/build-with-claude/prompt-caching).
//
// It changes what the model is asked to do, so it is T2: it adds its note's tokens to
// every request and is judged on output tokens, cost per turn, test failures and
// turns, never assumed to save anything.
type VerbositySteer struct{}

// Name implements Optimizer.
func (VerbositySteer) Name() string { return OptimizerVerbositySteer }

// Tier implements Optimizer. Nothing is removed; the model is asked to write less.
func (VerbositySteer) Tier() Tier { return TierBehavior }

// Applies implements Optimizer: every Anthropic Messages request.
func (VerbositySteer) Applies(*Request, Session) bool { return true }

// Transform implements Optimizer with the default level.
func (v VerbositySteer) Transform(ctx context.Context, r *Request) (*Request, Report) {
	return v.TransformSession(ctx, r, Session{})
}

// TransformSession implements SessionTransformer: the note is chosen by the session's
// pinned level.
func (VerbositySteer) TransformSession(_ context.Context, r *Request, s Session) (*Request, Report) {
	if err := r.Ensure(); err != nil {
		return r, Report{Err: err.Error()}
	}
	note := VerbosityNote(s.Verbosity)
	// Charged for what it adds, braces and all; a note already there still costs its text.
	rep := Report{OverheadTokens: estimateTokens(len(note), s)}
	var edit Edit
	switch {
	case r.System.E == 0:
		// No system prompt: add one as the first field, so the rest of the body keeps its bytes.
		at := r.Root.S + 1
		field := append(append([]byte(`"system":`), EncodeString(note)...), ',')
		if r.RootEmpty {
			field = field[:len(field)-1]
		}
		edit = Edit{Span: Span{at, at}, New: field}
	case r.Raw[r.System.S] == '"':
		text, ok := r.DecodeString(r.System)
		if !ok {
			return r, Report{}
		}
		if strings.HasSuffix(text, note) {
			return r, rep // already there: never add it twice
		}
		end := r.System.E - 1 // the closing quote
		edit = Edit{Span: Span{end, end}, New: jsonStringBody("\n\n" + note)}
	case r.Raw[r.System.S] == '[':
		if n := len(r.SystemBlocks); n > 0 {
			last := r.SystemBlocks[n-1]
			if last.Text.E != 0 {
				if t, ok := r.DecodeString(last.Text); ok && t == note {
					return r, rep
				}
			}
		}
		end := r.System.E - 1 // the closing ']'
		blk := textBlockJSON(note)
		if len(r.SystemBlocks) > 0 {
			blk = append([]byte(","), blk...)
		}
		edit = Edit{Span: Span{end, end}, New: blk}
	default:
		return r, Report{} // a system value the API does not accept; leave it to the provider
	}
	out, err := r.Apply([]Edit{edit})
	if err != nil {
		return r, Report{Err: err.Error()}
	}
	rep.OverheadTokens = estimateTokens(len(edit.New), s)
	return out, rep
}

// jsonStringBody is s encoded as JSON without the surrounding quotes, for inserting
// inside an existing JSON string.
func jsonStringBody(s string) []byte {
	b := EncodeString(s)
	return b[1 : len(b)-1]
}

// estimateTokens is n bytes in estimated tokens at the session's ratio.
func estimateTokens(n int, sess Session) int {
	return int(float64(n)/sess.bytesPerToken() + 0.5)
}

// Points implements Capable: request-level only. Hooks cannot see the model request.
func (VerbositySteer) Points() []Point { return []Point{PointRequest} }

// OverheadTokens implements Capable: the largest insertion (the longest note as a new
// system block), at the default ratio. Each request's report carries the exact figure.
func (VerbositySteer) OverheadTokens() int {
	n := 0
	for _, note := range verbosityNotes {
		n = max(n, estimateTokens(len(textBlockJSON(note))+1, Session{}))
	}
	return n
}

// Caveat implements Caveater.
func (VerbositySteer) Caveat() string {
	return "Anthropic Messages requests only; it asks for shorter replies, so it is judged by session A/B, not assumed to save"
}

// claimsBehaviorRules reports whether the named technique asks the model for shorter
// replies (it holds the behavior_rules slot), so it cannot run beside verbosity_steer.
func claimsBehaviorRules(name string) bool {
	t, ok := technique.Get(name)
	if !ok {
		return false
	}
	for _, c := range t.Claims {
		if c.Resource == technique.ResourceBehaviorRules {
			return true
		}
	}
	return false
}
