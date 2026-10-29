package optimize

import (
	"context"
	"strings"
)

// maxVolatileLine keeps long instruction paragraphs that merely mention a date
// from being treated as a volatile line.
const maxVolatileLine = 500

// isVolatileLine reports whether a line's content usually changes between
// sessions or between requests: an ISO date or timestamp (2026-10-29), a UUID, a
// Unix epoch value (10 or 13 digits), or a phrase such as "today's date". The
// classifier is stateless on purpose: the optimizer must be a pure function of
// one request, so it cannot compare requests the way the cache-miss analysis
// does. It is hand-written rather than a regular expression because it runs over
// every line of the system prompt on every request.
func isVolatileLine(l string) bool {
	if len(l) > maxVolatileLine {
		return false
	}
	return hasDigitPattern(l) || hasVolatilePhrase(l)
}

func isWordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isHex(c byte) bool {
	return isDigit(c) || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func allMatch(s string, ok func(byte) bool) bool {
	for i := 0; i < len(s); i++ {
		if !ok(s[i]) {
			return false
		}
	}
	return true
}

// bounded reports whether s[i:j] is not part of a longer word.
func bounded(s string, i, j int) bool {
	return (i == 0 || !isWordByte(s[i-1])) && (j == len(s) || !isWordByte(s[j]))
}

// hasDigitPattern looks for an ISO date, a UUID or an epoch number.
func hasDigitPattern(l string) bool {
	for i := 0; i < len(l); i++ {
		// Epoch seconds or milliseconds: 1[5-9] followed by 8 or 11 more digits.
		if l[i] == '1' && i+1 < len(l) && l[i+1] >= '5' && l[i+1] <= '9' && (i == 0 || !isWordByte(l[i-1])) {
			j := i
			for j < len(l) && isDigit(l[j]) {
				j++
			}
			if (j-i == 10 || j-i == 13) && bounded(l, i, j) {
				return true
			}
		}
	}
	for i := 0; i < len(l); i++ {
		if l[i] != '-' {
			continue
		}
		// ISO date: dddd-dd-dd
		if i >= 4 && i+6 <= len(l) && allMatch(l[i-4:i], isDigit) && allMatch(l[i+1:i+3], isDigit) &&
			l[i+3] == '-' && allMatch(l[i+4:i+6], isDigit) && (i == 4 || !isWordByte(l[i-5])) && isoEnd(l, i+6) {
			return true
		}
		// UUID: 8-4-4-4-12 hex digits; i is the first hyphen.
		if i >= 8 && i+28 <= len(l) && l[i+5] == '-' && l[i+10] == '-' && l[i+15] == '-' &&
			allMatch(l[i-8:i], isHex) && allMatch(l[i+1:i+5], isHex) && allMatch(l[i+6:i+10], isHex) &&
			allMatch(l[i+11:i+15], isHex) && allMatch(l[i+16:i+28], isHex) && bounded(l, i-8, i+28) {
			return true
		}
	}
	return false
}

// isoEnd reports whether an ISO date ending at j is followed by a non-word
// character, the end of the line, or the 'T' of a timestamp.
func isoEnd(l string, j int) bool {
	return j == len(l) || !isWordByte(l[j]) || (l[j] == 'T' && j+1 < len(l) && isDigit(l[j+1]))
}

var volatilePhrases = []string{"today's date", "todays date", "current date", "current time", "date today"}

func hasVolatilePhrase(l string) bool {
	// Every phrase contains "date" or "time", so most lines are rejected by a
	// case-insensitive look for the letters that start those words.
	if strings.IndexByte(l, 'd') < 0 && strings.IndexByte(l, 'D') < 0 && strings.IndexByte(l, 't') < 0 && strings.IndexByte(l, 'T') < 0 {
		return false
	}
	lower := strings.ToLower(l)
	for _, p := range volatilePhrases {
		if i := strings.Index(lower, p); i >= 0 && bounded(lower, i, i+len(p)) {
			return true
		}
	}
	return false
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
	if err := r.Ensure(); err != nil {
		return r, Report{Err: err.Error()}
	}
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
