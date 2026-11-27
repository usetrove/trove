package optimize

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/usetrove/trove/internal/config"
)

// OutputPreview is the hook_output_preview optimizer (B30). When a successful tool call returns a long text that
// no narrower optimizer shortened, it keeps a short preview (the first and last lines, every line that reports an
// error, a warning, an exit status or a summary) and stores the whole output in the recovery store first, so
// trove_fetch_full returns it byte for byte.
//
// It acts on Bash, PowerShell, Grep text results and MCP tools other than Trove's own, and never on:
//   - an error result (Claude Code already keeps a head-and-tail excerpt of failures, and errors must stay whole);
//   - a tool the user listed in optimize.preview_passthrough (Session.Passthrough);
//   - output the trimmer already shortened (it owns test, build and log output);
//   - output past the agent's own inline limit, which the agent already saves to a file with its own preview;
//   - output whose preview would not be at most half its size.
//
// The preview is never longer than previewBudget bytes, which is no more than Claude Code's own preview of an
// output it saves to a file (2,000 characters, code.claude.com/docs/en/tools-reference#output-limits, read
// 2026-11-25). So even where the user lowered the inline limit and the agent would have shown its own preview, this
// one is not larger.
//
// It is a pure function of the text and the session's pinned settings: the same output gives the same bytes.
type OutputPreview struct{}

// Preview parameters. They are fixed, not tuned: there is no benchmark to tune them from yet.
const (
	// previewMinBytes is the smallest output worth a preview, the same floor as the trimmer's default.
	previewMinBytes = 6000
	// previewBudget is the most bytes the preview may have, header included.
	previewBudget = 2000
	// previewMaxPercent: a preview is used only if it is at most this share of the output.
	previewMaxPercent = 50
)

// previewShapes are the head lines, tail lines and longest kept line, tried in order until the preview fits.
var previewShapes = []struct{ head, tail, clip int }{
	{8, 12, 200}, {5, 8, 160}, {3, 5, 120}, {2, 3, 100}, {1, 2, 80},
}

var (
	// previewSignalRe marks lines worth keeping besides failures: warnings, exit status and the summary lines tools
	// print at the end. It is deliberately broad; a kept line that did not matter costs a few bytes.
	previewSignalRe = regexp.MustCompile(`(?i)\b(?:warn(?:ing)?s?|exit(?:ed)? (?:code|status)|returned \d+|total\b|summary|\d+ (?:passed|failed|errors?|warnings?|skipped|files? changed|insertions?|deletions?|matches|results?|rows?)\b|no (?:matches|results) found|not found|denied|refused|timed? ?out|truncated)`)
	// previewMarker matches the preview's header line; group 1 is the recovery id.
	previewMarker = regexp.MustCompile(`^\[Trove: preview of a long output \(\d+ lines, \d+ bytes\)[^\]]*; full output: trove_fetch_full output_id=([0-9a-f]{16})\]$`)
)

// Name implements Base.
func (OutputPreview) Name() string { return config.OptimizerHookOutputPreview }

// Tier implements Base: the full output stays fetchable.
func (OutputPreview) Tier() Tier { return TierRecoverable }

// Points implements Capable.
func (OutputPreview) Points() []Point { return []Point{PointToolOutput} }

// OverheadTokens implements Capable: the header and gap lines are part of the preview, so they are netted out of
// BytesRemoved. The MCP tool definitions it relies on are charged per session in the technique registry.
func (OutputPreview) OverheadTokens() int { return 0 }

// Support implements SupportOverrider.
func (OutputPreview) Support(a Adapter) (Support, bool) {
	if a == AdapterGateway {
		return Support{LevelImpossible, "replaces a tool's output when it returns; the gateway would have to rewrite history, which output_trimmer already does for test and build output"}, true
	}
	return Support{}, false
}

// PreviewKey is the ledger key under which a previewed output is remembered, so a later fetch-back is attributed
// to this optimizer.
func PreviewKey(id string) string { return "v:" + id }

// PreviewIDs returns the recovery ids named by preview headers in text.
func PreviewIDs(text string) []string {
	var ids []string
	for _, l := range strings.Split(text, "\n") {
		if m := previewMarker.FindStringSubmatch(strings.TrimRight(l, "\r")); m != nil {
			ids = append(ids, m[1])
		}
	}
	return ids
}

// PreviewTool reports whether the preview may act on a tool's output at all, before the user's passthrough list.
// Trove's own MCP tools are always left alone: trove_fetch_full is how a preview is expanded, and trove_run already
// condenses its output.
func PreviewTool(name string) bool {
	switch name {
	case "Bash", "PowerShell", "Grep":
		return true
	}
	return strings.HasPrefix(name, "mcp__") && !strings.Contains(name, "__trove_")
}

// Passthrough reports whether a tool is on a passthrough list: an entry is a tool name, or a prefix ending in "*"
// ("mcp__github__*").
func Passthrough(list []string, tool string) bool {
	for _, p := range list {
		if strings.HasSuffix(p, "*") {
			if strings.HasPrefix(tool, strings.TrimSuffix(p, "*")) {
				return true
			}
		} else if p == tool {
			return true
		}
	}
	return false
}

// fileReaders are commands whose output is the contents of a file the agent asked to see. A preview would hide
// exactly what was asked for, and the agent would fetch it back: an extra turn and the whole text anyway. A replay
// of 177 local Claude Code sessions (2026-11-25) found these behind about 80% of the bytes a preview would have
// removed from Bash output, so they always pass through.
var fileReaders = map[string]bool{
	"cat": true, "sed": true, "head": true, "tail": true, "nl": true, "bat": true, "less": true, "more": true,
	"type": true, "get-content": true, "gc": true,
}

// readsFile reports whether a shell command's first command prints file contents: one of fileReaders, or git
// show, diff or blame. Leading "cd <dir> &&" steps are skipped. It looks at the first command only, so
// "go test ./... | tail -50" is not a file read.
func readsFile(command string) bool {
	c := strings.TrimSpace(command)
	for strings.HasPrefix(c, "cd ") || strings.HasPrefix(c, "Set-Location ") {
		i := strings.Index(c, "&&")
		if j := strings.IndexByte(c, ';'); j >= 0 && (i < 0 || j < i) {
			i = j - 1
		}
		if i < 0 {
			return false
		}
		c = strings.TrimSpace(c[i+2:])
	}
	f := strings.Fields(c)
	if len(f) == 0 {
		return false
	}
	name := strings.ToLower(f[0])
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.TrimSuffix(name, ".exe")
	if name == "git" && len(f) > 1 {
		switch f[1] {
		case "show", "diff", "blame":
			return true
		}
	}
	return fileReaders[name]
}

// shellCommand is the command of a Bash or PowerShell call, or "" for any other tool.
func shellCommand(call ToolCall) string {
	if call.Name != "Bash" && call.Name != "PowerShell" {
		return ""
	}
	var a struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(call.Input, &a)
	return a.Command
}

// TransformOutput implements ToolOutputOptimizer.
func (OutputPreview) TransformOutput(ctx context.Context, call ToolCall, out ToolOutput, s Session) (ToolOutput, Report) {
	if !PreviewTool(call.Name) || Passthrough(s.Passthrough, call.Name) || out.IsError || readsFile(shellCommand(call)) {
		return out, Report{}
	}
	ceiling, hasCeiling := outputCapBytes(call.Name, s.bytesPerToken())
	res := ToolOutput{Parts: make([]Part, len(out.Parts)), IsError: out.IsError}
	var originals []string
	for i, p := range out.Parts {
		res.Parts[i] = p
		if len(p.Text) < previewMinBytes || len(TrimmedIDs(p.Text)) > 0 {
			continue
		}
		if ctx.Err() != nil {
			return out, Report{Err: "time budget exceeded"}
		}
		src := sourceText(s.Raw, i, p.Text)
		if hasCeiling && int64(len(src)) > ceiling {
			continue // the agent saves this one to a file and shows its own preview
		}
		pv, ok := previewText(p.Text, recoveryID(src))
		if !ok {
			continue
		}
		res.Parts[i] = Part{Text: pv}
		originals = append(originals, src)
	}
	return res, Report{Name: config.OptimizerHookOutputPreview, Originals: originals}
}

// previewText builds the preview of text, naming the stored output by id. ok is false when no preview fits the
// budget or the preview would not be at most previewMaxPercent of the text.
func previewText(text, id string) (string, bool) {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	header := fmt.Sprintf("[Trove: preview of a long output (%d lines, %d bytes); kept the first and last lines and lines that report errors, warnings, an exit status or a summary; full output: trove_fetch_full output_id=%s]",
		len(lines), len(text), id)
	limit := min(previewBudget, len(text)*previewMaxPercent/100)
	for _, sh := range previewShapes {
		if pv, ok := buildPreview(lines, header, sh.head, sh.tail, sh.clip, limit); ok {
			return pv, true
		}
	}
	return text, false
}

// buildPreview lays out one preview shape: the header, the head and tail lines, and as many signal lines (in
// order) as fit within limit bytes. Runs of left-out lines become one gap line. ok is false when the header, head
// and tail alone do not fit.
func buildPreview(lines []string, header string, head, tail, clip, limit int) (string, bool) {
	n := len(lines)
	keep := make([]bool, n)
	for i := 0; i < n && i < head; i++ {
		keep[i] = true
	}
	for i := max(0, n-tail); i < n; i++ {
		keep[i] = true
	}
	if len(render(lines, keep, header, clip)) > limit {
		return "", false
	}
	for i, l := range lines {
		if keep[i] || len(l) > maxTrimLineBytes || !failureRe.MatchString(l) && !logAlertRe.MatchString(l) && !previewSignalRe.MatchString(l) {
			continue
		}
		keep[i] = true
		if len(render(lines, keep, header, clip)) > limit {
			keep[i] = false // the budget is spent; earlier signal lines win
			break
		}
	}
	return render(lines, keep, header, clip), true
}

// render writes the header, then every kept line (clipped) and one gap line for each run of left-out lines.
func render(lines []string, keep []bool, header string, clip int) string {
	var b strings.Builder
	b.WriteString(header)
	for i := 0; i < len(lines); {
		b.WriteByte('\n')
		if keep[i] {
			b.WriteString(clipLine(lines[i], clip))
			i++
			continue
		}
		j := i
		for j < len(lines) && !keep[j] {
			j++
		}
		fmt.Fprintf(&b, "[… %d lines …]", j-i)
		i = j
	}
	return b.String()
}

// clipLine shortens a line to at most clip bytes, on a rune boundary, saying how much was cut.
func clipLine(l string, clip int) string {
	l = strings.TrimRight(l, "\r")
	if len(l) <= clip {
		return l
	}
	cut := clip
	for cut > 0 && !utf8.RuneStart(l[cut]) {
		cut--
	}
	return fmt.Sprintf("%s[… %d bytes]", l[:cut], len(l)-cut)
}
