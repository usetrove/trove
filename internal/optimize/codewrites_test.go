package optimize

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/technique"
)

func codeFile(t *testing.T, lines int) (string, string) {
	t.Helper()
	var b strings.Builder
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&b, "line %d: some code that does a thing\n", i)
	}
	p := filepath.Join(t.TempDir(), "big.go")
	if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return p, b.String()
}

func writeCall(path, content string) ToolCall {
	in, _ := json.Marshal(map[string]string{"file_path": path, "content": content})
	return ToolCall{Name: "Write", Input: in}
}

func editCall(path string) ToolCall {
	in, _ := json.Marshal(map[string]any{"file_path": path, "old_string": "a", "new_string": "b"})
	return ToolCall{Name: "Edit", Input: in}
}

func codeSess(l *memLedger, mode string) Session {
	return Session{Ledger: l, Mode: mode, Now: time.Unix(1_800_000_000, 0)}
}

func TestLineChange(t *testing.T) {
	cases := []struct {
		a, b           string
		added, removed int
	}{
		{"x\ny\nz", "x\ny\nz", 0, 0},
		{"x\ny\nz", "x\nY\nz", 1, 1},
		{"x\ny\nz", "z\ny\nx", 0, 0}, // order-blind: can only undercount
		{"x\ny", "x\ny\nw", 1, 0},
		{"x\nx\ny", "x\ny", 0, 1},
	}
	for _, c := range cases {
		_, _, add, rem, _ := LineChange([]byte(c.a), []byte(c.b))
		if add != c.added || rem != c.removed {
			t.Errorf("%q -> %q: added %d removed %d, want %d %d", c.a, c.b, add, rem, c.added, c.removed)
		}
	}
}

func TestWriteToEditDeniesASmallRewriteOnceThenLetsTheSameWriteThrough(t *testing.T) {
	path, old := codeFile(t, 400)
	newContent := strings.Replace(old, "line 7: some code", "line 7: other code", 1)
	l := newMemLedger()
	o := WriteToEdit{}
	_, rep := o.TransformInput(context.Background(), writeCall(path, newContent), codeSess(l, config.StateOn))
	d := rep.Decision
	if d == nil || d.Kind != KindBlocked || !strings.Contains(d.Message, "Use Edit for targeted changes") || !strings.Contains(d.Message, "400-line") {
		t.Fatalf("a one-line change to a 400-line file must be denied: %+v", rep)
	}
	if d.Bytes <= int64(len(newContent))/2 || d.Tokens <= 0 {
		t.Errorf("saving should be most of the file: %+v", d)
	}
	_, rep = o.TransformInput(context.Background(), writeCall(path, newContent), codeSess(l, config.StateOn))
	if rep.Decision == nil || rep.Decision.Kind != KindOverridden || rep.Decision.IsDeny() {
		t.Fatalf("the same Write again must go through and be counted: %+v", rep)
	}
	other := strings.Replace(old, "line 9: some code", "line 9: other code", 1)
	if _, rep = o.TransformInput(context.Background(), writeCall(path, other), codeSess(l, config.StateOn)); rep.Decision != nil {
		t.Fatalf("a file is denied at most once per session: %+v", rep)
	}
}

func TestWriteToEditShadowOnlyCountsOncePerFile(t *testing.T) {
	path, old := codeFile(t, 400)
	newContent := old + "one more line\n"
	l := newMemLedger()
	_, rep := WriteToEdit{}.TransformInput(context.Background(), writeCall(path, newContent), codeSess(l, config.StateShadow))
	if rep.Decision == nil || rep.Decision.Kind != KindWouldBlock || rep.Decision.Message != "" {
		t.Fatalf("shadow must only count: %+v", rep)
	}
	if _, rep = (WriteToEdit{}).TransformInput(context.Background(), writeCall(path, newContent), codeSess(l, config.StateShadow)); rep.Decision != nil {
		t.Fatalf("shadow counts a file once: %+v", rep)
	}
}

func TestWriteToEditLeavesOtherWritesAlone(t *testing.T) {
	small, smallOld := codeFile(t, 100)
	big, bigOld := codeFile(t, 400)
	var rewritten strings.Builder
	for i, line := range strings.Split(bigOld, "\n") {
		if i%3 == 0 {
			line = "changed " + line
		}
		rewritten.WriteString(line + "\n")
	}
	cases := []struct {
		name string
		call ToolCall
	}{
		{"small file", writeCall(small, smallOld+"x\n")},
		{"large change", writeCall(big, rewritten.String())},
		{"new file", writeCall(filepath.Join(t.TempDir(), "new.go"), bigOld)},
		{"relative path", writeCall("big.go", bigOld+"x\n")},
		{"not a Write", ToolCall{Name: "Read", Input: json.RawMessage(`{"file_path":"` + filepath.ToSlash(big) + `"}`)}},
	}
	for _, c := range cases {
		if _, rep := (WriteToEdit{}).TransformInput(context.Background(), c.call, codeSess(newMemLedger(), config.StateOn)); rep.Decision != nil {
			t.Errorf("%s: %+v", c.name, rep)
		}
	}
}

func TestWriteToEditDoesNotDenyWhatItCannotRemember(t *testing.T) {
	path, old := codeFile(t, 400)
	l := newMemLedger()
	l.saveErr = os.ErrPermission
	_, rep := WriteToEdit{}.TransformInput(context.Background(), writeCall(path, old+"x\n"), codeSess(l, config.StateOn))
	if rep.Decision != nil || rep.Err == "" {
		t.Fatalf("an unsaved deny could loop, so it must not be made: %+v", rep)
	}
}

func TestEditBatchNotesTheThirdEditToAFileInOneTurn(t *testing.T) {
	l := newMemLedger()
	o := EditBatch{}
	var kinds []string
	for i := 0; i < 5; i++ {
		_, rep := o.TransformInput(context.Background(), editCall("/src/a.go"), codeSess(l, config.StateOn))
		if rep.Decision != nil {
			kinds = append(kinds, fmt.Sprintf("%d:%s", i+1, rep.Decision.Kind))
			if rep.Decision.Message != editBatchNote || rep.Decision.IsDeny() {
				t.Errorf("note: %+v", rep.Decision)
			}
		}
	}
	if strings.Join(kinds, ",") != "3:advised" {
		t.Fatalf("the note goes on the third Edit only: %v", kinds)
	}
	// Another file has its own count; a new prompt starts the count again.
	if _, rep := o.TransformInput(context.Background(), editCall("/src/b.go"), codeSess(l, config.StateOn)); rep.Decision != nil {
		t.Errorf("first Edit to another file: %+v", rep)
	}
	l.prompts++
	for i := 0; i < 2; i++ {
		if _, rep := o.TransformInput(context.Background(), editCall("/src/a.go"), codeSess(l, config.StateOn)); rep.Decision != nil {
			t.Errorf("edit %d of a new turn: %+v", i+1, rep)
		}
	}
	_, rep := o.TransformInput(context.Background(), editCall("/src/a.go"), codeSess(l, config.StateShadow))
	if rep.Decision == nil || rep.Decision.Kind != KindWouldAdvise || rep.Decision.Message != "" || rep.Decision.Bytes <= 0 {
		t.Fatalf("shadow counts the third Edit of the new turn with the bytes spent: %+v", rep)
	}
}

// The registry declares what each message costs per use (rule 15); the texts must stay within it.
func TestCodeWriteMessagesFitTheirDeclaredOverhead(t *testing.T) {
	const wrapper = 68 // Claude Code's system-reminder wrapper, as for hook_routine_steer
	for name, text := range map[string]string{
		config.OptimizerHookWriteToEdit: fmt.Sprintf(writeToEditFormat, 999999, 10),
		config.OptimizerHookEditBatch:   editBatchNote,
	} {
		tq, ok := technique.Get(name)
		if !ok || tq.UseOverhead == nil {
			t.Fatalf("%s has no per-use overhead in the registry", name)
		}
		if got := EstTokens(int64(len(text) + wrapper)); got > int64(*tq.UseOverhead) {
			t.Errorf("%s: %d tokens, declared %d", name, got, *tq.UseOverhead)
		}
		if tq.Tier != technique.T2 {
			t.Errorf("%s is %v, want T2", name, tq.Tier)
		}
	}
}
