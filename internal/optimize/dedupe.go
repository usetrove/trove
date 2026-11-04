package optimize

import (
	"context"
	"crypto/sha256"
	"strconv"
)

// minDuplicateBytes is the smallest tool result worth collapsing. It matches the
// duplicate_tool_result detector, and keeps the note smaller than what it
// replaces.
const minDuplicateBytes = 200

// DuplicateCollapser is the duplicate_result_collapser optimizer. When a tool
// result is byte-identical to an earlier tool result in the same conversation,
// every later copy is replaced by a short note pointing at the first one.
//
// It is deterministic given history order: the first occurrence is never
// changed, and a later copy always points at the first occurrence, so a growing
// history rewrites each old block the same way on every request.
//
// Only exact matches fire: the content must be the same bytes, on a successful
// result that holds only text. Error results are never collapsed, because the
// model may need to see that an error repeated.
type DuplicateCollapser struct{}

// Name implements Optimizer.
func (DuplicateCollapser) Name() string { return "duplicate_result_collapser" }

// Tier implements Optimizer.
func (DuplicateCollapser) Tier() Tier { return TierLossless }

func duplicateCandidate(b Block) bool {
	return b.Type == "tool_result" && b.TextOnly && !b.IsError && b.Content.Len() >= minDuplicateBytes
}

// Applies implements Optimizer: at least two candidate tool results.
func (DuplicateCollapser) Applies(r *Request, _ Session) bool {
	n := 0
	for _, m := range r.Messages {
		for _, b := range m.Blocks {
			if duplicateCandidate(b) {
				if n++; n >= 2 {
					return true
				}
			}
		}
	}
	return false
}

type firstSeen struct {
	msg int // 1-based message number
	id  string
}

// Transform implements Optimizer.
func (DuplicateCollapser) Transform(ctx context.Context, r *Request) (*Request, Report) {
	if err := r.Ensure(); err != nil {
		return r, Report{Err: err.Error()}
	}
	seen := map[[sha256.Size]byte]firstSeen{}
	var edits []Edit
	for mi, m := range r.Messages {
		for _, b := range m.Blocks {
			if !duplicateCandidate(b) {
				continue
			}
			if ctx.Err() != nil {
				return r, Report{Err: "time budget exceeded"}
			}
			sum := sha256.Sum256(r.Raw[b.Content.S:b.Content.E])
			first, dup := seen[sum]
			if !dup {
				seen[sum] = firstSeen{msg: mi + 1, id: b.ToolUseID}
				continue
			}
			note := EncodeString(duplicateNote(first))
			if len(note)+32 > b.Content.Len() {
				continue
			}
			edits = append(edits, Edit{Span: b.Content, New: note})
		}
	}
	out, err := r.Apply(edits)
	if err != nil {
		return r, Report{Err: err.Error()}
	}
	return out, Report{}
}

// CollapsedPrefix starts the note that replaces a collapsed duplicate result.
// internal/measure uses it to recognise results an optimizer removed.
const CollapsedPrefix = "[Trove: this tool result is identical to the earlier result in "

func duplicateNote(f firstSeen) string {
	ref := "message " + strconv.Itoa(f.msg)
	if f.id != "" {
		ref += ", tool call " + f.id
	}
	return CollapsedPrefix + ref + "; it is not repeated here.]"
}

// Points implements Capable: request-level only.
func (DuplicateCollapser) Points() []Point { return []Point{PointRequest} }

// OverheadTokens implements Capable: nothing is injected on every request.
func (DuplicateCollapser) OverheadTokens() int { return 0 }
