package optimize

import (
	"context"

	"github.com/usetrove/trove/internal/noise"
)

// NoiseNormalizer is the noise_normalizer optimizer. In tool results it removes
// ANSI escape codes and intermediate progress or spinner frames, and collapses
// runs of identical consecutive lines into one line plus "(repeated N times)".
// The model sees the same information; see package noise for the exact rules.
type NoiseNormalizer struct {
	// memo skips work for strings seen on an earlier request; nil disables it.
	memo *spanMemo
}

// Name implements Optimizer.
func (NoiseNormalizer) Name() string { return "noise_normalizer" }

// Tier implements Optimizer.
func (NoiseNormalizer) Tier() Tier { return TierLossless }

// Applies implements Optimizer: any request with tool-result text.
func (NoiseNormalizer) Applies(r *Request, _ Session) bool {
	for _, m := range r.Messages {
		for _, b := range m.Blocks {
			if len(b.Texts) > 0 {
				return true
			}
		}
	}
	return false
}

// Transform implements Optimizer.
func (n NoiseNormalizer) Transform(ctx context.Context, r *Request) (*Request, Report) {
	if err := r.Ensure(); err != nil {
		return r, Report{Err: err.Error()}
	}
	var edits []Edit
	for _, m := range r.Messages {
		for _, b := range m.Blocks {
			for _, sp := range b.Texts {
				if ctx.Err() != nil {
					return r, Report{Err: "time budget exceeded"}
				}
				var key [32]byte
				memoable := sp.Len() >= memoMinSpan
				if memoable {
					key = memoKey(r.Raw[sp.S:sp.E])
					if repl, known := n.memo.get(key); known {
						if repl != nil {
							edits = append(edits, Edit{Span: sp, New: repl})
						}
						continue
					}
				}
				s, ok := r.DecodeString(sp)
				if !ok {
					continue
				}
				var repl []byte
				if clean := noise.Clean(s); clean != s {
					repl = EncodeString(clean)
					edits = append(edits, Edit{Span: sp, New: repl})
				}
				if memoable {
					n.memo.put(key, repl)
				}
			}
		}
	}
	out, err := r.Apply(edits)
	if err != nil {
		return r, Report{Err: err.Error()}
	}
	return out, Report{}
}
