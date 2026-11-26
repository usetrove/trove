package hooks

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/recovery"
)

const rwPreview = config.OptimizerHookOutputPreview

var previewRe = regexp.MustCompile(`full output: trove_fetch_full output_id=([0-9a-f]{16})\]`)

func previewRig(t *testing.T, s Snapshot, caps ...Capability) *rig {
	t.Helper()
	noClock(t)
	r := newRig(t, nil)
	if s.States == nil {
		s.States = map[string]string{rwPreview: "on"}
	}
	r.snapshot(s)
	r.allow(caps...)
	r.start()
	return r
}

// gitLog is long, ordinary command output: not test or build output, so the trimmer never touches it.
func gitLog() string {
	var b strings.Builder
	for i := 0; i < 200; i++ { // about 22 KB: under the 30,000-character Bash inline limit
		fmt.Fprintf(&b, "commit %040x\nAuthor: dev <dev@example.com>\n    change number %d to the parser\n", i, i)
	}
	return b.String()
}

// mcp runs post_tool_output for an MCP tool whose result is the given JSON.
func (r *rig) mcp(tool string, response any) (string, int) {
	r.t.Helper()
	j, _ := json.Marshal(response)
	return r.run(EventPostOutput, r.input("claude_post_mcp.json", "{{TOOL}}", tool, "{{RESPONSE}}", string(j)))
}

func TestALongBashOutputBecomesAPreviewAndItsFetchIsChargedNet(t *testing.T) {
	r := previewRig(t, Snapshot{TroveRun: true}, Capability{Tool: "Bash", OutputRewrite: CapYes})
	full := gitLog()
	out, code := r.post("claude_post_bash.json", full, false)
	if code != 0 || out == "" {
		t.Fatalf("code %d output %q; log: %s", code, out, r.hookLog())
	}
	var resp map[string]any
	if err := json.Unmarshal(parseOut(t, out).H.Output, &resp); err != nil {
		t.Fatal(err)
	}
	stdout := resp["stdout"].(string)
	if len(stdout) > 2000 || resp["interrupted"] != false || resp["isImage"] != false {
		t.Fatalf("the preview must keep the Bash shape and stay within 2,000 bytes: %d bytes, %v", len(stdout), resp)
	}
	m := previewRe.FindStringSubmatch(stdout)
	if m == nil {
		t.Fatalf("no preview header in %q", stdout)
	}
	rec, err := recovery.Open(filepath.Join(r.dir, "recovery"), recovery.Options{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if got, _, err := rec.Get(m[1], 1, 1<<20); err != nil || strings.TrimRight(got, "\n") != strings.TrimRight(full, "\n") {
		t.Fatalf("the full output must be fetchable before the preview is sent: %v", err)
	}
	var saved int64
	for _, e := range r.events() {
		if e.Hook == rwPreview && e.Kind == KindRewritten {
			saved = e.Tokens
		}
	}
	if saved <= 0 {
		t.Fatalf("events = %v", r.kinds())
	}

	// Fetching it back is charged against the saving: once, with the size of what the fetch returned.
	r.fetch(m[1])
	r.fetch(m[1])
	var fetched []event
	for _, e := range r.events() {
		if e.Kind == KindFetchedBack {
			fetched = append(fetched, e)
		}
	}
	if len(fetched) != 1 || fetched[0].Hook != rwPreview || fetched[0].Bytes != int64(len("(the rest of the output)")) || fetched[0].Tokens <= 0 {
		t.Fatalf("fetched_back events = %+v", fetched)
	}
}

func TestMCPOutputIsPreviewedInItsOwnShapeOnlyOnceProbed(t *testing.T) {
	full := gitLog()
	blocks := map[string]any{"content": []any{map[string]any{"type": "text", "text": full}}}

	// Not probed: shadow only.
	r := previewRig(t, Snapshot{TroveRun: true}, Capability{Tool: "Bash", OutputRewrite: CapYes})
	if out, _ := r.mcp("mcp__db__query", blocks); out != "" {
		t.Fatalf("MCP output must not be replaced before a probe saw it honoured: %s", out)
	}
	if k := strings.Join(r.kinds(), ","); k != rwPreview+":would_rewrite" {
		t.Fatalf("events = %s", k)
	}

	// Probed: replaced, in the same block shape, for every shape Trove knows.
	for _, resp := range []any{
		blocks,
		[]any{map[string]any{"type": "text", "text": full}},
		full,
	} {
		r = previewRig(t, Snapshot{TroveRun: true}, Capability{Tool: "mcp", OutputRewrite: CapYes})
		out, _ := r.mcp("mcp__db__query", resp)
		if out == "" {
			t.Fatalf("%T: not previewed; log: %s", resp, r.hookLog())
		}
		got := string(parseOut(t, out).H.Output)
		if !previewRe.MatchString(got) || len(got) > 2200 {
			t.Fatalf("%T: %s", resp, got)
		}
		switch resp.(type) {
		case map[string]any:
			if !strings.HasPrefix(got, `{"content":[{"text":`) || !strings.Contains(got, `"type":"text"`) {
				t.Fatalf("shape lost: %s", got)
			}
		case []any:
			if !strings.HasPrefix(got, `[{"text":`) {
				t.Fatalf("shape lost: %s", got)
			}
		}
	}
}

func TestPreviewLeavesAlone(t *testing.T) {
	full := gitLog()
	withImage := map[string]any{"content": []any{map[string]any{"type": "text", "text": full}, map[string]any{"type": "image", "data": "AAAA", "mimeType": "image/png"}}}
	for _, c := range []struct {
		name string
		snap Snapshot
		run  func(r *rig) (string, int)
		want string // the events expected
	}{
		{"a tool on the passthrough list", Snapshot{TroveRun: true, PreviewPassthrough: []string{"mcp__db__*"}},
			func(r *rig) (string, int) { return r.mcp("mcp__db__query", full) }, ""},
		{"a result with an image in it", Snapshot{TroveRun: true},
			func(r *rig) (string, int) { return r.mcp("mcp__db__query", withImage) }, ""},
		{"Trove's own tools", Snapshot{TroveRun: true},
			func(r *rig) (string, int) { return r.mcp("mcp__trove__trove_run", full) }, ""},
		{"no MCP server to fetch from", Snapshot{},
			func(r *rig) (string, int) { return r.mcp("mcp__db__query", full) }, rwPreview + ":would_rewrite"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := previewRig(t, c.snap, Capability{Tool: "mcp", OutputRewrite: CapYes})
			if out, code := c.run(r); out != "" || code != 0 {
				t.Fatalf("must pass unchanged: code %d %q", code, out)
			}
			if k := strings.Join(r.kinds(), ","); k != c.want {
				t.Fatalf("events = %q, want %q", k, c.want)
			}
		})
	}
}

func TestGrepTextIsPreviewedOnlyOnceItsTextCaseWasProbed(t *testing.T) {
	full := gitLog() // a long content-mode search result

	// The "Grep" entry comes from a file-list probe: it says nothing about a text result, so shadow only.
	r := previewRig(t, Snapshot{TroveRun: true}, Capability{Tool: "Grep", OutputRewrite: CapYes})
	if out, _ := r.post("claude_post_grep.json", full, false); out != "" {
		t.Fatalf("Grep text must not be replaced on the file-list probe alone: %s", out)
	}
	if k := strings.Join(r.kinds(), ","); k != rwPreview+":would_rewrite" {
		t.Fatalf("events = %s", k)
	}

	// The text case was probed: replaced, every other response field kept.
	r = previewRig(t, Snapshot{TroveRun: true}, Capability{Tool: "GrepContent", OutputRewrite: CapYes})
	out, _ := r.post("claude_post_grep.json", full, false)
	if out == "" {
		t.Fatalf("not previewed; log: %s", r.hookLog())
	}
	var resp map[string]any
	if err := json.Unmarshal(parseOut(t, out).H.Output, &resp); err != nil {
		t.Fatal(err)
	}
	if c, _ := resp["content"].(string); !previewRe.MatchString(c) || len(c) > 2000 || resp["numLines"] != float64(400) {
		t.Fatalf("%v", resp)
	}
}
