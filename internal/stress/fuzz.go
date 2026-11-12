package stress

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/optimize"
)

// fuzzCase is one hostile input. Body is a whole request; for tool-level checks, Output and Input are the
// text a tool returned and the arguments it was called with.
type fuzzCase struct {
	family string
	body   []byte
	output string
	input  json.RawMessage
}

// envelope wraps one tool result in a minimal Messages request. result is the raw JSON for the tool_result
// content, so a family can put bytes that are not valid JSON there on purpose.
func envelope(result string, toolInput string) []byte {
	if toolInput == "" {
		toolInput = `{"command":"go test ./..."}`
	}
	return []byte(`{"model":"claude-sonnet-5-5","max_tokens":4096,"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"keep the tests green"}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":` + toolInput + `}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":` + result + `}]}]}`)
}

// corpusFamilies returns the hostile families. Every family is deterministic for a seed and variant.
func corpusFamilies(seed uint64, variant int) []fuzzCase {
	r := rng(seed, "fuzz", fmt.Sprint(variant))
	scale := 1 + r.IntN(4) // 1x to 4x, so variants differ in size
	lines := func(n int, eol string) string {
		var b strings.Builder
		for i := 0; b.Len() < n; i++ {
			fmt.Fprintf(&b, "ok pkg%d %d%s", i%13, i, eol)
		}
		return b.String()[:n]
	}
	var out []fuzzCase
	add := func(family, result, output string, body []byte, input json.RawMessage) {
		if body == nil {
			body = envelope(result, "")
		}
		out = append(out, fuzzCase{family: family, body: body, output: output, input: input})
	}

	huge := lines((1<<20)*scale, "\n")
	add("huge-output", jstr(huge), huge, nil, nil)

	long := strings.Repeat("x", (1<<20)*scale)
	add("very-long-line", jstr(long), long, nil, nil)

	crlf := strings.ReplaceAll(lines(200_000*scale, "\n"), "\n", "\r\n") + "\r" + "lone-cr\n"
	add("crlf-mixed", jstr(crlf), crlf, nil, nil)

	ansi := strings.Repeat("\x1b[31mred\x1b[0m\x1b[K", 20_000*scale)
	add("ansi-flood", jstr(ansi), ansi, nil, nil)

	bt := strings.Repeat("a", 100_000*scale) + "!"
	add("regex-backtrack", jstr(bt), bt, nil, nil)
	nearMiss := strings.Repeat("ERROR: ", 30_000*scale) + "ERROR"
	add("regex-near-miss", jstr(nearMiss), nearMiss, nil, nil)

	// Raw bytes that are not valid UTF-8 and contain a NUL: this is not valid JSON, so the engine must forward it.
	raw := []byte(`{"model":"claude-sonnet-5-5","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"`)
	for i := 0; i < 4096*scale; i++ {
		raw = append(raw, byte(0x80+i%128), 0x00)
	}
	raw = append(raw, []byte(`"}]}]}`)...)
	add("binary-invalid-utf8", "", string(raw), raw, nil)

	// Valid JSON whose string holds invalid UTF-8 bytes: the engine must still produce valid JSON or forward.
	utf := []byte(`{"model":"claude-sonnet-5-5","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"`)
	for i := 0; i < 4096*scale; i++ {
		utf = append(utf, byte(0xC3+i%2))
	}
	utf = append(utf, []byte(`"}]}]}`)...)
	add("invalid-utf8-json-string", "", string(utf), utf, nil)

	depth := 10_000 * scale
	deepInput := json.RawMessage(strings.Repeat("[", depth) + strings.Repeat("]", depth))
	add("deep-nesting", `"ok"`, "ok", envelope(`"ok"`, string(deepInput)), deepInput)

	many := make([]string, 0, 500*scale)
	for i := 0; i < 500*scale; i++ {
		many = append(many, fmt.Sprintf(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t%d","content":"same output"}]}`, i))
	}
	add("many-identical-results", "", "", []byte(`{"model":"claude-sonnet-5-5","messages":[`+strings.Join(many, ",")+`]}`), nil)

	add("unbalanced-pairs", "", "",
		[]byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"orphan","content":"x"}]},{"role":"assistant","content":[{"type":"tool_use","id":"lonely","name":"Read","input":{}}]}]}`), nil)

	// Malformed and empty requests. Each of these must come back unchanged.
	add("empty-body", "", "", []byte{}, nil)
	add("not-json", "", "", []byte("this is not json {"), nil)
	add("truncated", "", "", envelope(jstr("half"), "")[:90], nil)
	add("wrong-types", "", "", []byte(`{"messages":"x","model":5}`), nil)
	add("null-messages", "", "", []byte(`{"messages":null}`), nil)
	add("duplicate-keys", "", "", []byte(`{"messages":[],"messages":[{"role":"user","content":"x"}]}`), nil)
	add("json-null", "", "", []byte(`null`), nil)
	add("ten-kb-of-spaces", "", "", []byte(`{"messages":[]`+strings.Repeat(" ", 10_000)+`}`), nil)
	return out
}

// oversize is one request just over the engine's body limit. It is built once, not per variant.
func oversize() fuzzCase {
	pad := strings.Repeat("y", optimize.MaxBody+1)
	return fuzzCase{family: "over-max-body", body: []byte(`{"messages":[],"pad":"` + pad + `"}`)}
}

// fuzz is layer 2. Pass/fail only: it reports no savings.
func (e *env) fuzz() LayerResult {
	lr := LayerResult{Name: LayerFuzz}
	cases := []fuzzCase{oversize()}
	for v := 0; v < e.p.fuzzVariants; v++ {
		cases = append(cases, corpusFamilies(e.opts.Seed, v)...)
	}
	names := allNames()
	for _, c := range cases {
		e.record(&lr, c.family, e.fuzzOne(c, names), c.body)
	}
	lr.Notes = append(lr.Notes, fmt.Sprintf("%d hostile inputs, each run through the full pipeline and through every optimizer alone; per-input time cap %s",
		len(cases), e.p.capPerCase))
	finish(&lr)
	return lr
}

// fuzzOne runs one hostile input through the engine and every optimizer alone, and through the tool-level
// entry points. It returns every violation found.
func (e *env) fuzzOne(c fuzzCase, names []string) []Violation {
	var vs []Violation

	// Whole pipeline on.
	res, took, rec := e.transformOnce(c.body, names, "full")
	if took > e.p.capPerCase {
		vs = append(vs, Violation{"time_cap", fmt.Sprintf("took %s, over the %s cap", took.Round(time.Millisecond), e.p.capPerCase)})
	}
	vs = append(vs, rec...)
	vs = append(vs, CheckRequest(c.body, res.Body, declaredOverheadTokens(res))...)
	if len(res.Failures) > 0 && !bytes.Equal(res.Body, c.body) {
		vs = append(vs, Violation{InvFailOpenOnError, "a failed optimizer still changed the request"})
	}
	// Determinism: a second, independent engine gives the same bytes.
	again, _, _ := e.transformOnce(c.body, names, "full-again")
	if !bytes.Equal(again.Body, res.Body) {
		vs = append(vs, Violation{InvDeterministic, "the same input gave different bytes on a second run"})
	}
	// Idempotence: transforming the output again changes nothing.
	if res.Changed {
		second, _, _ := e.transformOnce(res.Body, names, "full-second")
		if !bytes.Equal(second.Body, res.Body) {
			vs = append(vs, Violation{InvIdempotent, "a second pass changed a request the first pass had already transformed"})
		}
	}

	// Every optimizer alone, so a fault in one cannot hide behind the others.
	for _, n := range names {
		single, took, srec := e.transformOnce(c.body, []string{n}, "single-"+n)
		if took > e.p.capPerCase {
			vs = append(vs, Violation{"time_cap", fmt.Sprintf("%s took %s, over the cap", n, took.Round(time.Millisecond))})
		}
		vs = append(vs, srec...)
		vs = append(vs, CheckRequest(c.body, single.Body, declaredOverheadTokens(single))...)
		if len(single.Failures) > 0 && !bytes.Equal(single.Body, c.body) {
			vs = append(vs, Violation{InvFailOpenOnError, n + " failed but still changed the request"})
		}
	}

	// Tool-level entry points on the raw text and arguments. They must return, never panic, and keep the
	// part count.
	if c.output != "" {
		call := optimize.ToolCall{Name: "Bash", Input: json.RawMessage(`{"command":"x"}`)}
		eng := newEngine(names, nil, PricingDate)
		outp, _ := eng.TransformToolOutput(context.Background(), call, optimize.ToolOutput{Parts: []optimize.Part{{Text: c.output}}},
			optimize.Session{Key: "fuzz-tool", Agent: "claude_code", State: func(string) string { return "on" }})
		if len(outp.Parts) != 1 {
			vs = append(vs, Violation{InvFailOpenOnError, "tool output changed its number of parts"})
		}
	}
	if len(c.input) > 0 {
		eng := newEngine(names, nil, PricingDate)
		call := optimize.ToolCall{Name: "Read", Input: c.input}
		got, _ := eng.TransformToolInput(context.Background(), call, optimize.Session{Key: "fuzz-in", Agent: "claude_code", State: func(string) string { return "on" }})
		if !json.Valid(c.input) && !bytes.Equal(got.Input, c.input) {
			vs = append(vs, Violation{InvFailOpen, "an invalid tool input was changed instead of passed through"})
		}
	}
	return vs
}

// transformOnce runs the engine with the named optimizers on, over one body. It returns the result, how long
// it took, and any recovery violation: a stored blob that is not one of the body's tool outputs.
func (e *env) transformOnce(body []byte, names []string, key string) (optimize.Result, time.Duration, []Violation) {
	store := &memStore{}
	eng := newCustomEngine(names, store, PricingDate)
	start := time.Now()
	res := eng.TransformRequest(context.Background(), optimize.RequestIn{
		Provider: "anthropic", Path: "/v1/messages", Header: http.Header{}, Body: body,
	}, optimize.Session{Key: "fuzz-" + key, Agent: customAgent, TroveRun: true})
	took := time.Since(start)
	return res, took, CheckRecovery(body, store.snapshot())
}
