package noise

import (
	"math/rand"
	"strconv"
	"strings"
	"testing"
)

func TestClean(t *testing.T) {
	long := strings.Repeat("x", 40)
	tests := []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"plain", "a\nb\nc", "a\nb\nc"},
		{"ansi colors", "\x1b[31mFAIL\x1b[0m test", "FAIL test"},
		{"ansi osc title", "\x1b]0;title\x07done", "done"},
		{"cr redraw keeps last frame", "10%\r50%\rdone\nnext", "done\nnext"},
		{"crlf endings untouched", "a\r\nb\r\n", "a\r\nb\r\n"},
		{"progress group keeps last", "Downloading 10% [####====]\nDownloading 50% [########]\nDownloading 100% [########]\nok", "Downloading 100% [########]\nok"},
		{"single progress line kept", "Downloading 50% [########]\nok", "Downloading 50% [########]\nok"},
		{"braille spinner", "⠋ building\n⠙ building\n⠹ building\nbuilt", "⠹ building\nbuilt"},
		{"run collapsed", strings.Repeat(long+"\n", 5) + "end", long + "\n(repeated 5 times)\nend"},
		{"short run not worth it", "a\na\na\nb", "a\na\na\nb"},
		{"two identical kept", long + "\n" + long + "\nend", long + "\n" + long + "\nend"},
		{"non-adjacent repeats kept", long + "\nx\n" + long + "\nx\n" + long, long + "\nx\n" + long + "\nx\n" + long},
		{"bullets are not spinners", "- one\n- two\n- three", "- one\n- two\n- three"},
		{"table bars kept", "|\n|\n-", "|\n|\n-"},
		{"crlf run", strings.Repeat(long+"\r\n", 4) + "end", long + "\r\n(repeated 4 times)\r\nend"},
		{"marker line is never collapsed", "(repeated 9 times)\n(repeated 9 times)\n(repeated 9 times)", "(repeated 9 times)\n(repeated 9 times)\n(repeated 9 times)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Clean(tt.in)
			if got != tt.want {
				t.Errorf("Clean(%q)\n got %q\nwant %q", tt.in, got, tt.want)
			}
			if again := Clean(got); again != got {
				t.Errorf("not idempotent: %q -> %q", got, again)
			}
		})
	}
}

// pool mixes ordinary content, repeated lines, ANSI-wrapped lines, frames and
// the literal marker text, so random documents hit every rule.
var pool = []string{
	"alpha", "beta", "gamma line with more text in it to be long enough to collapse", "", "  indented",
	"- item", "|", "ok  \tpkg/a\t0.1s", "(repeated 4 times)", "\x1b[32mgreen\x1b[0m",
	"Downloading 10% [####====]", "Downloading 90% [########]", "⠋ working", "⠙ working",
	"carriage\rredraw", "tail\r", "same same same same same same same same same same same same",
}

func randomDoc(rng *rand.Rand) string {
	var sb strings.Builder
	n := rng.Intn(40)
	for i := 0; i < n; i++ {
		line := pool[rng.Intn(len(pool))]
		reps := 1
		if rng.Intn(4) == 0 {
			reps = 1 + rng.Intn(9)
		}
		for k := 0; k < reps; k++ {
			sb.WriteString(line)
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

func TestCleanProperties(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 3000; i++ {
		in := randomDoc(rng)
		out := Clean(in)
		if out != Clean(in) {
			t.Fatalf("not deterministic for %q", in)
		}
		if again := Clean(out); again != out {
			t.Fatalf("not idempotent:\n in %q\nout %q\nagain %q", in, out, again)
		}
		if len(out) > len(in) {
			t.Fatalf("output longer than input: %q -> %q", in, out)
		}
	}
}

// Every distinct line of the input that is not a frame must survive, once ANSI
// codes and redraws are resolved the way Clean does.
func TestCleanNeverDropsAUniqueLine(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	for i := 0; i < 3000; i++ {
		in := randomDoc(rng)
		out := Clean(in)
		have := map[string]bool{}
		for _, l := range strings.Split(out, "\n") {
			have[l] = true
		}
		for _, l := range strings.Split(in, "\n") {
			l = lastRedrawFrame(ANSI.ReplaceAllString(l, ""))
			if IsProgress(l) || IsSpinnerFrame(l) {
				continue // frames are allowed to go; only the last of a group is kept
			}
			if !have[l] {
				t.Fatalf("line %q was dropped\n in %q\nout %q", l, in, out)
			}
		}
	}
}

// Collapsed runs must be reconstructible: the count in the marker equals the
// number of identical lines that were removed plus the one kept.
func TestCleanCountsAreExact(t *testing.T) {
	line := strings.Repeat("y", 30)
	for n := 3; n < 60; n++ {
		in := strings.Repeat(line+"\n", n) + "end"
		out := Clean(in)
		if want := line + "\n(repeated " + strconv.Itoa(n) + " times)\nend"; out != want {
			t.Fatalf("n=%d: got %q", n, out)
		}
	}
}

func TestMeasure(t *testing.T) {
	if Measure("a\nb") != 0 {
		t.Error("clean text must measure 0")
	}
	if got := Measure("\x1b[31mx\x1b[0m"); got != 9 {
		t.Errorf("Measure = %d, want 9", got)
	}
}

func BenchmarkCleanPlain(b *testing.B) {
	in := strings.Repeat("some ordinary build output line number 12345\n", 2000)
	b.SetBytes(int64(len(in)))
	for i := 0; i < b.N; i++ {
		Clean(in)
	}
}
