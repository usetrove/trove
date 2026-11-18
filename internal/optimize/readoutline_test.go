package optimize

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
)

// bigGoFile is a Go source file well past ReadOutlineMinBytes with n functions.
func bigGoFile(n int) string {
	var b strings.Builder
	b.WriteString("package big\n\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "// F%d does a thing.\nfunc F%d(x int) int {\n", i, i)
		for j := 0; j < 20; j++ {
			fmt.Fprintf(&b, "\tx = x*%d + %d // a line of ordinary code that takes up some room\n", j+1, j)
		}
		b.WriteString("\treturn x\n}\n\n")
	}
	return b.String()
}

func readCall(path string, offset, limit int) ToolCall {
	m := map[string]any{"file_path": path}
	if offset != 0 {
		m["offset"] = offset
	}
	if limit != 0 {
		m["limit"] = limit
	}
	b, _ := json.Marshal(m)
	return ToolCall{Name: "Read", Input: b}
}

func outlineSess(l ReadLedger) Session {
	return Session{Ledger: l, Now: time.Unix(1_800_000_000, 0), Mode: config.StateOn}
}

func TestReadOutlineReplacesALargeFullReadWithItsOutline(t *testing.T) {
	src := bigGoFile(40)
	if len(src) < ReadOutlineMinBytes {
		t.Fatalf("fixture too small: %d", len(src))
	}
	l := newMemLedger()
	res, rep := ReadOutline{}.TransformOutput(context.Background(), readCall("/p/big.go", 0, 0), dedupOut(src), outlineSess(l))
	if rep.Name != config.OptimizerHookReadOutline || len(rep.Originals) != 1 || rep.Originals[0] != src {
		t.Fatalf("the full file must be handed over for storage, byte for byte: %+v", rep)
	}
	got := res.Parts[0].Text
	if len(got) >= len(src)/4 {
		t.Fatalf("the outline should be much smaller: %d of %d bytes", len(got), len(src))
	}
	for _, want := range []string{"func F0 4-26", "func F39 ", "offset and limit", "trove_fetch_full"} {
		if !strings.Contains(got, want) {
			t.Fatalf("outline lacks %q:\n%s", want, got)
		}
	}
	if ids := OutlineIDs(got); len(ids) != 1 || ids[0] != recoveryID(src) {
		t.Fatalf("the marker must name the full file's recovery id: %v", ids)
	}
	if len(TrimmedIDs(got)) != 0 || len(DedupIDs(got)) != 0 {
		t.Fatal("an outline marker must not look like a trim or dedup marker")
	}
	// Rule 5: the same file gives byte-identical output.
	again, _ := ReadOutline{}.TransformOutput(context.Background(), readCall("/p/big.go", 0, 0), dedupOut(src), outlineSess(newMemLedger()))
	if again.Parts[0].Text != got {
		t.Fatal("the outline must be deterministic")
	}
}

func TestReadOutlineLeavesEverythingElseAlone(t *testing.T) {
	big := bigGoFile(40)
	plain := strings.Repeat("just some prose without any declarations in it at all\n", 1200)
	cases := []struct {
		name string
		call ToolCall
		out  ToolOutput
		l    ReadLedger
	}{
		{"a small file", readCall("/p/small.go", 0, 0), dedupOut(bigGoFile(3)), newMemLedger()},
		{"a ranged read", readCall("/p/big.go", 100, 50), dedupOut(big), newMemLedger()},
		{"a limited read", readCall("/p/big.go", 0, 400), dedupOut(big), newMemLedger()},
		{"a language the outliner does not know", readCall("/p/notes.txt", 0, 0), dedupOut(plain), newMemLedger()},
		{"another tool", ToolCall{Name: "Bash", Input: json.RawMessage(`{"command":"cat big.go"}`)}, dedupOut(big), newMemLedger()},
		{"an error", readCall("/p/big.go", 0, 0), ToolOutput{Parts: []Part{{Text: big}}, IsError: true}, newMemLedger()},
		{"no ledger", readCall("/p/big.go", 0, 0), dedupOut(big), nil},
		{"a paged read", ToolCall{Name: "Read", Input: json.RawMessage(`{"file_path":"/p/a.pdf","pages":"1-3"}`)}, dedupOut(big), newMemLedger()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, rep := ReadOutline{}.TransformOutput(context.Background(), c.call, c.out, outlineSess(c.l))
			if rep.Name != "" || res.Parts[0].Text != c.out.Parts[0].Text {
				t.Fatalf("must not apply: %+v", rep)
			}
		})
	}
}

func TestAFullReadAfterAnOutlineGoesThroughAndIsCountedOnce(t *testing.T) {
	src := bigGoFile(40)
	l := newMemLedger()
	MarkOutlined(l, "/p/big.go", recoveryID(src), 1, 1_800_000_000)
	call := readCall("/p/big.go", 0, 0)

	res, rep := ReadOutline{}.TransformOutput(context.Background(), call, dedupOut(src), outlineSess(l))
	if res.Parts[0].Text != src || len(rep.Originals) != 0 {
		t.Fatal("the agent asked for the whole file again: it must get it")
	}
	if rep.Name != config.OptimizerHookReadOutline || rep.Decision == nil || rep.Decision.Kind != KindExtraRead {
		t.Fatalf("the extra read must be counted: %+v", rep)
	}
	res, rep = ReadOutline{}.TransformOutput(context.Background(), call, dedupOut(src), outlineSess(l))
	if res.Parts[0].Text != src || rep.Name != "" {
		t.Fatalf("a file is outlined once a session and counted once: %+v", rep)
	}
	// A ranged read is how an outline is meant to be used, so it is never counted.
	if _, rep := (ReadOutline{}).TransformOutput(context.Background(), readCall("/p/other.go", 10, 20), dedupOut(src), outlineSess(l)); rep.Name != "" {
		t.Fatalf("%+v", rep)
	}
}

func TestFetchingAnOutlinedFileCountsOnceWithAFullReRead(t *testing.T) {
	src := bigGoFile(40)
	id := recoveryID(src)
	l := newMemLedger()
	MarkOutlined(l, "/p/big.go", id, 1, 1)
	if !CountOutlineFetch(l, id) {
		t.Fatal("the first fetch of an outlined file is an extra read")
	}
	if CountOutlineFetch(l, id) {
		t.Fatal("counted twice")
	}
	_, rep := ReadOutline{}.TransformOutput(context.Background(), readCall("/p/big.go", 0, 0), dedupOut(src), outlineSess(l))
	if rep.Decision != nil {
		t.Fatalf("a full read after the fetch must not count again: %+v", rep)
	}
	if CountOutlineFetch(newMemLedger(), id) {
		t.Fatal("an id that was never outlined is not ours")
	}
}

func TestReadOutlineFailsOpenWhenTheLedgerCannotBeSaved(t *testing.T) {
	src := bigGoFile(40)
	l := newMemLedger()
	MarkOutlined(l, "/p/big.go", recoveryID(src), 1, 1)
	l.saveErr = errors.New("disk full")
	res, rep := ReadOutline{}.TransformOutput(context.Background(), readCall("/p/big.go", 0, 0), dedupOut(src), outlineSess(l))
	if res.Parts[0].Text != src || rep.Decision != nil {
		t.Fatalf("%+v", rep)
	}
}

func TestReadOutlineIsClampedToShadowWithoutRecovery(t *testing.T) {
	if got := EffectiveStateFor(config.OptimizerHookReadOutline, config.StateOn, false); got != config.StateShadow {
		t.Fatal(got)
	}
	if got := EffectiveStateFor(config.OptimizerHookReadOutline, config.StateOn, true); got != config.StateOn {
		t.Fatal(got)
	}
}

func TestRepeatReadLetsTheFullReadAfterAnOutlineThrough(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big.go")
	if err := os.WriteFile(p, []byte(bigGoFile(40)), 0o600); err != nil {
		t.Fatal(err)
	}
	l := newMemLedger()
	s := Session{Ledger: l, Now: time.Unix(1_800_000_000, 0), Mode: config.StateOn}
	call := readCall(p, 0, 0)
	RepeatRead{}.TransformOutput(context.Background(), call, dedupOut("x"), s)
	MarkOutlined(l, p, "0123456789abcdef", 1, 1)
	if _, rep := (RepeatRead{}).TransformInput(context.Background(), call, s); rep.Decision != nil {
		t.Fatalf("the agent only saw an outline, so its full read must not be denied: %+v", rep.Decision)
	}
}
