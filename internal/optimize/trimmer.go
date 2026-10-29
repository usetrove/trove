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
// It is recoverable only if the agent can fetch the full output back, which
// needs the MCP fetch tool (Prompt 11). Until that exists this optimizer is
// built and tested but cannot be switched on: config validation rejects "on"
// and the engine clamps it to "shadow", which only estimates and changes nothing.
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
	var edits []Edit
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
	if trimmed < minTrimmedLines {
		return text, false
	}
	return strings.Join(out, "\n"), true
}
