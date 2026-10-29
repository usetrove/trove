package optimize

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/config"
)

// TestRealPipelineFailsOpenOnDamagedRequests feeds every truncation of a rich
// request, and a set of corruptions, through the real pipeline with every
// optimizer on. The engine must never panic, and whatever it returns must be
// either the original bytes or a body that still parses: a damaged request is
// forwarded exactly as received.
func TestRealPipelineFailsOpenOnDamagedRequests(t *testing.T) {
	good := req{
		system: pad(5000) + "Today's date: 2026-10-29\n",
		tools:  []string{"bash", "read"},
		msgs: []msg{
			user(text("go")),
			assistant(toolUse("t1", "bash")),
			user(toolResult("t1", noisyOutput()), toolResultBlocks("t2", bigOutput("a"), "x")),
			assistant(toolUse("t3", "bash")),
			user(toolResult("t3", bigOutput("a")), toolResult("t4", goTestOutput(120))),
		},
	}.body()

	cfg := config.Default()
	for _, n := range PipelineNames {
		cfg.Optimizers[n] = config.StateOn
	}
	cfg.Optimize.CustomAgents = []string{"sdk"}
	cfg.Optimize.BudgetMS = 1000
	e := New(Options{Config: cfg, Getenv: func(string) string { return "" }})

	check := func(name string, body []byte) {
		t.Helper()
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("%s: engine panicked: %v", name, p)
			}
		}()
		res := e.Run(context.Background(), input(body))
		if !res.Changed {
			if !bytes.Equal(res.Body, body) {
				t.Fatalf("%s: unchanged result differs from the input", name)
			}
			return
		}
		if _, err := ParseRequest(res.Body); err != nil {
			t.Fatalf("%s: forwarded a body that does not parse: %v", name, err)
		}
	}

	// Sanity: the intact request is optimized.
	if res := e.Run(context.Background(), input(good)); !res.Changed || len(res.Failures) != 0 {
		t.Fatalf("intact request: changed=%v failures=%+v", res.Changed, res.Failures)
	}

	for n := 0; n < len(good); n += 37 {
		check("truncated", good[:n])
	}
	for name, body := range map[string][]byte{
		"empty":            nil,
		"not json":         []byte("hello"),
		"array":            []byte("[]"),
		"null messages":    []byte(`{"model":"claude-opus-4-8","messages":null}`),
		"string messages":  []byte(`{"model":"claude-opus-4-8","messages":"x"}`),
		"object messages":  []byte(`{"model":"claude-opus-4-8","messages":{}}`),
		"null content":     []byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":null}]}`),
		"number block":     []byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":[1,2]}]}`),
		"bad tool result":  []byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":[{"type":"tool_result","content":{"a":1}}]}]}`),
		"result no id":     []byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":[{"type":"tool_result","content":"` + strings.Repeat("x", 300) + `"}]}]}`),
		"system number":    []byte(`{"model":"claude-opus-4-8","system":5,"messages":[{"role":"user","content":"hi"}]}`),
		"system objects":   []byte(`{"model":"claude-opus-4-8","system":[1,{"type":"text"},{"text":5}],"messages":[{"role":"user","content":"hi"}]}`),
		"tools not array":  []byte(`{"model":"claude-opus-4-8","tools":{},"messages":[{"role":"user","content":"hi"}]}`),
		"empty tool":       []byte(`{"model":"claude-opus-4-8","tools":[{}],"system":"` + strings.Repeat("a ", 4000) + `","messages":[{"role":"user","content":"hi"}]}`),
		"model not string": []byte(`{"model":5,"messages":[]}`),
		"bad escape":       []byte(`{"model":"claude-opus-4-8","system":"` + strings.Repeat("a", 5000) + `\q Today's date: 2026-10-29","messages":[{"role":"user","content":"hi"}]}`),
		"deep nesting":     []byte(`{"model":"x","messages":[{"role":"user","content":[{"type":"tool_result","content":[` + strings.Repeat("[", 200) + strings.Repeat("]", 200) + `]}]}]}`),
	} {
		check(name, body)
	}
}
