package optimize

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/usetrove/trove/internal/config"
)

// OutputDedup is the hook_output_dedup optimizer. After a Bash, Grep or Glob call it hashes each part of the
// output as the tool produced it. When the same bytes already came back earlier in this session, the part is
// replaced by one marker line naming the earlier copy and the full output's recovery id; the original goes to
// the recovery store first, so trove_fetch_full returns it byte for byte.
//
// There is no time-based expiry: a hash is forgotten only when the session's ledger is cleared (compaction or a
// resumed session, where the earlier copy may no longer be in context) or when the ledger drops its oldest
// entries. Error outputs are never collapsed, because the agent may need to see an error repeat. Read is left out
// until a live probe confirms its output shape.
type OutputDedup struct{}

// Name implements Base.
func (OutputDedup) Name() string { return config.OptimizerHookOutputDedup }

// Tier implements Base: the full output stays fetchable.
func (OutputDedup) Tier() Tier { return TierRecoverable }

// Points implements Capable.
func (OutputDedup) Points() []Point { return []Point{PointToolOutput} }

// OverheadTokens implements Capable: the marker line replaces the text it stands for, so it is netted out of
// BytesRemoved. The MCP tool definitions it relies on are charged per session in the technique registry.
func (OutputDedup) OverheadTokens() int { return 0 }

// Support implements SupportOverrider.
func (OutputDedup) Support(a Adapter) (Support, bool) {
	if a == AdapterGateway {
		return Support{LevelImpossible, "the gateway's duplicate_result_collapser does this on the request; this one acts when the tool returns"}, true
	}
	return Support{}, false
}

// seenKey is the ledger key under which an output's first appearance is remembered.
func seenKey(id string) string { return "u:" + id }

// DedupKey is the ledger key under which a collapsed output is remembered, so a later fetch-back can be
// attributed to this optimizer.
func DedupKey(id string) string { return "d:" + id }

// dedupMarker matches the line that replaces a collapsed output; group 1 is the recovery id.
var dedupMarker = regexp.MustCompile(`^\[Trove: identical to an output earlier in this session \(prompt #\d+, [^)]*\); full output sha256:([0-9a-f]{16})\]$`)

func dedupNote(prompt int, ago, id string) string {
	return fmt.Sprintf("[Trove: identical to an output earlier in this session (prompt #%d, %s ago); full output sha256:%s]", prompt, ago, id)
}

// DedupIDs returns the recovery ids named by dedup markers in text.
func DedupIDs(text string) []string {
	var ids []string
	for _, l := range strings.Split(text, "\n") {
		if m := dedupMarker.FindStringSubmatch(strings.TrimRight(l, "\r")); m != nil {
			ids = append(ids, m[1])
		}
	}
	return ids
}

// TransformOutput implements ToolOutputOptimizer.
func (OutputDedup) TransformOutput(_ context.Context, call ToolCall, out ToolOutput, s Session) (ToolOutput, Report) {
	switch call.Name {
	case "Bash", "Grep", "Glob":
	default:
		return out, Report{}
	}
	if s.Ledger == nil || out.IsError {
		return out, Report{}
	}
	res := ToolOutput{Parts: make([]Part, len(out.Parts)), IsError: out.IsError}
	var originals []string
	dirty := false
	for i, p := range out.Parts {
		res.Parts[i] = p
		src := sourceText(s.Raw, i, p.Text)
		if len(src) < minDuplicateBytes {
			continue
		}
		id := recoveryID(src)
		ent, seen := s.Ledger.Get(seenKey(id))
		if !seen {
			s.Ledger.Put(seenKey(id), ReadEntry{Size: int64(len(src)), Prompt: s.Ledger.Prompts(), At: s.Now.Unix()})
			dirty = true
			continue
		}
		note := dedupNote(ent.Prompt, ago(s.Now, ent.At), id)
		if len(note) >= len(p.Text) {
			continue
		}
		res.Parts[i] = Part{Text: note}
		originals = append(originals, src)
	}
	if dirty {
		if err := s.Ledger.Save(); err != nil {
			return out, Report{Err: "ledger"}
		}
	}
	return res, Report{Name: config.OptimizerHookOutputDedup, Originals: originals}
}
