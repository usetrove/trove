package optimize

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/usetrove/trove/internal/config"
)

// Two PreToolUse nudges that cut the output tokens an agent spends writing code (techniques B26 and B27, T2: they
// change what the agent is told or allowed to do, so they are off by default and need the user's explicit opt-in).
//
//   - WriteToEdit (hook_write_to_edit): a Write that replaces a large existing file with content that differs from
//     it in only a small share of lines is denied once, with the reason "use Edit for targeted changes". The agent
//     sees the reason (PreToolUse permissionDecision "deny", permissionDecisionReason "shown to Claude",
//     code.claude.com/docs/en/hooks, verified 2026-11-24). The same Write sent again goes through, so a deny can
//     never loop, and a file is denied at most once per session.
//   - EditBatch (hook_edit_batch): on the third Edit to the same file under one prompt, it adds a note suggesting
//     one Write or a single sed when the remaining edits are mechanical (PreToolUse additionalContext, "added to
//     Claude's context alongside the tool result", same page and date). It never denies.
//
// The diff is computed in memory from the file on disk and the Write's content; nothing of either is stored. The
// session ledger keeps only a hash of the path, counts and sizes.

// Thresholds. Placeholders, not set by a benchmark; the docs say so.
const (
	// WriteEditMinLines is the smallest existing file, in lines, whose rewrite is judged.
	WriteEditMinLines = 300
	// WriteEditMaxChangePct is the largest share of lines, in percent of the old and new line counts together,
	// that may differ for the Write to count as a small change.
	WriteEditMaxChangePct = 10
	// EditBatchAt is the Edit to the same file under one prompt at which the note is added (once).
	EditBatchAt = 3
	// editFixedBytes is a rough allowance for an Edit call's own JSON (tool name, path, field names) and the
	// unchanged lines it needs around each change to be unique.
	editFixedBytes = 300
)

// The messages the agent sees. Their sizes are the per-use overheads declared for B26 and B27.
const (
	writeToEditFormat = "Trove: this Write replaces a %d-line file but changes only about %d%% of its lines. Use Edit for targeted changes: it sends only the lines that change, not the whole file. If you really need to rewrite the whole file, send the same Write again and it will go through."
	editBatchNote     = "Trove: this is the third Edit to this file in this turn. If the remaining changes are mechanical (the same rename or replacement in many places), make them with one Write of the whole file or a single sed command rather than more Edit calls. If they are not mechanical, carry on."
)

// KindOverridden is the event of an agent sending a denied call again, so it went through.
const KindOverridden = "overridden"

// WriteKey and EditKey are the ledger keys of the two nudges for one file.
func WriteKey(path string) string { return "w:" + pathHash(path) }

// EditKey is the ledger key of EditBatch for one file.
func EditKey(path string) string { return "e:" + pathHash(path) }

func pathHash(path string) string {
	h := sha256.Sum256([]byte(path))
	return hex.EncodeToString(h[:])[:16]
}

func contentSum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])[:16]
}

// LineChange compares two texts line by line, as multisets: added counts lines of b with no match left in a,
// removed lines of a with no match left in b, and changedBytes the bytes of both. It is order-blind, so it can only
// undercount a change, never invent one; a moved block counts as unchanged. It is deterministic and linear.
func LineChange(a, b []byte) (aLines, bLines, added, removed int, changedBytes int64) {
	// A final newline ends the last line; it does not start another.
	a, b = bytes.TrimSuffix(a, []byte("\n")), bytes.TrimSuffix(b, []byte("\n"))
	left := map[string]int{}
	for _, l := range bytes.Split(a, []byte("\n")) {
		left[string(l)]++
		aLines++
	}
	for _, l := range bytes.Split(b, []byte("\n")) {
		bLines++
		if left[string(l)] > 0 {
			left[string(l)]--
			continue
		}
		added++
		changedBytes += int64(len(l)) + 1
	}
	for l, n := range left {
		removed += n
		changedBytes += int64(n) * int64(len(l)+1)
	}
	return aLines, bLines, added, removed, changedBytes
}

// WriteToEdit is the hook_write_to_edit optimizer.
type WriteToEdit struct{}

// Name implements Base.
func (WriteToEdit) Name() string { return config.OptimizerHookWriteToEdit }

// Tier implements Base: it changes what the agent may do.
func (WriteToEdit) Tier() Tier { return TierBehavior }

// Points implements Capable.
func (WriteToEdit) Points() []Point { return []Point{PointToolInput} }

// OverheadTokens implements Capable: the reason is sent only when it acts (declared per use for B26).
func (WriteToEdit) OverheadTokens() int { return 0 }

// Support implements SupportOverrider.
func (WriteToEdit) Support(a Adapter) (Support, bool) {
	if a == AdapterGateway {
		return Support{LevelImpossible, "needs the file on disk before the Write runs; the gateway only sees finished history"}, true
	}
	return Support{}, false
}

type writeArgs struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

// TransformInput implements ToolInputOptimizer.
func (WriteToEdit) TransformInput(_ context.Context, call ToolCall, s Session) (ToolCall, Report) {
	if call.Name != "Write" || s.Ledger == nil {
		return call, Report{}
	}
	var a writeArgs
	if json.Unmarshal(call.Input, &a) != nil || !filepath.IsAbs(a.FilePath) {
		return call, Report{}
	}
	name := config.OptimizerHookWriteToEdit
	key := WriteKey(a.FilePath)
	newSum := contentSum([]byte(a.Content))
	if ent, seen := s.Ledger.Get(key); seen {
		if ent.BlockedSum == newSum && s.Mode != config.StateShadow {
			// The agent sent the denied Write again: let it through and count it, because the deny did not help.
			ent.BlockedSum = "-"
			s.Ledger.Put(key, ent)
			if err := s.Ledger.Save(); err != nil {
				return call, Report{Err: "ledger"}
			}
			return call, Report{Name: name, Decision: &Decision{Kind: KindOverridden, Bytes: ent.Size, Tokens: EstTokensAt(ent.Size, s.bytesPerToken())}}
		}
		return call, Report{} // at most one deny per file per session
	}
	fi, err := os.Stat(a.FilePath)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() == 0 || fi.Size() > MaxHashBytes {
		return call, Report{} // a new file, or one too large to read quickly: never judged
	}
	old, err := os.ReadFile(a.FilePath) //nolint:gosec // the file the agent is about to overwrite, read in memory only
	if err != nil {
		return call, Report{}
	}
	oldN, newN, added, removed, changed := LineChange(old, []byte(a.Content))
	if oldN < WriteEditMinLines {
		return call, Report{}
	}
	pct := 100 * (added + removed) / (oldN + newN)
	if pct > WriteEditMaxChangePct {
		return call, Report{}
	}
	// What an Edit would have cost instead: the changed lines on both sides plus its fixed allowance (estimated).
	saved := int64(len(a.Content)) - (changed + editFixedBytes)
	if saved <= 0 {
		return call, Report{}
	}
	tokens := EstTokensAt(saved, s.bytesPerToken())
	ent := ReadEntry{Size: saved, Prompt: s.Ledger.Prompts(), At: s.Now.Unix(), BlockedSum: newSum}
	kind := KindBlocked
	if s.Mode == config.StateShadow {
		kind, ent.BlockedSum = KindWouldBlock, "-"
	}
	s.Ledger.Put(key, ent)
	if err := s.Ledger.Save(); err != nil {
		return call, Report{Err: "ledger"} // if the deny cannot be remembered, do not deny: it could loop
	}
	d := &Decision{Kind: kind, Bytes: saved, Tokens: tokens}
	if kind == KindBlocked {
		d.Message = fmt.Sprintf(writeToEditFormat, oldN, max(pct, 1))
	}
	return call, Report{Name: name, Decision: d}
}

// EditBatch is the hook_edit_batch optimizer.
type EditBatch struct{}

// Name implements Base.
func (EditBatch) Name() string { return config.OptimizerHookEditBatch }

// Tier implements Base: it changes what the agent is told.
func (EditBatch) Tier() Tier { return TierBehavior }

// Points implements Capable.
func (EditBatch) Points() []Point { return []Point{PointToolInput} }

// OverheadTokens implements Capable: the note is sent only when it acts (declared per use for B27).
func (EditBatch) OverheadTokens() int { return 0 }

// Support implements SupportOverrider.
func (EditBatch) Support(a Adapter) (Support, bool) {
	if a == AdapterGateway {
		return Support{LevelImpossible, "the note has to reach the agent before the Edit runs; the gateway only sees finished history"}, true
	}
	return Support{}, false
}

// TransformInput implements ToolInputOptimizer. The ledger entry for a file holds the prompt number in Prompt, the
// number of Edits under that prompt in MTime, and their input bytes in Size.
func (EditBatch) TransformInput(_ context.Context, call ToolCall, s Session) (ToolCall, Report) {
	if (call.Name != "Edit" && call.Name != "MultiEdit") || s.Ledger == nil {
		return call, Report{}
	}
	var a struct {
		FilePath string `json:"file_path"`
	}
	if json.Unmarshal(call.Input, &a) != nil || a.FilePath == "" {
		return call, Report{}
	}
	key := EditKey(a.FilePath)
	prompt := s.Ledger.Prompts()
	ent, ok := s.Ledger.Get(key)
	if !ok || ent.Prompt != prompt {
		ent = ReadEntry{Prompt: prompt}
	}
	ent.MTime++
	ent.Size += int64(len(call.Input))
	ent.At = s.Now.Unix()
	s.Ledger.Put(key, ent)
	if err := s.Ledger.Save(); err != nil {
		return call, Report{Err: "ledger"}
	}
	if ent.MTime != EditBatchAt {
		return call, Report{}
	}
	// Bytes is the output already spent on this file's Edits in this turn: an upper bound on what batching the rest
	// could save, never a saving in itself (estimated).
	d := &Decision{Kind: KindAdvised, Bytes: ent.Size, Tokens: EstTokensAt(ent.Size, s.bytesPerToken()), Message: editBatchNote}
	if s.Mode == config.StateShadow {
		d.Kind, d.Message = KindWouldAdvise, ""
	}
	return call, Report{Name: config.OptimizerHookEditBatch, Decision: d}
}
