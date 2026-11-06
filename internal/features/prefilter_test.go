package features

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/optimize"
)

// fragments are pieces of real runner, build and trace output, plus near misses.
var fragments = []string{
	"--- PASS: TestX (0.00s)", "--- FAIL: TestY", "ok  \tpkg\t0.1s", "ok", "PASS", "PASSED", "FAIL", "FAILED", "FAIL\tpkg",
	"test a::b ... ok", "test a::b ... FAILED", "tests/test_a.py ....", "x.py .F.", "✓ works", "✔ works", "✗ broke", "✘ broke", "× broke", "● fail",
	"Error: boom", "error: boom", "error[E0308]: mismatched", "ERROR: x", "[ERROR] build", "npm ERR! code 1", "panic: oops", "fatal: nope",
	"Traceback (most recent call last):", "AssertionError: x", "java.lang.NullPointerException", "SomeException", "thread 'main' panicked at src/x.rs:3",
	"E   assert 1 == 2", "  at com.x.Y.z(Y.java:42)", "  at Object.<anonymous> (/a/b.js:10:5)", "  File \"a.py\", line 3, in f",
	"pkg/a.go:17 +0x1d", "    src/lib.rs:12", "\tmain.go:9", "plain prose with no markers", "", "   ", "\t--- PASS: Indented", "\r", "ünïcödé line",
	"warning: unused", "Errors: 0", "no failures here", "an error occurred", "x: y: z", "see http://example.com:8080/path",
	"  indented text", "FAILURE is not FAILED", "exception handling docs", "PASSING", "okay then", "testing 1 2 3", "t",
}

func TestPrefiltersAgreeWithTheRegexes(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	check := func(l string) {
		t.Helper()
		if got, want := isPass(l), passRe.MatchString(l); got != want {
			t.Fatalf("isPass(%q) = %v, regex says %v", l, got, want)
		}
		if got, want := isFailure(l), optimize.IsFailureLine(l); got != want {
			t.Fatalf("isFailure(%q) = %v, regex says %v", l, got, want)
		}
		if got, want := isFrame(l), frameRe.MatchString(l); got != want {
			t.Fatalf("isFrame(%q) = %v, regex says %v", l, got, want)
		}
	}
	for _, f := range fragments {
		check(f)
	}
	seps := []string{"", " ", "\t", "  ", ": ", "\n", "\r"}
	for i := 0; i < 60000; i++ {
		var sb strings.Builder
		for n := rng.Intn(4) + 1; n > 0; n-- {
			sb.WriteString(seps[rng.Intn(len(seps))])
			sb.WriteString(fragments[rng.Intn(len(fragments))])
		}
		l := sb.String()
		if rng.Intn(10) == 0 && len(l) > 2 { // cut it somewhere, to hit partial matches
			l = l[:rng.Intn(len(l))]
		}
		check(l)
	}
	long := "error: " + strings.Repeat("x", 500) // over the failure scan's line cap
	check(long)
}

func TestFnv64MatchesTheStandardLibraryHash(t *testing.T) {
	// FNV-1a 64 of "" and "a" from the published test vectors.
	for in, want := range map[string]uint64{"": 0xcbf29ce484222325, "a": 0xaf63dc4c8601ec8c} {
		if got := fnv64(in); got != want {
			t.Errorf("fnv64(%q) = %x, want %x", in, got, want)
		}
	}
}
