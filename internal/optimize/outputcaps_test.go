package optimize

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/config"
)

func TestOutputCapBytes(t *testing.T) {
	for _, tc := range []struct {
		tool string
		want int64
	}{
		{"Bash", 30000},
		{"PowerShell", 30000},
		{"mcp__github__search", 50000},
		{"Read", int64(25000 * DefaultBytesPerToken)},
		{"Grep", 0}, // no verified cap: nothing is limited
		{"", 0},
	} {
		got, ok := outputCapBytes(tc.tool, DefaultBytesPerToken)
		if tc.want == 0 && ok || tc.want != 0 && (!ok || got != tc.want) {
			t.Errorf("%q: got %d,%v want %d", tc.tool, got, ok, tc.want)
		}
	}
}

func TestCappedSaving(t *testing.T) {
	for _, tc := range []struct {
		name          string
		tool          string
		before, after int
		want          int
	}{
		{"under the cap is unchanged", "Bash", 20000, 5000, 15000},
		// Past 30,000 characters Claude Code saves Bash output to a file and shows a 2,000-character preview.
		{"agent would have shown a 2k preview, trimmed to 5k: nothing saved", "Bash", 100000, 5000, 0},
		{"agent would have shown a 2k preview, trimmed to 1k", "Bash", 100000, 1000, 1000},
		{"both over the cap: nothing was saved", "Bash", 100000, 40000, 0},
		{"MCP past 50,000 characters becomes a path: nothing to save", "mcp__x__y", 60000, 3000, 0},
		{"MCP under its limit counts whole", "mcp__x__y", 40000, 3000, 37000},
		{"no cap for an unknown tool", "Grep", 100000, 5000, 95000},
		{"growth is never a saving", "Bash", 1000, 2000, 0},
	} {
		if got := cappedSaving(tc.tool, tc.before, tc.after, DefaultBytesPerToken); got != tc.want {
			t.Errorf("%s: got %d want %d", tc.name, got, tc.want)
		}
	}
}

// The engine must not count output the agent would have truncated anyway.
func TestToolOutputSavingsStopAtAgentCap(t *testing.T) {
	e := toolEngine(t, 100, toolStub{name: "x", outFn: shorten})
	in := ToolOutput{Parts: []Part{{Text: strings.Repeat("a", 100000)}}}
	_, reps := e.TransformToolOutput(context.Background(), ToolCall{Name: "Bash"}, in, stateOf(config.StateShadow))
	// 100,000 -> 5 bytes, but the agent would only have shown its 2,000-character preview.
	if len(reps) != 1 || reps[0].BytesRemoved != 2000-5 {
		t.Fatalf("Bash savings = %+v, want %d bytes", reps, 2000-5)
	}
	_, reps = e.TransformToolOutput(context.Background(), ToolCall{Name: "Grep"}, in, stateOf(config.StateShadow))
	if len(reps) != 1 || reps[0].BytesRemoved != 100000-5 {
		t.Fatalf("an uncapped tool keeps its full figure: %+v", reps)
	}
}

func TestReadLimitEstimateStopsAtReadCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")
	const size = 400_000 // well past the 25,000-token read cap
	if err := os.WriteFile(path, []byte(strings.Repeat("x", size)), 0o600); err != nil {
		t.Fatal(err)
	}
	call := ToolCall{Name: "Read", Input: []byte(`{"file_path":` + quote(path) + `}`)}
	_, rep := ReadLimit{}.TransformInput(context.Background(), call, Session{Mode: config.StateShadow})
	capB, _ := outputCapBytes("Read", DefaultBytesPerToken)
	limit := capB - int64(ReadLimitLines*bytesPerLine) // the note is netted off too
	if rep.Decision == nil || rep.Decision.Bytes <= 0 || rep.Decision.Bytes > limit {
		t.Fatalf("estimate %+v must not exceed what the agent would have received (%d)", rep.Decision, limit)
	}
	if rep.BytesRemoved > int(limit) {
		t.Fatalf("BytesRemoved %d above %d", rep.BytesRemoved, limit)
	}
}

func quote(s string) string {
	b := []byte(`"`)
	for _, r := range s {
		if r == '\\' || r == '"' {
			b = append(b, '\\')
		}
		b = append(b, string(r)...)
	}
	return string(append(b, '"'))
}
