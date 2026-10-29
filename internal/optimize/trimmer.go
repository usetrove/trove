package optimize

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// Thresholds for the output trimmer. Output smaller than this is not worth
// trimming, and the trimmer only acts on text that is recognizably test or
// build output, never on a file the agent read.
const (
	minTrimBytes     = 6000
	minTrimLines     = 60
	minSignature     = 10 // lines that look like test/build output
	minTrimmedLines  = 20 // trimming fewer lines than this is not worth a marker
	keepHead         = 5
	keepTail         = 10
	contextBefore    = 2
	contextAfter     = 2
	maxStackLines    = 40
	maxTrimLineBytes = 400
)

var (
	// signatureRe marks lines typical of test runners and build tools.
	signatureRe = regexp.MustCompile(`^\s*(?:=== (?:RUN|PAUSE|CONT)|--- (?:PASS|FAIL|SKIP)|ok\s+\S+\s|PASS\b|FAIL\b|collected \d+ items|running \d+ tests?|test \S+ \.\.\. |Test Suites:|Tests:|[✓✔✗✘×]|\S+\.py [.FEsx]+|BUILD |\[INFO\]|\[ERROR\]|npm ERR!|Compiling |error\[E\d+\])`)
	// failureRe marks lines that carry a failure or the start of a stack trace.
	failureRe = regexp.MustCompile(`^\s*(?:--- FAIL|FAIL\b|FAILED\b|ERROR\b|Error:|error:|error\[|panic:|fatal:|Traceback \(most recent call last\)|AssertionError|[✗✘×●] |npm ERR!|\[ERROR\]|E {3}|thread '.*' panicked)|\b(?:FAILED|panicked at)\b|\w*Exception\b`)
	// frameRe marks stack-frame lines.
	frameRe = regexp.MustCompile(`^\s+at .+:\d+|\.go:\d+|File ".+", line \d+|^\s+\S+\.(?:rs|java|kt|py|js|ts):\d+`)
	// trimMarker is the line the trimmer writes; it is always kept.
	trimMarker = regexp.MustCompile(`^\[Trove: \d+ lines trimmed; full output sha256:[0-9a-f]{16}\]\r?$`)
)

// OutputTrimmer is the output_trimmer optimizer. In large test and build output
// it keeps the failures, stack traces, a few lines of context, and the first and
// last lines (where runners print their summary), and replaces everything else
// with one marker line naming how many lines were trimmed and a hash of the
// full output.
//
// It is recoverable only if the agent can fetch the full output back. The
// engine stores each original in the recovery store before the shortened request
// is forwarded (Report.Originals), and the agent gets it back with the MCP tool
// trove_fetch_full. Without the store, or without the MCP server registered, the
// engine clamps "on" to "shadow", which only estimates and changes nothing.
type OutputTrimmer struct{}

// Name implements Optimizer.
func (OutputTrimmer) Name() string { return "output_trimmer" }

// Tier implements Optimizer.
func (OutputTrimmer) Tier() Tier { return TierRecoverable }

// Applies implements Optimizer: any large tool result.
func (OutputTrimmer) Applies(r *Request, _ Session) bool {
	for _, m := range r.Messages {
		for _, b := range m.Blocks {
			for _, sp := range b.Texts {
				if sp.Len() >= minTrimBytes {
					return true
				}
			}
		}
	}
	return false
}

// Transform implements Optimizer.
func (OutputTrimmer) Transform(ctx context.Context, r *Request) (*Request, Report) {
	if err := r.Ensure(); err != nil {
		return r, Report{Err: err.Error()}
	}
	var edits []Edit
	var originals []string
	for _, m := range r.Messages {
		for _, b := range m.Blocks {
			for _, sp := range b.Texts {
				if sp.Len() < minTrimBytes {
					continue
				}
				if ctx.Err() != nil {
					return r, Report{Err: "time budget exceeded"}
				}
				s, ok := r.DecodeString(sp)
				if !ok {
					continue
				}
				if trimmed, changed := trimOutput(s); changed {
					edits = append(edits, Edit{Span: sp, New: EncodeString(trimmed)})
					originals = append(originals, s)
				}
			}
		}
	}
	out, err := r.Apply(edits)
	if err != nil {
		return r, Report{Err: err.Error()}
	}
	return out, Report{Originals: originals}
}

// trimOutput is a pure function of text. Which lines survive depends only on
// the line itself and its neighbors, and every surviving line stays put, so
// trimming already-trimmed output removes nothing.
func trimOutput(text string) (string, bool) {
	if len(text) < minTrimBytes {
		return text, false
	}
	lines := strings.Split(text, "\n")
	if len(lines) < minTrimLines {
		return text, false
	}
	signature := 0
	for _, l := range lines {
		if len(l) <= maxTrimLineBytes && signatureRe.MatchString(l) {
			signature++
		}
	}
	if signature < minSignature {
		return text, false
	}

	return collapseRuns(text, lines, keepLines(lines), minTrimmedLines)
}

// keepLines marks the lines worth keeping: the head and tail, every failure or
// stack-frame line with its context, and any existing trim marker.
func keepLines(lines []string) []bool {
	keep := make([]bool, len(lines))
	for i := 0; i < len(lines) && i < keepHead; i++ {
		keep[i] = true
	}
	for i := max(0, len(lines)-keepTail); i < len(lines); i++ {
		keep[i] = true
	}
	for i, l := range lines {
		if len(l) > maxTrimLineBytes {
			continue
		}
		if trimMarker.MatchString(l) {
			keep[i] = true
			continue
		}
		if !failureRe.MatchString(l) && !frameRe.MatchString(l) {
			continue
		}
		for j := max(0, i-contextBefore); j <= min(len(lines)-1, i+contextAfter); j++ {
			keep[j] = true
		}
		// A trace continues through indented lines and frames.
		for j, n := i+1, 0; j < len(lines) && n < maxStackLines; j, n = j+1, n+1 {
			c := lines[j]
			if !(strings.HasPrefix(c, " ") || strings.HasPrefix(c, "\t") || frameRe.MatchString(c)) {
				break
			}
			keep[j] = true
		}
	}

	return keep
}

// collapseRuns replaces each run of unkept lines with one marker line. It reports
// false, returning text unchanged, when fewer than minTrimmed lines go.
func collapseRuns(text string, lines []string, keep []bool, minTrimmed int) (string, bool) {
	var out []string
	hash := sha256.Sum256([]byte(text))
	short := hex.EncodeToString(hash[:8])
	trimmed := 0
	for i := 0; i < len(lines); {
		if keep[i] {
			out = append(out, lines[i])
			i++
			continue
		}
		j := i
		for j < len(lines) && !keep[j] {
			j++
		}
		out = append(out, fmt.Sprintf("[Trove: %d lines trimmed; full output sha256:%s]", j-i, short))
		trimmed += j - i
		i = j
	}
	if trimmed < minTrimmed {
		return text, false
	}
	return strings.Join(out, "\n"), true
}

// Condense shortens command output for the MCP trove_run tool with the same
// rules the trimmer uses: failures, stack traces, their context and the first
// and last lines survive, and the rest becomes marker lines naming the full
// output's hash. Unlike the trimmer it does not need the text to look like a
// test run, because the caller already knows it is command output. It reports
// false when the output is too short to be worth shortening.
func Condense(text string) (string, bool) {
	if len(text) < 2000 {
		return text, false
	}
	lines := strings.Split(text, "\n")
	if len(lines) < 30 {
		return text, false
	}
	return collapseRuns(text, lines, keepLines(lines), 10)
}

// IsFailureLine reports whether a line of command output carries a failure.
func IsFailureLine(l string) bool {
	return len(l) <= maxTrimLineBytes && failureRe.MatchString(l)
}

// recoveryID is the ID the recovery store gives text: the first 16 hex
// characters of its sha256, the same hash the marker line carries.
func recoveryID(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:8])
}
