package optimize

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"github.com/usetrove/trove/internal/config"
)

// Fixed parameters of the output trimmer. The trimmer only acts on text that is
// recognizably test or build output, never on a file the agent read. The values a
// repo may tune are in TrimThresholds below.
const (
	minSignature     = 10 // lines that look like test/build output
	contextBefore    = 2
	contextAfter     = 2
	maxStackLines    = 40
	maxTrimLineBytes = 400
)

// TrimThresholds are the output trimmer's tunable parameters. Output smaller
// than MinBytes or MinLines is not worth trimming; KeepHead and KeepTail are the
// first and last lines always kept (runners print their summary there); fewer
// than MinTrimmedLines trimmed lines is not worth a marker.
//
// They are an explicit input, never read from global state, so Transform stays a
// pure function of the request and these values. An adapter pins them at the
// start of a session (Session.Tuning) so cached prefixes stay valid (rule 5).
type TrimThresholds struct {
	MinBytes        int
	MinLines        int
	MinTrimmedLines int
	KeepHead        int
	KeepTail        int
}

// DefaultTrimThresholds are the shipped values, used whenever a session carries
// no tuning.
var DefaultTrimThresholds = TrimThresholds{MinBytes: 6000, MinLines: 60, MinTrimmedLines: 20, KeepHead: 5, KeepTail: 10}

// TuneParam describes one tunable parameter: its name, its shipped default and
// the range any tuned value is clamped to, so a bad stored value can never make
// the trimmer unsafe.
type TuneParam struct {
	Name     string
	Default  int
	Min, Max int
}

// The trimmer's parameter names, as used in Session.Tuning keys (TuningKey).
const (
	ParamMinBytes        = "min_bytes"
	ParamMinLines        = "min_lines"
	ParamMinTrimmedLines = "min_trimmed_lines"
	ParamKeepHead        = "keep_head"
	ParamKeepTail        = "keep_tail"
)

// TrimParams lists the trimmer's tunable parameters in a fixed order.
var TrimParams = []TuneParam{
	{ParamMinBytes, 6000, 2000, 50000},
	{ParamMinLines, 60, 20, 500},
	{ParamMinTrimmedLines, 20, 10, 200},
	{ParamKeepHead, 5, 2, 20},
	{ParamKeepTail, 10, 5, 40},
}

// Tuning holds per-session parameter values keyed by TuningKey. A missing key
// means the shipped default. It is copied into the session's pin when the session
// starts and never changes afterwards.
type Tuning map[string]int

// TuningKey is the key of an optimizer's parameter in a Tuning.
func TuningKey(optimizer, param string) string { return optimizer + "." + param }

// Clamp returns v limited to the parameter's allowed range.
func (p TuneParam) Clamp(v int) int { return min(max(v, p.Min), p.Max) }

// TrimThresholdsFrom resolves a session's tuning into thresholds, clamping every
// value to its range. A nil Tuning gives DefaultTrimThresholds.
func TrimThresholdsFrom(t Tuning) TrimThresholds {
	get := func(p TuneParam) int {
		if v, ok := t[TuningKey(config.OptimizerOutputTrimmer, p.Name)]; ok {
			return p.Clamp(v)
		}
		return p.Default
	}
	return TrimThresholds{
		MinBytes: get(TrimParams[0]), MinLines: get(TrimParams[1]), MinTrimmedLines: get(TrimParams[2]),
		KeepHead: get(TrimParams[3]), KeepTail: get(TrimParams[4]),
	}
}

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
//
// The stored original is the output exactly as the tool produced it, byte for
// byte, even when an earlier optimizer (noise_normalizer) has already changed the
// text the model sees. The marker's hash names that same original. A fetch
// therefore returns what the tool printed, not a cleaned copy.
//
// Trimming is a pure function of the text, so the trimmer remembers what it did
// for each text it has seen (trimSpan). An agent resends its history on every
// request, so most outputs are not new; they cost a lookup, not a rescan.
type OutputTrimmer struct {
	// memo maps the hash of (thresholds, text) to the trimmed text, with the
	// source's hash left as a placeholder. Nil (the zero value) disables it.
	memo *spanMemo
	// placeholder is the source hash the memo stores in place of the real one.
	placeholder string
}

// placeholderSource is a fixed string whose hash stands in for the real source
// in memoized trims. It never reaches a request: fillMarkers replaces it.
const placeholderSource = "trove-trimmer-placeholder-source"

// markerHashPrefix is the text before the hash in every trim marker.
const markerHashPrefix = "full output sha256:"

// newOutputTrimmer returns a trimmer with its memo, as the pipelines use it.
func newOutputTrimmer() OutputTrimmer {
	return OutputTrimmer{memo: newSpanMemo(), placeholder: recoveryID(placeholderSource)}
}

// trimSpan is trimWithSource, memoized by content. A memo hit fills in the real
// source hash, so the result is exactly what trimWithSource would give. When the
// template cannot be filled safely (the text already contains the placeholder or
// a marker of its own), it falls back to computing directly.
func (t OutputTrimmer) trimSpan(text string, origin func() string, th TrimThresholds) (string, bool) {
	if t.memo == nil || t.placeholder == "" || len(text) < memoMinSpan {
		return trimWithSource(text, origin, th)
	}
	h := sha256.New()
	_, _ = h.Write([]byte(fmt.Sprint(th)))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(text))
	var key [sha256.Size]byte
	copy(key[:], h.Sum(nil))

	if repl, known := t.memo.get(key); known {
		if repl == nil {
			return text, false
		}
		return fillMarkers(string(repl), t.placeholder, origin()), true
	}
	tmpl, changed := trimWithSource(text, func() string { return placeholderSource }, th)
	if !changed {
		t.memo.put(key, nil)
		return text, false
	}
	markers := strings.Count(tmpl, markerHashPrefix)
	if strings.Count(tmpl, markerHashPrefix+t.placeholder) != markers || strings.Count(tmpl, t.placeholder) != markers {
		return trimWithSource(text, origin, th) // not safe to template: compute directly
	}
	t.memo.put(key, []byte(tmpl))
	return fillMarkers(tmpl, t.placeholder, origin()), true
}

// trimRaw is trimSpan for a request span, keyed by the span's raw JSON bytes. The
// same text escaped the same way has the same bytes, so a hit is exact; a different
// escaping of the same text is only a miss. decode runs only on a miss. It reports
// ok=false when the span cannot be decoded, which leaves it unchanged.
func (t OutputTrimmer) trimRaw(raw []byte, th TrimThresholds, decode func() (string, bool), origin func() string) (string, bool, bool) {
	if t.memo == nil || t.placeholder == "" || len(raw) < memoMinSpan {
		text, ok := decode()
		if !ok {
			return "", false, false
		}
		tr, changed := trimWithSource(text, origin, th)
		return tr, changed, true
	}
	h := sha256.New()
	_, _ = h.Write([]byte(fmt.Sprint(th)))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(raw)
	var key [sha256.Size]byte
	copy(key[:], h.Sum(nil))

	if repl, known := t.memo.get(key); known {
		if repl == nil {
			return "", false, true
		}
		return fillMarkers(string(repl), t.placeholder, origin()), true, true
	}
	text, ok := decode()
	if !ok {
		return "", false, false
	}
	tmpl, changed := trimWithSource(text, func() string { return placeholderSource }, th)
	if !changed {
		t.memo.put(key, nil)
		return "", false, true
	}
	markers := strings.Count(tmpl, markerHashPrefix)
	if strings.Count(tmpl, markerHashPrefix+t.placeholder) != markers || strings.Count(tmpl, t.placeholder) != markers {
		tr, ch := trimWithSource(text, origin, th) // not safe to template: compute directly
		return tr, ch, true
	}
	t.memo.put(key, []byte(tmpl))
	return fillMarkers(tmpl, t.placeholder, origin()), true, true
}

// fillMarkers writes the real source's hash into every marker of a template.
func fillMarkers(tmpl, placeholder, source string) string {
	return strings.ReplaceAll(tmpl, markerHashPrefix+placeholder, markerHashPrefix+recoveryID(source))
}

// Name implements Optimizer.
func (OutputTrimmer) Name() string { return "output_trimmer" }

// Tier implements Optimizer.
func (OutputTrimmer) Tier() Tier { return TierRecoverable }

// Points implements Capable.
func (OutputTrimmer) Points() []Point { return []Point{PointRequest, PointToolOutput} }

// OverheadTokens implements Capable. The trimmer's marker line is netted out of
// BytesRemoved; the cost that is always present is the MCP tool definitions the
// agent needs to fetch trimmed output back (trove_fetch_full, trove_run).
// TODO(measure): fill this from `trove mcp schema-size` once it is wired in.
func (OutputTrimmer) OverheadTokens() int { return 0 }

// TransformOutput implements ToolOutputOptimizer, using the same trimOutput as
// the request-level Transform. The output as the tool produced it goes in
// Report.Originals (see sourceText); the engine stores it before the shortened
// text is returned.
func (t OutputTrimmer) TransformOutput(ctx context.Context, _ ToolCall, out ToolOutput, s Session) (ToolOutput, Report) {
	th := TrimThresholdsFrom(s.Tuning)
	res := ToolOutput{Parts: make([]Part, len(out.Parts)), IsError: out.IsError}
	var originals []string
	for i, p := range out.Parts {
		res.Parts[i] = p
		if len(p.Text) < th.MinBytes {
			continue
		}
		if ctx.Err() != nil {
			return out, Report{Err: "time budget exceeded"}
		}
		own, at := p.Text, i
		origin := func() string { return sourceText(s.Raw, at, own) }
		if trimmed, changed := t.trimSpan(p.Text, origin, th); changed {
			res.Parts[i] = Part{Text: trimmed}
			originals = append(originals, origin())
		}
	}
	return res, Report{Name: "output_trimmer", Originals: originals}
}

// sourceText is the text the tool produced for span i: the engine's pre-pipeline
// copy when it provided one, otherwise the optimizer's own input. It is called
// only for spans that are actually shortened, so the engine decodes nothing else.
func sourceText(raw func(int) string, i int, own string) string {
	if raw != nil {
		if s := raw(i); s != "" {
			return s
		}
	}
	return own
}

// Applies implements Optimizer: any large tool result.
func (OutputTrimmer) Applies(r *Request, s Session) bool {
	minBytes := TrimThresholdsFrom(s.Tuning).MinBytes
	for _, m := range r.Messages {
		for _, b := range m.Blocks {
			for _, sp := range b.Texts {
				if sp.Len() >= minBytes {
					return true
				}
			}
		}
	}
	return false
}

// Transform implements Optimizer, with the shipped thresholds. The engine calls
// TransformSession instead, so a session's pinned tuning applies.
func (t OutputTrimmer) Transform(ctx context.Context, r *Request) (*Request, Report) {
	return t.TransformSession(ctx, r, Session{})
}

// TransformSession implements SessionTransformer: Transform with the session's
// pinned tuning. It is a pure function of the request and that tuning.
func (t OutputTrimmer) TransformSession(ctx context.Context, r *Request, s Session) (*Request, Report) {
	th := TrimThresholdsFrom(s.Tuning)
	if err := r.Ensure(); err != nil {
		return r, Report{Err: err.Error()}
	}
	var edits []Edit
	var originals []string
	idx := -1 // index of the current span, counting every span, as SpanTexts does
	for _, m := range r.Messages {
		for _, b := range m.Blocks {
			for _, sp := range b.Texts {
				idx++
				if sp.Len() < th.MinBytes {
					continue
				}
				if ctx.Err() != nil {
					return r, Report{Err: "time budget exceeded"}
				}
				// The span is decoded only when the memo does not know it: an
				// unchanged span costs one hash of its raw bytes, not a decode.
				at := idx
				decode := func() (string, bool) { return r.DecodeString(sp) }
				origin := func() string { text, _ := r.DecodeString(sp); return sourceText(s.Raw, at, text) }
				trimmed, changed, ok := t.trimRaw(r.Raw[sp.S:sp.E], th, decode, origin)
				if !ok {
					continue
				}
				if changed {
					edits = append(edits, Edit{Span: sp, New: EncodeString(trimmed)})
					originals = append(originals, origin())
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
func trimOutput(text string) (string, bool) { return trimWith(text, DefaultTrimThresholds) }

// trimWith is trimOutput with explicit thresholds.
func trimWith(text string, th TrimThresholds) (string, bool) {
	return trimWithSource(text, func() string { return text }, th)
}

// trimWithSource trims text, and names the full output by the hash of source(),
// which is called only when a marker is written. source is the output as the
// tool produced it; text may be a copy an earlier optimizer already normalized.
// The marker must name the source, because the source is what the recovery
// store keeps and what trove_fetch_full returns.
func trimWithSource(text string, source func() string, th TrimThresholds) (string, bool) {
	if len(text) < th.MinBytes {
		return text, false
	}
	lines := strings.Split(text, "\n")
	if len(lines) < th.MinLines {
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

	return collapseRunsFrom(text, source, lines, keepLines(lines, th), th.MinTrimmedLines)
}

// keepLines marks the lines worth keeping: the head and tail, every failure or
// stack-frame line with its context, and any existing trim marker.
func keepLines(lines []string, th TrimThresholds) []bool {
	keep := make([]bool, len(lines))
	for i := 0; i < len(lines) && i < th.KeepHead; i++ {
		keep[i] = true
	}
	for i := max(0, len(lines)-th.KeepTail); i < len(lines); i++ {
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
			if !strings.HasPrefix(c, " ") && !strings.HasPrefix(c, "\t") && !frameRe.MatchString(c) {
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
	return collapseRunsFrom(text, func() string { return text }, lines, keep, minTrimmed)
}

// collapseRunsFrom is collapseRuns with the marker's hash taken from source.
func collapseRunsFrom(text string, source func() string, lines []string, keep []bool, minTrimmed int) (string, bool) {
	var out []string
	hash := sha256.Sum256([]byte(source()))
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
	return collapseRuns(text, lines, keepLines(lines, DefaultTrimThresholds), 10)
}

// IsFailureLine reports whether a line of command output carries a failure.
func IsFailureLine(l string) bool {
	return len(l) <= maxTrimLineBytes && failureRe.MatchString(l)
}

// TrimmedIDs returns the output ids named by the trim markers in text, in order.
func TrimmedIDs(text string) []string {
	var ids []string
	for _, l := range strings.Split(text, "\n") {
		if m := trimMarker.FindString(strings.TrimRight(l, "\r")); m != "" {
			ids = append(ids, m[len(m)-17:len(m)-1])
		}
	}
	return ids
}

// recoveryID is the ID the recovery store gives text: the first 16 hex
// characters of its sha256, the same hash the marker line carries.
func recoveryID(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:8])
}
