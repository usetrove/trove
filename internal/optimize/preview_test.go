package optimize

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// longOutput is n lines of routine text with the given lines inserted at their indexes.
func longOutput(n int, at map[int]string) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if l, ok := at[i]; ok {
			b.WriteString(l)
		} else {
			fmt.Fprintf(&b, "src/pkg/file_%04d.go: routine line with some ordinary content %d", i, i)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func TestOutputPreviewKeepsWhatMatters(t *testing.T) {
	text := longOutput(400, map[int]string{
		150: "error: connection refused while dialing db:5432",
		260: "warning: deprecated flag --legacy used",
		399: "Summary: 3 files changed, 12 insertions(+), 4 deletions(-)",
	})
	out := ToolOutput{Parts: []Part{{Text: text}}}
	res, rep := OutputPreview{}.TransformOutput(context.Background(), ToolCall{Name: "Bash"}, out, Session{})
	pv := res.Parts[0].Text
	if pv == text {
		t.Fatal("a 400-line output was not previewed")
	}
	if len(pv) > previewBudget {
		t.Fatalf("preview is %d bytes, over the %d-byte budget", len(pv), previewBudget)
	}
	for _, want := range []string{
		"src/pkg/file_0000.go", // head
		"error: connection refused",
		"warning: deprecated flag",
		"Summary: 3 files changed", // last line
		"output_id=" + recoveryID(text),
		"[… ",
	} {
		if !strings.Contains(pv, want) {
			t.Errorf("preview lacks %q:\n%s", want, pv)
		}
	}
	if len(rep.Originals) != 1 || rep.Originals[0] != text {
		t.Fatal("the full output must be handed to the engine to store before the preview leaves")
	}
	if ids := PreviewIDs(pv); len(ids) != 1 || ids[0] != recoveryID(text) {
		t.Fatalf("PreviewIDs = %v", ids)
	}
	// Pure: the same output always gives the same bytes (PRINCIPLES.md rule 5).
	again, _ := OutputPreview{}.TransformOutput(context.Background(), ToolCall{Name: "Bash"}, out, Session{})
	if again.Parts[0].Text != pv {
		t.Fatal("the preview is not deterministic")
	}
	// A preview is far below the floor, so it is never previewed again.
	twice, _ := OutputPreview{}.TransformOutput(context.Background(), ToolCall{Name: "Bash"}, res, Session{})
	if twice.Parts[0].Text != pv {
		t.Fatal("previewing a preview changed it")
	}
}

func TestOutputPreviewLeavesAlone(t *testing.T) {
	big := longOutput(400, nil)
	for _, tc := range []struct {
		name string
		tool string
		out  ToolOutput
		s    Session
	}{
		{"an error result", "Bash", ToolOutput{Parts: []Part{{Text: big}}, IsError: true}, Session{}},
		{"a short output", "Bash", ToolOutput{Parts: []Part{{Text: longOutput(50, nil)}}}, Session{}},
		{"Read", "Read", ToolOutput{Parts: []Part{{Text: big}}}, Session{}},
		{"Glob", "Glob", ToolOutput{Parts: []Part{{Text: big}}}, Session{}},
		{"Trove's own fetch tool", "mcp__trove__trove_fetch_full", ToolOutput{Parts: []Part{{Text: big}}}, Session{}},
		{"Trove's own tools in the plugin", "mcp__plugin_trove_trove__trove_run", ToolOutput{Parts: []Part{{Text: big}}}, Session{}},
		{"a passthrough tool by name", "Bash", ToolOutput{Parts: []Part{{Text: big}}}, Session{Passthrough: []string{"Bash"}}},
		{"a passthrough tool by prefix", "mcp__db__query", ToolOutput{Parts: []Part{{Text: big}}}, Session{Passthrough: []string{"mcp__db__*"}}},
		{"Bash past its inline limit (the agent saves it to a file)", "Bash", ToolOutput{Parts: []Part{{Text: longOutput(600, nil)}}}, Session{}},
		{"MCP past 50,000 characters", "mcp__db__query", ToolOutput{Parts: []Part{{Text: longOutput(900, nil)}}}, Session{}},
		{"output the trimmer already shortened", "Bash", ToolOutput{Parts: []Part{{Text: big + "[Trove: 40 lines trimmed; full output sha256:0123456789abcdef]\n"}}}, Session{}},
	} {
		res, rep := OutputPreview{}.TransformOutput(context.Background(), ToolCall{Name: tc.tool}, tc.out, tc.s)
		if res.Parts[0].Text != tc.out.Parts[0].Text || len(rep.Originals) != 0 {
			t.Errorf("%s: was changed", tc.name)
		}
	}
}

func TestOutputPreviewTools(t *testing.T) {
	big := longOutput(400, nil)
	for _, tool := range []string{"Bash", "PowerShell", "Grep", "mcp__db__query", "mcp__plugin_x_y__get"} {
		res, _ := OutputPreview{}.TransformOutput(context.Background(), ToolCall{Name: tool}, ToolOutput{Parts: []Part{{Text: big}}}, Session{})
		if res.Parts[0].Text == big {
			t.Errorf("%s: a long output was not previewed", tool)
		}
	}
	// Grep has no verified inline limit, so a large text result is still previewed.
	huge := longOutput(900, nil)
	res, _ := OutputPreview{}.TransformOutput(context.Background(), ToolCall{Name: "Grep"}, ToolOutput{Parts: []Part{{Text: huge}}}, Session{})
	if res.Parts[0].Text == huge {
		t.Error("Grep: a 60 KB result was not previewed")
	}
}

// Bash stdout and stderr are separate parts; a small stderr (where errors usually are) is kept whole.
func TestOutputPreviewKeepsSmallStderr(t *testing.T) {
	stderr := "fatal: could not read Username\n"
	out := ToolOutput{Parts: []Part{{Text: longOutput(400, nil)}, {Text: stderr}}}
	res, rep := OutputPreview{}.TransformOutput(context.Background(), ToolCall{Name: "Bash"}, out, Session{})
	if res.Parts[1].Text != stderr || len(rep.Originals) != 1 {
		t.Fatalf("stderr changed or wrong originals: %q, %d", res.Parts[1].Text, len(rep.Originals))
	}
}

func TestOutputPreviewLongLines(t *testing.T) {
	// One very long JSON line, and multi-byte text that must not be cut inside a rune.
	json := `{"rows":[` + strings.Repeat(`{"name":"ünïcödé","v":1},`, 600) + `{}]}`
	for _, text := range []string{json, strings.Repeat("héllo wörld ", 50) + "\n" + longOutput(300, nil)} {
		res, _ := OutputPreview{}.TransformOutput(context.Background(), ToolCall{Name: "mcp__db__query"}, ToolOutput{Parts: []Part{{Text: text}}}, Session{})
		pv := res.Parts[0].Text
		if pv == text || len(pv) > previewBudget || !utf8.ValidString(pv) {
			t.Fatalf("bad preview (%d bytes, valid UTF-8 %v):\n%s", len(pv), utf8.ValidString(pv), pv)
		}
		if !strings.Contains(pv, " bytes]") {
			t.Errorf("a clipped line must say how much was cut:\n%s", pv)
		}
	}
}

func TestPassthrough(t *testing.T) {
	for _, tc := range []struct {
		list []string
		tool string
		want bool
	}{
		{nil, "Bash", false},
		{[]string{"Bash"}, "Bash", true},
		{[]string{"Bash"}, "BashOutput", false},
		{[]string{"mcp__gh__*"}, "mcp__gh__get_file", true},
		{[]string{"mcp__gh__*"}, "mcp__ghx__get", false},
		{[]string{"*"}, "anything", true},
	} {
		if got := Passthrough(tc.list, tc.tool); got != tc.want {
			t.Errorf("Passthrough(%v, %q) = %v", tc.list, tc.tool, got)
		}
	}
}

func TestReadsFile(t *testing.T) {
	for cmd, want := range map[string]bool{
		"cat internal/x.go":              true,
		"sed -n '1,200p' internal/x.go":  true,
		"cd /repo && sed -n 1,50p a.go":  true,
		"cd /repo; head -100 a.log":      true,
		"/usr/bin/tail -n 300 build.log": true,
		"git show HEAD~1":                true,
		"git diff --stat":                true,
		"Get-Content .\app.log":          true,
		"go test ./... | tail -50":       false,
		"git log --oneline":              false,
		"grep -rn foo .":                 false,
		"npm install":                    false,
		"":                               false,
		"cd /repo":                       false,
	} {
		if got := readsFile(cmd); got != want {
			t.Errorf("readsFile(%q) = %v, want %v", cmd, got, want)
		}
	}
	// A file read through Bash passes through whole.
	big := longOutput(400, nil)
	in, _ := json.Marshal(map[string]string{"command": "sed -n '1,400p' a.go"})
	res, _ := OutputPreview{}.TransformOutput(context.Background(), ToolCall{Name: "Bash", Input: in}, ToolOutput{Parts: []Part{{Text: big}}}, Session{})
	if res.Parts[0].Text != big {
		t.Fatal("a file the agent printed on purpose was previewed")
	}
}
