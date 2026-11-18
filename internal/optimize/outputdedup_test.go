package optimize

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
)

type memLedger struct {
	m       map[string]ReadEntry
	prompts int
	saveErr error
}

func newMemLedger() *memLedger { return &memLedger{m: map[string]ReadEntry{}, prompts: 1} }

func (l *memLedger) Get(k string) (ReadEntry, bool) { e, ok := l.m[k]; return e, ok }
func (l *memLedger) Put(k string, e ReadEntry)      { l.m[k] = e }
func (l *memLedger) Delete(k string)                { delete(l.m, k) }
func (l *memLedger) Prompts() int                   { return l.prompts }
func (l *memLedger) Save() error                    { return l.saveErr }

func dedupOut(texts ...string) ToolOutput {
	o := ToolOutput{}
	for _, t := range texts {
		o.Parts = append(o.Parts, Part{Text: t})
	}
	return o
}

func TestOutputDedupCollapsesOnlyARepeatOfTheSameBytes(t *testing.T) {
	big := strings.Repeat("main.go:12: some search hit\n", 40)
	other := strings.Repeat("util.go:3: another hit\n", 40)
	small := "ok\n"
	l := newMemLedger()
	now := time.Unix(1_800_000_000, 0)
	s := Session{Ledger: l, Now: now, Mode: config.StateOn}
	d := OutputDedup{}
	call := ToolCall{Name: "Grep"}

	res, rep := d.TransformOutput(context.Background(), call, dedupOut(big, small), s)
	if rep.Name != config.OptimizerHookOutputDedup || len(rep.Originals) != 0 || res.Parts[0].Text != big || res.Parts[1].Text != small {
		t.Fatalf("the first output must pass unchanged: %+v %+v", res, rep)
	}

	l.prompts = 3
	s.Now = now.Add(5 * time.Minute)
	res, rep = d.TransformOutput(context.Background(), call, dedupOut(big, small, other), s)
	if len(rep.Originals) != 1 || rep.Originals[0] != big {
		t.Fatalf("the repeat must be handed over for storage, byte for byte: %+v", rep)
	}
	note := res.Parts[0].Text
	if !strings.Contains(note, "prompt #1") || !strings.Contains(note, "about 5 min ago") {
		t.Fatalf("note should name the first copy: %q", note)
	}
	if ids := DedupIDs(note); len(ids) != 1 || ids[0] != recoveryID(big) {
		t.Fatalf("marker must name the full output's recovery id: %v", ids)
	}
	if ids := TrimmedIDs(note); len(ids) != 0 {
		t.Fatalf("a dedup marker must not look like a trim marker: %v", ids)
	}
	if res.Parts[1].Text != small || res.Parts[2].Text != other {
		t.Fatalf("a small part and a new part must pass unchanged: %+v", res.Parts)
	}

	// The same bytes much later still count: there is no time-based expiry.
	s.Now = now.Add(30 * 24 * time.Hour)
	if _, rep := d.TransformOutput(context.Background(), call, dedupOut(big), s); len(rep.Originals) != 1 {
		t.Fatalf("no expiry expected: %+v", rep)
	}
}

func TestOutputDedupHashesTheRawOutput(t *testing.T) {
	raw := strings.Repeat("\x1b[32mPASS\x1b[0m line\n", 40)
	l := newMemLedger()
	s := Session{Ledger: l, Now: time.Unix(1_800_000_000, 0), Raw: func(int) string { return raw }}
	d := OutputDedup{}
	d.TransformOutput(context.Background(), ToolCall{Name: "Bash"}, dedupOut("normalized copy"+strings.Repeat(".", 300)), s)
	_, rep := d.TransformOutput(context.Background(), ToolCall{Name: "Bash"}, dedupOut("normalized copy"+strings.Repeat(".", 300)), s)
	if len(rep.Originals) != 1 || rep.Originals[0] != raw {
		t.Fatalf("the stored original must be the tool's own output: %+v", rep)
	}
}

func TestOutputDedupLeavesOtherToolsErrorsAndNoLedgerAlone(t *testing.T) {
	big := strings.Repeat("x", 500)
	cases := []struct {
		name string
		call string
		out  ToolOutput
		l    ReadLedger
	}{
		{"Read waits for its probe", "Read", dedupOut(big), newMemLedger()},
		{"an MCP tool", "mcp__x__y", dedupOut(big), newMemLedger()},
		{"an error output", "Bash", ToolOutput{Parts: []Part{{Text: big}}, IsError: true}, newMemLedger()},
		{"no ledger", "Bash", dedupOut(big), nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := Session{Ledger: c.l, Now: time.Unix(1_800_000_000, 0)}
			for i := 0; i < 2; i++ {
				res, rep := OutputDedup{}.TransformOutput(context.Background(), ToolCall{Name: c.call}, c.out, s)
				if rep.Name != "" || res.Parts[0].Text != big {
					t.Fatalf("must not apply: %+v", rep)
				}
			}
		})
	}
}

func TestOutputDedupFailsOpenWhenTheLedgerCannotBeSaved(t *testing.T) {
	l := newMemLedger()
	l.saveErr = errors.New("disk full")
	big := strings.Repeat("y", 500)
	res, rep := OutputDedup{}.TransformOutput(context.Background(), ToolCall{Name: "Glob"}, dedupOut(big), Session{Ledger: l, Now: time.Unix(1, 0)})
	if rep.Err == "" || res.Parts[0].Text != big {
		t.Fatalf("%+v", rep)
	}
}

func TestOutputDedupIsClampedToShadowWithoutRecovery(t *testing.T) {
	if got := EffectiveStateFor(config.OptimizerHookOutputDedup, config.StateOn, false); got != config.StateShadow {
		t.Fatal(got)
	}
	if got := EffectiveStateFor(config.OptimizerHookOutputDedup, config.StateOn, true); got != config.StateOn {
		t.Fatal(got)
	}
}
