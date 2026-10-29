package optimize

import (
	"context"
	"regexp"
	"strings"
)

// volatileRe matches lines whose content usually changes between sessions or
// between requests: ISO dates and timestamps, UUIDs, Unix epoch values, and
// "today's date" style phrases. The classifier is stateless on purpose: the
// optimizer must be a pure function of one request, so it cannot compare
// requests the way the cache-miss analysis does.
var volatileRe = regexp.MustCompile(`(?i)\b\d{4}-\d{2}-\d{2}\b|\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b|\b1[5-9]\d{8}(?:\d{3})?\b|\b(?:today'?s date|current (?:date|time)|date today)\b`)

// maxVolatileLine keeps long instruction paragraphs that merely mention a date
// from being treated as a volatile line.
const maxVolatileLine = 500

func isVolatileLine(l string) bool {
	return len(l) <= maxVolatileLine && volatileRe.MatchString(l)
}

// splitVolatile partitions text into its stable lines and its volatile lines,
// each in original order. Every line lands in exactly one of the two.
func splitVolatile(text string) (stable, volatile string, found bool) {
	lines := strings.Split(text, "\n")
	var st, vo []string
	for _, l := range lines {
		if isVolatileLine(l) {
			vo = append(vo, l)
		} else {
			st = append(st, l)
		}
	}
	if len(vo) == 0 {
		return text, "", false
	}
	return strings.Join(st, "\n"), strings.Join(vo, "\n"), true
}

func hasVolatileLine(text string) bool {
	for _, l := range strings.Split(text, "\n") {
		if isVolatileLine(l) {
			return true
		}
	}
	return false
}

// VolatilePrefixMover is the volatile_prefix_mover optimizer. It moves lines
// that change often (dates, timestamps, ids) from the system prompt into a
// final system block, so the stable text before them can be cached.
//
// Information is preserved and only its position changes. It only runs for
// agents the user explicitly marked as custom; it never touches agents that
// build their own system prompt (Claude Code, Codex).
//
// A string system prompt becomes two text blocks, stable then volatile. In an
// array system prompt, volatile lines are taken out of blocks that also hold
// stable text and appended as one new final block; a block made only of
// volatile lines is left where it is, because moving blocks would change which
// block carries the agent's own cache markers.
type VolatilePrefixMover struct{}

// Name implements Optimizer.
func (VolatilePrefixMover) Name() string { return "volatile_prefix_mover" }

// Tier implements Optimizer. Nothing is removed, so it is lossless; the
// position of some text changes.
func (VolatilePrefixMover) Tier() Tier { return TierLossless }

// Applies implements Optimizer: custom agents with a system prompt only.
func (VolatilePrefixMover) Applies(r *Request, s Session) bool {
	return s.Custom && r.System.E != 0
}

func textBlockJSON(text string) []byte {
	return append(append([]byte(`{"type":"text","text":`), EncodeString(text)...), '}')
}

// Transform implements Optimizer.
func (VolatilePrefixMover) Transform(ctx context.Context, r *Request) (*Request, Report) {
	var edits []Edit
	switch {
	case r.Raw[r.System.S] == '"':
		text, ok := r.DecodeString(r.System)
		if !ok {
			return r, Report{}
		}
		stable, volatile, found := splitVolatile(text)
		if !found || strings.TrimSpace(stable) == "" {
			return r, Report{}
		}
		blocks := append([]byte("["), textBlockJSON(stable)...)
		blocks = append(append(blocks, ','), textBlockJSON(volatile)...)
		edits = append(edits, Edit{Span: r.System, New: append(blocks, ']')})
	case len(r.SystemBlocks) > 0:
		var moved []string
		for _, b := range r.SystemBlocks {
			if ctx.Err() != nil {
				return r, Report{Err: "time budget exceeded"}
			}
			if b.Type != "text" || b.Text.E == 0 {
				continue
			}
			text, ok := r.DecodeString(b.Text)
			if !ok {
				continue
			}
			stable, volatile, found := splitVolatile(text)
			if !found || strings.TrimSpace(stable) == "" {
				continue // nothing volatile, or a block made only of volatile lines
			}
			edits = append(edits, Edit{Span: b.Text, New: EncodeString(stable)})
			moved = append(moved, volatile)
		}
		if len(moved) > 0 {
			end := r.System.E - 1 // the closing ']'
			edits = append(edits, Edit{Span: Span{end, end}, New: append([]byte(","), textBlockJSON(strings.Join(moved, "\n"))...)})
		}
	}
	out, err := r.Apply(edits)
	if err != nil {
		return r, Report{Err: err.Error()}
	}
	return out, Report{}
}
