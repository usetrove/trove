package optimize

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/mcp/outline"
)

// ReadOutline is the hook_read_outline optimizer. When a Read without offset or limit returns a very large
// source file, it replaces the file text with the file's outline (functions and types with their line ranges,
// from the same outliner trove_read uses) and says how to read a section. The full text goes to the recovery
// store first, so trove_fetch_full returns it byte for byte, and a second full Read of the same file goes through
// untouched: a file is outlined at most once a session.
//
// That second full Read, or a fetch of the full text, is an extra read: the agent needed the whole file after
// all. It is counted once per outlined file and the hook_extra_read guardrail rolls the optimizer back when it
// happens too often. A ranged Read is how an outline is meant to be used and is never counted.
//
// Built-in Read honours a replaced output only on agent versions a live probe has seen do it; the hooks adapter
// keeps the optimizer in shadow until then.
type ReadOutline struct{}

// ReadOutlineMinBytes is the size from which a full Read is outlined. Smaller files are cheap to read whole.
const ReadOutlineMinBytes = 40_000

// readOutlineMinSymbols is the fewest declarations an outline must list to be worth showing instead of the file.
const readOutlineMinSymbols = 3

// readOutlineMaxRows caps the outline, like trove_read's.
const readOutlineMaxRows = 200

// Name implements Base.
func (ReadOutline) Name() string { return config.OptimizerHookReadOutline }

// Tier implements Base: the full file stays fetchable and readable.
func (ReadOutline) Tier() Tier { return TierRecoverable }

// Points implements Capable.
func (ReadOutline) Points() []Point { return []Point{PointToolOutput} }

// OverheadTokens implements Capable: the outline replaces the text it stands for, so its size is netted out of
// BytesRemoved. The MCP tool definitions it relies on are charged per session in the technique registry.
func (ReadOutline) OverheadTokens() int { return 0 }

// Support implements SupportOverrider.
func (ReadOutline) Support(a Adapter) (Support, bool) {
	if a == AdapterGateway {
		return Support{LevelImpossible, "changing a tool result in history would break the cached prefix; this acts when the tool returns"}, true
	}
	return Support{}, false
}

// OutlinedKey is the ledger key for "this file's full read was replaced by its outline". The path is hashed,
// never stored.
func OutlinedKey(path string) string { return "o:" + ReadKey(path, 0, 0) }

// outlineFetchKey is the ledger key that ties an outlined file's recovery id to its OutlinedKey, so a fetch of
// the full text is attributed to this optimizer.
func outlineFetchKey(id string) string { return "of:" + id }

// MarkOutlined remembers that the file at path was outlined and its full text stored under id. The hooks adapter
// calls it only after the outline has been sent, so a mark always stands for an outline the agent saw.
func MarkOutlined(l ReadLedger, path, id string, prompt int, at int64) {
	l.Put(OutlinedKey(path), ReadEntry{Prompt: prompt, At: at})
	l.Put(outlineFetchKey(id), ReadEntry{Sum: OutlinedKey(path), Prompt: prompt, At: at})
}

// CountOutlineFetch reports whether a trove_fetch_full of id is the first extra read of a file this optimizer
// outlined, and marks it counted. A full Read and a fetch of the same file count once between them. The caller
// saves the ledger.
func CountOutlineFetch(l ReadLedger, id string) bool {
	f, ok := l.Get(outlineFetchKey(id))
	if !ok || f.Sum == "" {
		return false
	}
	ent, ok := l.Get(f.Sum)
	if !ok || ent.BlockedSum != "" {
		return false
	}
	ent.BlockedSum = "counted"
	l.Put(f.Sum, ent)
	return true
}

// outlinedUncounted reports whether the file was outlined and the agent has not yet read it whole.
func outlinedUncounted(l ReadLedger, path string) bool {
	ent, ok := l.Get(OutlinedKey(path))
	return ok && ent.BlockedSum == ""
}

// outlineMarker matches the outline's first line; group 1 is the recovery id.
var outlineMarker = regexp.MustCompile(`full file sha256:([0-9a-f]{16})\]$`)

// OutlineIDs returns the recovery ids named by outline markers in text.
func OutlineIDs(text string) []string {
	var ids []string
	for _, l := range strings.Split(text, "\n") {
		if !strings.HasPrefix(l, "[Trove: ") {
			continue
		}
		if m := outlineMarker.FindStringSubmatch(strings.TrimRight(l, "\r")); m != nil {
			ids = append(ids, m[1])
		}
	}
	return ids
}

// readOutlineText is what the agent sees instead of the file. It depends only on the file's bytes, so the same
// file always gives the same text (rule 5).
func readOutlineText(src string, syms []outline.Symbol, id string) string {
	lines := strings.Count(src, "\n")
	if !strings.HasSuffix(src, "\n") {
		lines++
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[Trove: this file is large (%d lines, %d bytes), so only its outline is shown. Read a section with offset and limit using the line ranges below. To see the whole file, Read it again without offset or limit and it will go through, or call trove_fetch_full with this output_id. full file sha256:%s]\n", lines, len(src), id)
	for i, s := range syms {
		if i == readOutlineMaxRows {
			fmt.Fprintf(&b, "[%d more symbols omitted]\n", len(syms)-i)
			break
		}
		fmt.Fprintf(&b, "%s %s %d-%d\n", s.Kind, s.Name, s.Start, s.End)
	}
	return b.String()
}

// TransformOutput implements ToolOutputOptimizer.
func (ReadOutline) TransformOutput(_ context.Context, call ToolCall, out ToolOutput, s Session) (ToolOutput, Report) {
	if call.Name != "Read" || s.Ledger == nil || out.IsError || len(out.Parts) != 1 {
		return out, Report{}
	}
	a, ok := parseReadArgs(call.Input)
	if !ok || a.FilePath == "" || a.Offset != 0 || a.Limit != 0 {
		return out, Report{}
	}
	if ent, seen := s.Ledger.Get(OutlinedKey(a.FilePath)); seen {
		if ent.BlockedSum != "" {
			return out, Report{} // already read whole once; never outlined again this session
		}
		ent.BlockedSum = "counted"
		s.Ledger.Put(OutlinedKey(a.FilePath), ent)
		if err := s.Ledger.Save(); err != nil {
			return out, Report{} // an uncounted read is a missing count, never a failure of the call
		}
		return out, Report{Name: config.OptimizerHookReadOutline, Decision: &Decision{Kind: KindExtraRead}}
	}
	src := sourceText(s.Raw, 0, out.Parts[0].Text)
	if len(src) < ReadOutlineMinBytes {
		return out, Report{}
	}
	syms := outline.Of(a.FilePath, []byte(src))
	if len(syms) < readOutlineMinSymbols {
		return out, Report{}
	}
	note := readOutlineText(src, syms, recoveryID(src))
	if len(note) >= len(src) {
		return out, Report{}
	}
	return ToolOutput{Parts: []Part{{Text: note}}}, Report{Name: config.OptimizerHookReadOutline, Originals: []string{src}}
}
