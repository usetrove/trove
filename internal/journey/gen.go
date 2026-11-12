package journey

import (
	"fmt"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"strings"
)

// Turn is one tool call and its output in a simulated session. The text is
// synthetic: it has the shape and size of real tool output, never real code.
type Turn struct {
	Kind         Kind
	ToolUseID    string
	ToolName     string
	ToolInput    string
	Output       string
	OutputTokens int // the assistant's reply tokens for this turn (same in both arms)
	Repeat       bool
}

// Session is one agent session. A subagent, when present, runs its own history
// and is billed as separate requests; its outputs never enter the parent's history.
type Session struct {
	Index    int
	Turns    []Turn
	Subagent *Session
}

// newRand returns a PCG generator seeded from the run seed and a label, so each
// profile and session draws from its own stream and adding a profile does not
// reshuffle the others.
func newRand(seed uint64, parts ...string) *rand.Rand {
	h := fnv.New64a()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return rand.New(rand.NewPCG(seed, h.Sum64())) //nolint:gosec // seeded simulation, reproducible by design, not security-sensitive
}

// genSession builds session idx of profile p for the run seed.
func genSession(p Profile, seed uint64, idx int) Session {
	r := newRand(seed, "session", p.ID, fmt.Sprint(idx))
	s := Session{Index: idx, Turns: genTurns(p, r, p.TurnsMin, p.TurnsMax)}
	if r.IntN(100) < p.Subagents {
		sr := newRand(seed, "subagent", p.ID, fmt.Sprint(idx))
		hi := p.TurnsMax / 3
		if hi < 4 {
			hi = 4
		}
		sub := Session{Index: idx, Turns: genTurns(p, sr, 3, hi)}
		s.Subagent = &sub
	}
	return s
}

func genTurns(p Profile, r *rand.Rand, lo, hi int) []Turn {
	n := lo
	if hi > lo {
		n = lo + r.IntN(hi-lo+1)
	}
	turns := make([]Turn, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("toolu_%04d", i)
		// A repeat re-reads something the session already read: same kind,
		// same input, same content.
		if prev, ok := pickPriorRead(turns, r, p.RepeatRead); ok {
			t := prev
			t.ToolUseID = id
			t.Repeat = true
			t.OutputTokens = replyTokens(r)
			turns = append(turns, t)
			continue
		}
		k := pickKind(p.Mix, r)
		t := Turn{
			Kind:         k,
			ToolUseID:    id,
			ToolName:     toolNameFor(k),
			ToolInput:    toolInputFor(k, i, r),
			Output:       genOutput(r, k),
			OutputTokens: replyTokens(r),
		}
		if k == KindReadSmall || k == KindReadLarge {
			// A file's content is a fixture of its path: reading it again gives the
			// same bytes, as on disk. The hook-level simulation writes these files.
			path := ReadPath(k, r.IntN(60))
			t.ToolInput = fmt.Sprintf(`{"file_path":%s}`, jstr(path))
			t.Output = genOutput(newRand(0, "file", path), k)
		}
		turns = append(turns, t)
	}
	return turns
}

func replyTokens(r *rand.Rand) int { return 80 + r.IntN(321) }

// ReadPath is the relative path of the i-th fixture file of a read kind. The
// hook-level simulation writes each one to disk so the real Read optimizers can
// check it.
func ReadPath(k Kind, i int) string {
	return fmt.Sprintf("internal/%s/file%03d.go", k, i)
}

func pickPriorRead(turns []Turn, r *rand.Rand, pct int) (Turn, bool) {
	if pct <= 0 || r.IntN(100) >= pct {
		return Turn{}, false
	}
	var reads []Turn
	for _, t := range turns {
		if t.Kind == KindReadSmall || t.Kind == KindReadLarge {
			reads = append(reads, t)
		}
	}
	if len(reads) == 0 {
		return Turn{}, false
	}
	return reads[r.IntN(len(reads))], true
}

func pickKind(mix map[Kind]int, r *rand.Rand) Kind {
	total := 0
	for _, k := range Kinds {
		total += mix[k]
	}
	if total == 0 {
		return KindBashSmall
	}
	x := r.IntN(total)
	for _, k := range Kinds {
		x -= mix[k]
		if x < 0 {
			return k
		}
	}
	return KindBashSmall
}

func toolNameFor(k Kind) string {
	switch k {
	case KindReadSmall, KindReadLarge:
		return "Read"
	case KindGrep:
		return "Grep"
	case KindStackTrace, KindBuildLog, KindTestVerbose, KindBashSmall:
		return "Bash"
	}
	return "Bash"
}

func toolInputFor(k Kind, i int, r *rand.Rand) string {
	switch k {
	case KindReadSmall, KindReadLarge:
		return fmt.Sprintf(`{"file_path":"internal/service/file%03d.go"}`, r.IntN(400))
	case KindGrep:
		return fmt.Sprintf(`{"pattern":"handler%02d"}`, r.IntN(40))
	case KindTestVerbose:
		return `{"command":"go test -v ./..."}`
	case KindStackTrace:
		return fmt.Sprintf(`{"command":"./run-case %d"}`, i)
	case KindBuildLog:
		return `{"command":"make build"}`
	}
	return `{"command":"ls"}`
}

// genOutput returns synthetic tool output of the kind's typical size. The shape
// (repeated lines, ANSI codes, timestamps, progress bars, repeated frames) is
// what the optimizers are designed to act on, so it must look like the real thing.
func genOutput(r *rand.Rand, k Kind) string {
	var b strings.Builder
	target := kindTarget(r, k)
	n := 0
	for b.Len() < target {
		n++
		if (k == KindTestVerbose || k == KindStackTrace || k == KindBashSmall || k == KindBuildLog) && r.IntN(100) < blankShare {
			b.WriteString("\n") // a repeated blank line, as command output has
		}
		switch k {
		case KindReadSmall, KindReadLarge:
			fmt.Fprintf(&b, "func handler%03d(ctx context.Context, n int) error {\n\tif n < %d {\n\t\treturn nil\n\t}\n\treturn wrap(ctx, n)\n}\n\n", n%500, n%97)
		case KindGrep:
			fmt.Fprintf(&b, "internal/service/handler%02d.go:%d:\treturn wrap(err, \"step %d\")\n", n%40, n, n%12) // every match is on its own line, so lines are unique, as in real grep output
		case KindTestVerbose:
			fmt.Fprintf(&b, "--- PASS: TestCase%04d (0.%03ds)\n", n, n%997)
			if n%5 == 0 {
				fmt.Fprintf(&b, "2026-11-08T10:%02d:%02d.%03dZ DEBUG cache hit key=%08x\n", n%60, n%60, n%1000, n*2654435761)
			}
		case KindStackTrace:
			if n == 1 {
				fmt.Fprintf(&b, "panic: runtime error: index out of range [%d] with length %d\n\ngoroutine 1 [running]:\n", 3+r.IntN(5), 3)
			}
			fmt.Fprintf(&b, "github.com/acme/app/pkg.(*Service).process(0x%x, 0x%x)\n\t/src/pkg/service.go:%d +0x%x\n", n*16, n*8, 40+n%30, n*4)
		case KindBashSmall:
			fmt.Fprintf(&b, "ok  \tacme/app/pkg/mod%02d\t0.%03ds\n", n%30, n%997)
		case KindBuildLog:
			fmt.Fprintf(&b, "\r[%d%%] Building object %d of 900\n", n%100, n)
			if n%7 == 0 {
				fmt.Fprintf(&b, "warning: deprecated API used in module%02d\n", n%30)
			}
		}
	}
	if k == KindReadSmall || k == KindReadLarge {
		return numberLines(b.String())
	}
	return b.String()
}

// numberLines puts a line number before every line, as Claude Code's Read tool
// does (cat -n style). Real Read output therefore has no repeated lines.
func numberLines(s string) string {
	lines := strings.Split(s, "\n")
	var b strings.Builder
	for i, l := range lines {
		if i == len(lines)-1 && l == "" {
			break
		}
		fmt.Fprintf(&b, "%6d\t%s\n", i+1, l)
	}
	return b.String()
}

// kindSize is each kind's output size: a log-normal with this median and 90th
// percentile, in bytes. These are the generator's fitted parameters: they were
// chosen so the tool classes match the real session aggregates (see calibrate.go).
var kindSize = map[Kind][2]int{
	KindReadSmall:   {1300, 2800},
	KindReadLarge:   {5000, 9000},
	KindGrep:        {1000, 4400},
	KindTestVerbose: {6000, 20000},
	KindStackTrace:  {3000, 8000},
	KindBashSmall:   {500, 800},
	KindBuildLog:    {9000, 20000},
}

// blankShare is the chance (percent) that a line of command output is followed by a
// blank line. Real command output repeats blank lines, which is most of its repeated-line share.
const blankShare = 18

// kindTarget draws an output size for kind k.
func kindTarget(r *rand.Rand, k Kind) int {
	mp, ok := kindSize[k]
	if !ok {
		return 1000
	}
	med, p90 := float64(mp[0]), float64(mp[1])
	sigma := math.Log(p90/med) / 1.2816 // z for the 90th percentile
	return int(med * math.Exp(sigma*normal(r)))
}

// normal draws a standard normal value (Box-Muller). Only the generator's
// seeded source is used, so the draw is deterministic.
func normal(r *rand.Rand) float64 {
	u1 := 1 - r.Float64() // in (0, 1], so the log is finite
	u2 := r.Float64()
	return math.Sqrt(-2*math.Log(u1)) * math.Cos(2*math.Pi*u2)
}
