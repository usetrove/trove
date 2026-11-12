package stress

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/optimize"
)

// Invariant names. Each is checked on every request any layer produces.
const (
	InvValidJSON       = "valid_json"
	InvFailOpen        = "fail_open_on_invalid_input"
	InvPairing         = "tool_use_result_pairing"
	InvRecovery        = "byte_identical_recovery"
	InvPrefix          = "cached_prefix_untouched"
	InvIdempotent      = "idempotence"
	InvDeterministic   = "determinism"
	InvCostBound       = "never_costs_more_than_original_plus_overhead"
	InvFailOpenOnError = "fail_open_on_optimizer_failure"
)

// info describes an input by size and a short hash. It never returns content (rule 7).
func info(b []byte) InputInfo {
	h := sha256.Sum256(b)
	return InputInfo{Bytes: len(b), Hash: hex.EncodeToString(h[:6])}
}

// newEngine returns an engine with exactly the named optimizers on, and the given recovery store. Every
// session is priced and run as Claude Code would send it.
func newEngine(names []string, rec optimize.RecoveryStore, now time.Time) *optimize.Engine {
	return buildEngine(names, rec, now, nil)
}

// customAgent is a label the stress config marks as a custom agent. The optimizers that change prompt layout
// (cache_breakpoints) act only for custom agents, and claude_code never counts as custom, so the layers that
// must exercise them send sessions under this label. Realism does not: it models the default Claude Code path.
const customAgent = "stress_custom"

// newCustomEngine is newEngine with customAgent configured as a custom agent.
func newCustomEngine(names []string, rec optimize.RecoveryStore, now time.Time) *optimize.Engine {
	return buildEngine(names, rec, now, []string{customAgent})
}

func buildEngine(names []string, rec optimize.RecoveryStore, now time.Time, custom []string) *optimize.Engine {
	cfg := config.Default()
	cfg.Optimize.BudgetMS = 60000 // generous: a short budget makes results depend on machine load
	cfg.Optimize.CustomAgents = custom
	cfg.Optimizers = map[string]string{}
	for _, n := range names {
		cfg.Optimizers[n] = config.StateOn
	}
	return optimize.New(optimize.Options{
		Config:   cfg,
		Getenv:   func(string) string { return "" },
		Now:      func() time.Time { return now },
		Recovery: rec,
	})
}

// allNames lists every optimizer in both pipelines, request-level first.
func allNames() []string {
	var out []string
	for _, o := range optimize.Pipeline() {
		out = append(out, o.Name())
	}
	for _, b := range optimize.ToolPipeline() {
		out = append(out, b.Name())
	}
	return out
}

// memStore is an in-memory recovery store. It keeps every blob the engine stores, so the byte-identical
// check can compare them with the originals.
type memStore struct {
	mu    sync.Mutex
	blobs [][]byte
	fail  bool // when true, Put returns an error (fault injection)
}

func (m *memStore) Put(data []byte) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return "", fmt.Errorf("injected recovery store failure")
	}
	m.blobs = append(m.blobs, append([]byte(nil), data...))
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:8]), nil // the engine's recoveryID: first 16 hex characters of the sha256
}

func (m *memStore) snapshot() [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([][]byte(nil), m.blobs...)
}

// requestShape is the part of a request the structural checks read. Every field is tolerant: a string where a
// list is expected is skipped, not an error, so fuzzed bodies can still be inspected.
type requestShape struct {
	uses    []string // tool_use ids, sorted
	results []string // tool_result tool_use_ids, sorted
	outputs []string // text of every tool_result, for the recovery check
}

type rawBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	Text      string          `json:"text"`
}

// shapeOf parses a request. It returns false if the top level is not a JSON object with a messages array.
func shapeOf(body []byte) (requestShape, bool) {
	var top struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &top); err != nil {
		return requestShape{}, false
	}
	var s requestShape
	for _, m := range top.Messages {
		var blocks []rawBlock
		if err := json.Unmarshal(m.Content, &blocks); err != nil {
			continue // a plain string content has no blocks
		}
		for _, b := range blocks {
			switch b.Type {
			case "tool_use":
				s.uses = append(s.uses, b.ID)
			case "tool_result":
				s.results = append(s.results, b.ToolUseID)
				s.outputs = append(s.outputs, resultText(b.Content)...)
			}
		}
	}
	sort.Strings(s.uses)
	sort.Strings(s.results)
	return s, true
}

// resultText returns the text of a tool_result's content, which is a string or a list of text blocks.
func resultText(raw json.RawMessage) []string {
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return []string{str}
	}
	var blocks []rawBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return nil
	}
	var out []string
	for _, b := range blocks {
		if b.Type == "text" {
			out = append(out, b.Text)
		}
	}
	return out
}

// CheckRequest checks one (original, transformed) pair and returns one entry per violated invariant. Details
// describe the shape of the failure and never contain request text.
//
// overheadTokens is the overhead the applied optimizers declared (rule 15). The cost rule compares visible
// tokens: the transformed request may not exceed the original by more than that declared overhead.
func CheckRequest(orig, out []byte, overheadTokens int) []Violation {
	var vs []Violation
	if _, err := optimize.ParseRequest(orig); !json.Valid(orig) || err != nil {
		// Fail open: a request that is not a readable Messages request must come back unchanged, byte for byte.
		if !bytes.Equal(orig, out) {
			vs = append(vs, Violation{InvFailOpen, "a request that is not a readable Messages request was changed instead of forwarded as-is"})
		}
		return vs
	}
	if !json.Valid(out) {
		return append(vs, Violation{InvValidJSON, "transformed request is not valid JSON"})
	}
	if _, err := optimize.ParseRequest(out); err != nil {
		vs = append(vs, Violation{InvValidJSON, "transformed request does not parse as a Messages request"})
	}
	a, oka := shapeOf(orig)
	b, okb := shapeOf(out)
	if oka && okb && (!sameStrings(a.uses, b.uses) || !sameStrings(a.results, b.results)) {
		vs = append(vs, Violation{InvPairing, "tool_use and tool_result ids changed"})
	}
	if got, limit := visibleTokens(out), visibleTokens(orig)+int64(overheadTokens); got > limit {
		vs = append(vs, Violation{InvCostBound, fmt.Sprintf("grew by %d visible tokens, more than the declared overhead of %d",
			got-visibleTokens(orig), overheadTokens)})
	}
	return vs
}

// Violation is one broken invariant with a short, content-free detail.
type Violation struct {
	Invariant string
	Detail    string
}

// CheckRecovery verifies that every blob the engine stored is byte-identical to one of the original tool
// outputs. A blob that differs means the store holds something the tool did not print.
func CheckRecovery(orig []byte, stored [][]byte) []Violation {
	s, ok := shapeOf(orig)
	if !ok {
		return nil
	}
	originals := map[string]bool{}
	for _, t := range s.outputs {
		originals[sha(t)] = true
	}
	for _, b := range stored {
		if !originals[sha(string(b))] {
			return []Violation{{InvRecovery, fmt.Sprintf("stored %d bytes that are not a tool output", len(b))}}
		}
	}
	return nil
}

// uniqueByInvariant keeps the first violation of each invariant. A session can break the same rule on every
// turn; the report needs one line per rule per session, with the count kept by the caller.
func uniqueByInvariant(vs []Violation) []Violation {
	seen := map[string]bool{}
	out := vs[:0]
	for _, v := range vs {
		if !seen[v.Invariant] {
			seen[v.Invariant] = true
			out = append(out, v)
		}
	}
	return out
}

// CheckPrefix verifies the cache contract across turns: the previous request, minus its closing brackets,
// must still be a byte prefix of this request. Cache-control markers are ignored on both sides, because the
// breakpoint optimizer is allowed to place them. Only session-shaped bodies (ending in "]}") are checked.
func CheckPrefix(prevOut, curOut []byte) []Violation {
	if !bytes.HasSuffix(prevOut, []byte("]}")) {
		return nil
	}
	p := stripMarkers(bytes.TrimSuffix(prevOut, []byte("]}")))
	c := stripMarkers(curOut)
	if !bytes.HasPrefix(c, p) {
		return []Violation{{InvPrefix, "an earlier part of the request changed between turns"}}
	}
	return nil
}

// The breakpoint optimizer writes two shapes: a top-level marker followed by a comma, and a marker inside an
// object preceded by one. Removing the followed form first keeps the JSON valid for both.
var (
	markerFollowed = []byte(`"cache_control":{"type":"ephemeral"},`)
	markerPreceded = []byte(`,"cache_control":{"type":"ephemeral"}`)
)

func stripMarkers(b []byte) []byte {
	b = bytes.ReplaceAll(b, markerFollowed, nil)
	return bytes.ReplaceAll(b, markerPreceded, nil)
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// declaredOverheadTokens is the overhead, in tokens, that the applied optimizers declared (rule 15).
func declaredOverheadTokens(res optimize.Result) int {
	tokens := 0
	for _, r := range res.Applied {
		if r.State == config.StateOn && r.Changed {
			tokens += r.OverheadTokens
		}
	}
	return tokens
}

// structuralKeys are JSON keys whose values are protocol markup, not text the model reads: types, roles, IDs,
// model names and cache-control markers. Their bytes are not counted as visible tokens.
var structuralKeys = map[string]bool{
	"type": true, "role": true, "id": true, "tool_use_id": true, "name": true,
	"model": true, "cache_control": true,
}

// visibleTokens estimates the tokens of the text in a request: every string value except structural markup,
// at the engine's bytes-per-token ratio. It is an estimate; the provider's own counts are the reference. A body
// that cannot be decoded is counted at its full size, which is the conservative choice.
func visibleTokens(body []byte) int64 {
	text, ok := visibleText(body)
	if !ok {
		return tokensOf(len(body))
	}
	n := 0
	for _, s := range text {
		n += len(s)
	}
	return tokensOf(n)
}

// visibleText returns the text strings of a request in a fixed order (object keys sorted), skipping structural
// markup. Two requests with the same visible text give the same slice, whatever their JSON shape. It reports
// false when the body cannot be decoded.
func visibleText(body []byte) ([]string, bool) {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, false
	}
	var out []string
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case string:
			out = append(out, t)
		case []any:
			for _, e := range t {
				walk(e)
			}
		case map[string]any:
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if !structuralKeys[k] {
					walk(t[k])
				}
			}
		}
	}
	walk(v)
	return out, true
}
