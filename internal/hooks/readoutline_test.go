package hooks

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/recovery"
)

const rwOutline = config.OptimizerHookReadOutline

func bigSource(n int) string {
	var b strings.Builder
	b.WriteString("package big\n\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "func F%d(x int) int {\n", i)
		for j := 0; j < 20; j++ {
			fmt.Fprintf(&b, "\tx = x*%d + %d // a line of ordinary code that takes up some room\n", j+1, j)
		}
		b.WriteString("\treturn x\n}\n\n")
	}
	return b.String()
}

func outlineRig(t *testing.T, state string, probed bool, extra map[string]string) *rig {
	t.Helper()
	noClock(t)
	r := newRig(t, nil)
	states := map[string]string{rwOutline: state}
	for k, v := range extra {
		states[k] = v
	}
	r.snapshot(Snapshot{States: states, TroveRun: true})
	if probed {
		r.allow(Capability{Tool: "Read", OutputRewrite: CapYes})
	} else {
		r.allow(Capability{Tool: "Bash", OutputRewrite: CapYes})
	}
	r.start()
	return r
}

// postReadContent runs post_tool_read_record for a Read that returned content, in the shape recorded from
// Claude Code 2.1.318 (testdata/recorded/2.1.318/read_post.json).
func (r *rig) postReadContent(path, content string, offset, limit int) (string, int) {
	r.t.Helper()
	in := map[string]any{"file_path": path}
	if offset != 0 {
		in["offset"] = offset
	}
	if limit != 0 {
		in["limit"] = limit
	}
	ij, _ := json.Marshal(in)
	cj, _ := json.Marshal(content)
	return r.run(EventPostRead, r.input("claude_post_read_content.json", "{{READ_INPUT}}", string(ij),
		"{{CONTENT}}", strings.Trim(string(cj), `"`), "{{FILE}}", path, "{{LINES}}", fmt.Sprint(strings.Count(content, "\n"))))
}

func TestALargeReadIsOutlinedInTheReadShapeAndTheFullTextIsFetchable(t *testing.T) {
	r := outlineRig(t, "on", true, nil)
	p := r.file("big.go", bigSource(40))
	src := bigSource(40)
	out, code := r.postReadContent(p, src, 0, 0)
	if code != 0 || out == "" {
		t.Fatalf("code %d output %q; log: %s", code, out, r.hookLog())
	}
	o := parseOut(t, out)
	if o.H.Event != "PostToolUse" {
		t.Fatalf("event %q", o.H.Event)
	}
	var resp struct {
		Type string `json:"type"`
		File struct {
			Content    string `json:"content"`
			FilePath   string `json:"filePath"`
			NumLines   int    `json:"numLines"`
			StartLine  int    `json:"startLine"`
			TotalLines int    `json:"totalLines"`
		} `json:"file"`
	}
	if err := json.Unmarshal(o.H.Output, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Type != "text" || resp.File.FilePath != p || resp.File.StartLine != 1 || resp.File.TotalLines == 0 {
		t.Fatalf("every field but the content must be carried over: %s", o.H.Output)
	}
	if !strings.Contains(resp.File.Content, "func F0 3-25") || len(resp.File.Content) > len(src)/4 {
		t.Fatalf("content is not the outline: %q", resp.File.Content)
	}
	ids := optimize.OutlineIDs(resp.File.Content)
	if len(ids) != 1 {
		t.Fatalf("no recovery id in %q", resp.File.Content)
	}
	rec, err := recovery.Open(filepath.Join(r.dir, "recovery"), recovery.Options{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if got, _, err := rec.Get(ids[0], 1, 1<<20); err != nil || strings.TrimRight(got, "\n") != strings.TrimRight(src, "\n") {
		t.Fatalf("the full file must be fetchable: %v", err)
	}
	if k := strings.Join(r.kinds(), ","); k != rwOutline+":rewritten" {
		t.Fatalf("events = %s", k)
	}

	// A ranged read is the intended follow-up: not counted, not rewritten.
	if out, _ := r.postReadContent(p, "func F3(x int) int {\n", 70, 30); out != "" {
		t.Fatalf("a ranged read must pass: %q", out)
	}
	// The guardrail's numerator: fetching the whole file, then reading it whole, counts once.
	r.fetch(ids[0])
	if out, _ := r.postReadContent(p, src, 0, 0); out != "" {
		t.Fatalf("a full read after the outline must go through: %q", out)
	}
	if k := strings.Join(r.kinds(), ","); k != rwOutline+":rewritten,"+rwOutline+":extra_read" {
		t.Fatalf("events = %s", k)
	}
}

func TestAFullReReadAfterAnOutlineIsNotDeniedAndIsCounted(t *testing.T) {
	r := outlineRig(t, "on", true, map[string]string{config.OptimizerHookRepeatRead: "on"})
	src := bigSource(40)
	p := r.file("big.go", src)
	if out, _ := r.run(EventPreRead, r.input("claude_pre_read.json", "{{FILE}}", p)); out != "" {
		t.Fatalf("first read: %q", out)
	}
	if out, _ := r.postReadContent(p, src, 0, 0); out == "" {
		t.Fatalf("expected an outline; log: %s", r.hookLog())
	}
	if out, _ := r.run(EventPreRead, r.input("claude_pre_read.json", "{{FILE}}", p)); out != "" {
		t.Fatalf("the agent has only the outline; its full read must not be denied: %q", out)
	}
	if out, _ := r.postReadContent(p, src, 0, 0); out != "" {
		t.Fatalf("the second full read must go through whole: %q", out)
	}
	if k := strings.Join(r.kinds(), ","); k != rwOutline+":rewritten,"+rwOutline+":extra_read" {
		t.Fatalf("events = %s", k)
	}
}

func TestReadOutlineStaysShadowUntilTheProbeSaysReadOutputWorks(t *testing.T) {
	for _, c := range []struct {
		name   string
		state  string
		probed bool
	}{{"not probed", "on", false}, {"shadow", "shadow", true}} {
		t.Run(c.name, func(t *testing.T) {
			r := outlineRig(t, c.state, c.probed, nil)
			src := bigSource(40)
			p := r.file("big.go", src)
			if out, _ := r.postReadContent(p, src, 0, 0); out != "" {
				t.Fatalf("nothing may be rewritten: %q", out)
			}
			if k := strings.Join(r.kinds(), ","); k != rwOutline+":would_rewrite" {
				t.Fatalf("events = %s", k)
			}
		})
	}
}

func TestReadOutlineOffLeavesReadsAlone(t *testing.T) {
	r := outlineRig(t, "off", true, nil)
	src := bigSource(40)
	p := r.file("big.go", src)
	if out, _ := r.postReadContent(p, src, 0, 0); out != "" {
		t.Fatalf("%q", out)
	}
	if k := r.kinds(); len(k) != 0 {
		t.Fatalf("events = %v", k)
	}
}
