package optimize

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/config"
)

// goTestOutput is a long `go test -v` log with one failing test and a panic
// trace in the middle.
func goTestOutput(passing int) string {
	var sb strings.Builder
	sb.WriteString("=== RUN   TestSetup\n")
	for i := 0; i < passing; i++ {
		fmt.Fprintf(&sb, "=== RUN   TestCase%03d\n--- PASS: TestCase%03d (0.00s)\n", i, i)
	}
	sb.WriteString("=== RUN   TestBroken\n")
	sb.WriteString("--- FAIL: TestBroken (0.01s)\n")
	sb.WriteString("    broken_test.go:42: expected 3, got 4\n")
	sb.WriteString("panic: runtime error: index out of range [5] with length 3\n\n")
	sb.WriteString("goroutine 7 [running]:\n")
	sb.WriteString("pkg.Broken(...)\n\t/src/pkg/broken.go:17 +0x1d\n")
	for i := passing; i < passing+40; i++ {
		fmt.Fprintf(&sb, "=== RUN   TestAfter%03d\n--- PASS: TestAfter%03d (0.00s)\n", i, i)
	}
	sb.WriteString("FAIL\tpkg\t0.123s\nFAIL\n")
	return sb.String()
}

func TestTrimOutputKeepsFailuresAndStackTraces(t *testing.T) {
	in := goTestOutput(150)
	out, changed := trimOutput(in)
	if !changed || len(out) >= len(in)/2 {
		t.Fatalf("expected a large reduction: %d -> %d", len(in), len(out))
	}
	for _, must := range []string{
		"=== RUN   TestSetup", // head
		"--- FAIL: TestBroken (0.01s)",
		"broken_test.go:42: expected 3, got 4",
		"panic: runtime error: index out of range [5] with length 3",
		"goroutine 7 [running]:",
		"/src/pkg/broken.go:17 +0x1d",
		"FAIL\tpkg\t0.123s", // tail summary
	} {
		if !strings.Contains(out, must) {
			t.Errorf("dropped %q\n%s", must, out)
		}
	}
	if !strings.Contains(out, "lines trimmed; full output sha256:") {
		t.Errorf("no marker: %s", out)
	}
	// The marker names the exact number of lines removed.
	removed := 0
	for _, l := range strings.Split(out, "\n") {
		var n int
		if _, err := fmt.Sscanf(l, "[Trove: %d lines trimmed;", &n); err == nil {
			removed += n
		}
	}
	if got := len(strings.Split(in, "\n")) - (len(strings.Split(out, "\n")) - countMarkerLines(out)); got != removed {
		t.Errorf("markers claim %d lines trimmed, actually removed %d", removed, got)
	}
}

func countMarkerLines(s string) int {
	n := 0
	for _, l := range strings.Split(s, "\n") {
		if trimMarker.MatchString(l) {
			n++
		}
	}
	return n
}

func TestTrimOutputLeavesOtherTextAlone(t *testing.T) {
	file := strings.Repeat("func helper() int { return 42 }\n", 400) // large, but source code
	long := strings.Repeat("=== RUN   T\n--- PASS: T (0.00s)\n", 5)  // test output, but small
	for name, in := range map[string]string{"source file": file, "small output": long, "empty": ""} {
		if out, changed := trimOutput(in); changed || out != in {
			t.Errorf("%s was modified", name)
		}
	}
}

func TestTrimOutputIsDeterministicAndIdempotent(t *testing.T) {
	for _, passing := range []int{80, 150, 400} {
		in := goTestOutput(passing)
		a, _ := trimOutput(in)
		b, _ := trimOutput(in)
		if a != b {
			t.Fatalf("passing=%d: not deterministic", passing)
		}
		if again, changed := trimOutput(a); changed || again != a {
			t.Fatalf("passing=%d: trimming trimmed output changed it", passing)
		}
	}
}

func TestOutputTrimmerProperties(t *testing.T) {
	big := goTestOutput(200)
	applies := [][]byte{
		req{msgs: []msg{user(toolResult("t1", big))}}.body(),
		req{msgs: []msg{user(toolResultBlocks("t1", big, "tail"))}}.body(),
	}
	noops := [][]byte{
		req{msgs: []msg{user(toolResult("t1", "short"))}}.body(),
		req{msgs: []msg{user(text(big))}}.body(), // not a tool result
		req{msgs: []msg{user(toolResult("t1", strings.Repeat("plain prose line\n", 800)))}}.body(),
	}
	checkProperties(t, OutputTrimmer{}, Session{}, applies, noops)
}

func TestOutputTrimmerCannotBeSwitchedOn(t *testing.T) {
	body := req{msgs: []msg{user(toolResult("t1", goTestOutput(200)))}}.body()
	for _, state := range []string{config.StateOn, config.StateShadow} {
		cfg := config.Default()
		cfg.Optimizers[config.OptimizerOutputTrimmer] = state // bypasses config validation on purpose
		e := New(Options{Config: cfg, Getenv: func(string) string { return "" }})
		res := e.Run(context.Background(), input(body))
		if res.Changed || string(res.Body) != string(body) {
			t.Fatalf("%s: the trimmer must never change a forwarded request", state)
		}
		if len(res.Applied) != 1 || res.Applied[0].State != config.StateShadow || res.Applied[0].TokensRemoved == 0 {
			t.Errorf("%s: shadow estimate missing: %+v", state, res.Applied)
		}
	}
}
