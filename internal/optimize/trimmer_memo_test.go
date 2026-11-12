package optimize

import (
	"context"
	"strings"
	"testing"
)

// The memoized trimmer must return exactly what the direct computation returns,
// on the first call and on every later call that hits the memo.
func TestTrimMemoMatchesDirectComputation(t *testing.T) {
	texts := []string{
		goTestOutput(200),
		goTestOutput(60) + "\n" + goTestOutput(80),
		strings.ReplaceAll(goTestOutput(200), "PASS", "\x1b[32mPASS\x1b[0m"),
		goTestOutput(200) + "[Trove: 3 lines trimmed; full output sha256:0123456789abcdef]\n",
		"plain text, not test output, long enough to be looked at " + strings.Repeat("filler line\n", 300),
	}
	s := Session{Key: "memo", Agent: "claude_code"}
	direct := OutputTrimmer{} // no memo
	memo := newOutputTrimmer()
	for round := 0; round < 3; round++ { // rounds 2 and 3 hit the memo
		for i, text := range texts {
			out := ToolOutput{Parts: []Part{{Text: text}}}
			want, wantRep := direct.TransformOutput(context.Background(), ToolCall{Name: "Bash"}, out, s)
			got, gotRep := memo.TransformOutput(context.Background(), ToolCall{Name: "Bash"}, out, s)
			if got.Parts[0].Text != want.Parts[0].Text {
				t.Fatalf("round %d text %d: memoized output differs from direct output", round, i)
			}
			if len(gotRep.Originals) != len(wantRep.Originals) {
				t.Fatalf("round %d text %d: originals differ", round, i)
			}
			for k := range wantRep.Originals {
				if gotRep.Originals[k] != wantRep.Originals[k] {
					t.Fatalf("round %d text %d: original %d differs", round, i, k)
				}
			}
		}
	}
}

// The request path must agree too, including a text that is long enough to memoize.
func TestTrimMemoRequestPathMatchesDirect(t *testing.T) {
	raw := goTestOutput(200)
	body := req{msgs: []msg{user(toolResult("t1", raw))}}.body()
	s := Session{Key: "memo-req", Agent: "claude_code", TroveRun: true}
	r1, err := ParseRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := OutputTrimmer{}.TransformSession(context.Background(), r1, s)
	for i := 0; i < 3; i++ {
		r, _ := ParseRequest(body)
		got, _ := newOutputTrimmer().TransformSession(context.Background(), r, s)
		if string(got.Raw) != string(want.Raw) {
			t.Fatalf("attempt %d: memoized request differs from direct request", i)
		}
	}
}
