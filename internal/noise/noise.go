// Package noise recognizes and removes terminal noise from tool output: ANSI
// escape codes, progress-bar and spinner frames, and long runs of identical
// lines. It is shared by the waste detector (which measures noise) and the
// noise_normalizer optimizer (which removes it) so both agree on what noise is.
//
// Clean is a pure function of its input: the same text always gives the same
// bytes, and cleaning already-clean text changes nothing.
package noise

import (
	"fmt"
	"regexp"
	"strings"
)

// ANSI matches CSI and OSC escape sequences.
var ANSI = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)

var progressRe = regexp.MustCompile(`\d{1,3}(?:\.\d+)?%.*[#=█▓░>]{4,}|[#=█▓░>]{8,}.*\d{1,3}(?:\.\d+)?%|\d+(?:\.\d+)?\s?[kKMG]i?B/s`)

// Braille holds the spinner glyphs used by most CLI spinners.
const Braille = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"

// IsProgress reports whether the line looks like one frame of a progress bar.
// It checks cheap byte conditions first so the regular expression only runs on
// lines that could match.
func IsProgress(l string) bool {
	return (strings.Contains(l, "%") || strings.Contains(l, "B/s")) && progressRe.MatchString(l)
}

// IsSpinnerFrame is the strict spinner test Clean uses: a short line holding a
// braille spinner glyph. The detector's looser test also matches lines that
// start with | / \ or -, which would catch bullet lists, ASCII tables and diffs
// that must never be dropped.
func IsSpinnerFrame(l string) bool {
	return len(l) <= 120 && strings.ContainsAny(l, Braille)
}

const (
	// minRun is the shortest run of identical lines that may be collapsed.
	minRun = 3
	// maxFrameLine bounds how long a progress or spinner line can be.
	maxFrameLine = 200
)

// repeatMarker matches the line Clean writes after a collapsed run. Such lines
// are never themselves collapsed, which keeps Clean idempotent.
var repeatMarker = regexp.MustCompile(`^\(repeated \d+ times\)\r?$`)

// Clean removes ANSI codes, intermediate progress and spinner frames, and
// collapses runs of identical consecutive lines into the line followed by
// "(repeated N times)", where N is how many times the line appeared in a row.
// A line that differs from its neighbors is never removed, apart from the
// progress and spinner frames described above.
func Clean(text string) string {
	if text == "" {
		return text
	}
	hasEsc := strings.IndexByte(text, 0x1b) >= 0
	hasCR := strings.IndexByte(text, '\r') >= 0
	lines := strings.Split(text, "\n")
	changed := false

	if hasEsc || hasCR {
		for i, l := range lines {
			orig := l
			if hasEsc && strings.IndexByte(l, 0x1b) >= 0 {
				l = ANSI.ReplaceAllString(l, "")
			}
			if hasCR {
				l = lastRedrawFrame(l)
			}
			if l != orig {
				lines[i] = l
				changed = true
			}
		}
	}

	// Progress and spinner frames: keep only the last of each consecutive group.
	frame := func(l string) bool {
		return len(l) <= maxFrameLine && (IsProgress(l) || IsSpinnerFrame(l))
	}
	var keep []string
	dropped := false
	for i := 0; i < len(lines); i++ {
		if frame(lines[i]) && i+1 < len(lines) && frame(lines[i+1]) {
			dropped = true
			continue
		}
		keep = append(keep, lines[i])
	}
	if dropped {
		lines, changed = keep, true
	}

	// Runs of identical lines.
	var out []string
	collapsed := false
	for i := 0; i < len(lines); {
		j := i + 1
		for j < len(lines) && lines[j] == lines[i] {
			j++
		}
		run := j - i
		if run >= minRun && !repeatMarker.MatchString(lines[i]) {
			marker := fmt.Sprintf("(repeated %d times)", run)
			if strings.HasSuffix(lines[i], "\r") {
				marker += "\r"
			}
			// Only collapse when it makes the text shorter.
			if (run-1)*(len(lines[i])+1) > len(marker)+1 {
				out = append(out, lines[i], marker)
				collapsed = true
				i = j
				continue
			}
		}
		out = append(out, lines[i:j]...)
		i = j
	}
	if collapsed {
		lines, changed = out, true
	}

	if !changed {
		return text
	}
	return strings.Join(lines, "\n")
}

// lastRedrawFrame resolves carriage-return redraws the way a terminal would:
// text after the last interior "\r" wins. A trailing "\r" (the CR of a CRLF line
// ending) is kept as is.
func lastRedrawFrame(l string) string {
	body := strings.TrimRight(l, "\r")
	if strings.IndexByte(body, '\r') < 0 {
		return l
	}
	tail := l[len(body):]
	return body[strings.LastIndexByte(body, '\r')+1:] + tail
}

// Measure returns how many bytes Clean would remove from text.
func Measure(text string) int {
	if d := len(text) - len(Clean(text)); d > 0 {
		return d
	}
	return 0
}
