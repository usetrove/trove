package stress

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"strings"

	"github.com/usetrove/trove/internal/optimize"
)

// Everything in this file is synthetic. Tool output looks like real output in size and shape, but it is
// generated text: no real code, paths or secrets (rule 7 applies to recorded traffic, and none is used here).

// rng returns a PCG generator seeded from the run seed and a label, so each sample has its own stream and
// adding a sample does not reshuffle the others.
func rng(seed uint64, parts ...string) *rand.Rand {
	h := fnv.New64a()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return rand.New(rand.NewPCG(seed, h.Sum64())) //nolint:gosec // seeded synthetic data, reproducible by design, not security-sensitive
}

// SessionSpec is one point in the parameter space. Every field is something the realism layer reports on.
type SessionSpec struct {
	Name           string
	PrefixTokens   int     // base prompt: system prompt, tool definitions, project context
	Turns          int     // tool calls in the session
	Model          string  // pinned model ID, priced by the pricing table
	Mix            string  // tool mix: read-heavy, shell-heavy, search-heavy
	OutMedianBytes int     // median tool output size
	OutSigma       float64 // log-normal spread of output size (0 means every output is the median)
	ExpiryProb     float64 // chance a turn's cache entry has expired (0 = always hit, 1 = always cold)
	RepeatProb     float64 // chance a turn re-reads an earlier output verbatim
	NoiseProb      float64 // chance a shell output carries ANSI codes and CRLF line endings
	Profile        string  // journey-style profile label, used only for grouping
}

// bytesPerToken is the engine's estimate ratio. The realism layer's token counts are estimates for that reason.
const bytesPerToken = optimize.DefaultBytesPerToken

// genTurns builds the tool calls and outputs of one session from the spec and a seed.
func genTurns(spec SessionSpec, seed uint64) []turn {
	r := rng(seed, "turns", spec.Name)
	out := make([]turn, 0, spec.Turns)
	for i := 0; i < spec.Turns; i++ {
		if i > 0 && r.Float64() < spec.RepeatProb {
			prev := out[r.IntN(len(out))]
			out = append(out, turn{ID: fmt.Sprintf("toolu_%04d", i), Name: prev.Name, Input: prev.Input,
				Output: prev.Output, Reply: replyTokens(r)})
			continue
		}
		name, input := pickTool(spec.Mix, r, i)
		body := genOutput(name, spec, r)
		out = append(out, turn{ID: fmt.Sprintf("toolu_%04d", i), Name: name, Input: input,
			Output: body, Reply: replyTokens(r)})
	}
	return out
}

type turn struct {
	ID     string
	Name   string
	Input  string
	Output string
	Reply  int // the assistant's reply tokens for this turn (same in both arms)
}

func replyTokens(r *rand.Rand) int { return 80 + r.IntN(321) }

// pickTool draws a tool name and its input for a mix.
func pickTool(mix string, r *rand.Rand, i int) (string, string) {
	w := map[string][3]int{"read-heavy": {60, 20, 20}, "shell-heavy": {20, 70, 10}, "search-heavy": {25, 5, 70}}[mix]
	if w == [3]int{} {
		w = [3]int{34, 33, 33}
	}
	n := r.IntN(100)
	switch {
	case n < w[0]:
		return "Read", fmt.Sprintf(`{"file_path":"src/pkg%d/file%d.go"}`, i%7, i%53)
	case n < w[0]+w[1]:
		return "Bash", fmt.Sprintf(`{"command":"go test ./pkg%d/..."}`, i%11)
	default:
		return "Grep", fmt.Sprintf(`{"pattern":"symbol%d"}`, i%29)
	}
}

// genOutput returns synthetic tool output of a size drawn from the spec. Shell output may carry ANSI codes and
// CRLF endings; search output repeats lines, the way real search results do.
func genOutput(name string, spec SessionSpec, r *rand.Rand) string {
	median := spec.OutMedianBytes
	if median <= 0 {
		median = 400
	}
	size := median
	if spec.OutSigma > 0 {
		size = int(float64(median) * math.Exp(spec.OutSigma*normal(r)))
	}
	size = min(max(size, 16), 4<<20)

	var b strings.Builder
	eol := "\n"
	noisy := name == "Bash" && r.Float64() < spec.NoiseProb
	if noisy {
		eol = "\r\n"
	}
	for i := 0; b.Len() < size; i++ {
		switch name {
		case "Bash":
			line := fmt.Sprintf("ok   pkg%d  %d.%03ds%s", i%9, i%3, i%997, eol)
			if noisy && i%4 == 0 {
				line = "\x1b[32m" + line + "\x1b[0m"
			}
			b.WriteString(line)
		case "Grep":
			b.WriteString(fmt.Sprintf("src/pkg%d/file%d.go:%d: matches symbol%d%s", i%7, i%53, i*3, i%29, eol))
		default:
			b.WriteString(fmt.Sprintf("%5d  line %d of the file, nothing unusual here%s", i+1, i, eol))
		}
	}
	return b.String()[:size]
}

// normal draws a standard normal value (Box-Muller).
func normal(r *rand.Rand) float64 {
	u1 := r.Float64()
	for u1 == 0 {
		u1 = r.Float64()
	}
	return math.Sqrt(-2*math.Log(u1)) * math.Cos(2*math.Pi*r.Float64())
}

// jstr encodes s as a JSON string. Go's %q is not JSON, so it can emit escapes a parser rejects.
func jstr(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err) // a string always marshals
	}
	return string(b)
}

// pad returns n bytes of plain filler text, sized to match the spec's prefix.
func pad(n int) string {
	const unit = "Use the tool that matches the task and report results plainly. "
	if n <= 0 {
		return ""
	}
	return strings.Repeat(unit, n/len(unit)+1)[:n]
}

// buildBodies returns one request per turn. Request k holds the prompt and turns 0..k-1, because the agent
// resends its full history every turn; the engine then transforms each one from that history.
func buildBodies(spec SessionSpec, turns []turn) [][]byte {
	sysBytes := int(float64(spec.PrefixTokens) * bytesPerToken)
	head := fmt.Sprintf(`{"model":%s,"max_tokens":4096,"system":%s,"tools":[{"name":"agent_tools","description":%s,"input_schema":{"type":"object"}}],"messages":[`,
		jstr(spec.Model), jstr("You are a coding agent working in the user's repository. "+pad(sysBytes)), jstr(pad(2000)))
	frags := []string{fmt.Sprintf(`{"role":"user","content":[{"type":"text","text":%s}]}`,
		jstr("Continue the task from yesterday and keep the tests green."))}
	out := make([][]byte, 0, len(turns))
	for _, t := range turns {
		var b bytes.Buffer
		b.WriteString(head)
		b.WriteString(strings.Join(frags, ","))
		b.WriteString(`]}`)
		out = append(out, b.Bytes())
		frags = append(frags,
			fmt.Sprintf(`{"role":"assistant","content":[{"type":"text","text":%s},{"type":"tool_use","id":%s,"name":%s,"input":%s}]}`,
				jstr(replyText(t.Reply)), jstr(t.ID), jstr(t.Name), t.Input),
			fmt.Sprintf(`{"role":"user","content":[{"type":"tool_result","tool_use_id":%s,"content":%s}]}`,
				jstr(t.ID), jstr(t.Output)),
		)
	}
	return out
}

func replyText(tokens int) string {
	const s = "I will check the next file and compare it with the test expectation. "
	n := int(float64(tokens) * bytesPerToken)
	return strings.Repeat(s, n/len(s)+1)[:n]
}

// commonPrefix returns the length of the common byte prefix of a and b.
func commonPrefix(a, b []byte) int {
	n := min(len(a), len(b))
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return i
}

// tokensOf converts bytes to estimated tokens, rounding up.
func tokensOf(n int) int64 { return int64(math.Ceil(float64(n) / bytesPerToken)) }
