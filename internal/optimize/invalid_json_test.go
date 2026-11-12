package optimize

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
)

// The fixtures are the failing inputs from the stress suite's fuzz layer (seed 20261108): raw control bytes and
// invalid UTF-8 inside a string, and nesting far past any real request. The engine must forward each one
// unchanged, with every optimizer on.
func fixtureBodies(t *testing.T) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for _, name := range []string{"binary-invalid-utf8", "deep-nesting"} {
		b, err := os.ReadFile(filepath.Join("testdata", "invalid-json", name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		out[name] = b
	}
	return out
}

func TestParseRequestRejectsInvalidJSONFixtures(t *testing.T) {
	for name, body := range fixtureBodies(t) {
		if json.Valid(body) && name == "binary-invalid-utf8" {
			t.Fatalf("%s is valid JSON; the fixture no longer tests what it is for", name)
		}
		if _, err := ParseRequest(body); err == nil {
			t.Errorf("%s: ParseRequest accepted a body that encoding/json rejects", name)
		}
	}
}

func TestEngineForwardsInvalidFixturesUnchanged(t *testing.T) {
	cfg := config.Default()
	cfg.Optimize.BudgetMS = 60000
	cfg.Optimize.CustomAgents = []string{"fixture_custom"}
	cfg.Optimizers = map[string]string{}
	for _, n := range PipelineNames {
		cfg.Optimizers[n] = config.StateOn
	}
	eng := New(Options{Config: cfg, Getenv: func(string) string { return "" }, Now: time.Now})
	for name, body := range fixtureBodies(t) {
		res := eng.TransformRequest(context.Background(), RequestIn{
			Provider: "anthropic", Path: "/v1/messages", Header: http.Header{}, Body: body,
		}, Session{Key: "fixture-" + name, Agent: "fixture_custom", TroveRun: true})
		if !bytes.Equal(res.Body, body) {
			t.Errorf("%s: the engine changed a body that is not a readable request", name)
		}
		if res.Changed {
			t.Errorf("%s: Changed is true for a forwarded original", name)
		}
		if len(res.Failures) == 0 || res.Failures[0].Class != "malformed" {
			t.Errorf("%s: want a malformed-request failure recorded, got %+v", name, res.Failures)
		}
	}
}

func TestMaxNestingDepth(t *testing.T) {
	nest := func(depth int) []byte {
		return []byte(`{"messages":[],"x":` + strings.Repeat("[", depth) + strings.Repeat("]", depth) + `}`)
	}
	// The outer object and the messages array count too, so "x" is depth+1 deep at most.
	if _, err := ParseRequest(nest(MaxNestingDepth - 2)); err != nil {
		t.Fatalf("a body within the limit was rejected: %v", err)
	}
	if _, err := ParseRequest(nest(MaxNestingDepth + 1)); err == nil {
		t.Fatal("a body past the nesting limit was accepted")
	}
}

// Brackets inside strings are not nesting.
func TestMaxDepthIgnoresStrings(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"` + strings.Repeat("[[[[{{{{", 500) + `\"]]]]}}}}"}]}`)
	if !json.Valid(body) {
		t.Fatal("test body is not valid JSON")
	}
	if _, err := ParseRequest(body); err != nil {
		t.Fatalf("brackets inside a string counted as nesting: %v", err)
	}
}

// The depth scan skips strings, so it must get escapes exactly right: an escaped quote does not end a string, and
// an escaped backslash does not escape the quote after it.
func TestMaxDepthEscapes(t *testing.T) {
	cases := []struct {
		json string
		want int
	}{
		{`"\\"`, 0},                   // an escaped backslash, then the closing quote
		{`"\\\"[[[["`, 0},             // escaped backslash, escaped quote, then brackets that are still inside the string
		{`"a\"b\\\\"`, 0},             // escaped quote, then two escaped backslashes and the closing quote
		{`[[[1]]]`, 3},                // plain nesting
		{`{"k":"\\\"","n":[[1]]}`, 3}, // object, then two arrays
	}
	for _, c := range cases {
		if !json.Valid([]byte(c.json)) {
			t.Fatalf("test case is not valid JSON: %s", c.json)
		}
		if got := maxDepth([]byte(c.json)); got != c.want {
			t.Errorf("maxDepth(%s) = %d, want %d", c.json, got, c.want)
		}
	}
}

// A request is validated once on the way in. A changed output is validated once more. An edit between
// optimizers is only scanned, so an unchanged request costs exactly one validation.
func TestValidationCountPerRequest(t *testing.T) {
	cfg := config.Default()
	cfg.Optimize.BudgetMS = 60000
	cfg.Optimizers = map[string]string{"noise_normalizer": config.StateOn}
	eng := New(Options{Config: cfg, Getenv: func(string) string { return "" }, Now: time.Now})

	plain := []byte(`{"model":"claude-sonnet-5-5","messages":[{"role":"user","content":"hello"}]}`)
	before := validations.Load()
	eng.TransformRequest(context.Background(), RequestIn{Provider: "anthropic", Path: "/v1/messages",
		Header: http.Header{}, Body: plain}, Session{Key: "count-plain", Agent: "claude_code"})
	if got := validations.Load() - before; got != 1 {
		t.Fatalf("unchanged request validated %d times, want 1", got)
	}

	ansi := []byte(`{"model":"claude-sonnet-5-5","messages":[{"role":"user","content":[{"type":"tool_result",` +
		`"tool_use_id":"t1","content":"\u001b[31mred\u001b[0m output\nsecond line\n"}]}]}`)
	before = validations.Load()
	res := eng.TransformRequest(context.Background(), RequestIn{Provider: "anthropic", Path: "/v1/messages",
		Header: http.Header{}, Body: ansi}, Session{Key: "count-ansi", Agent: "claude_code"})
	if !res.Changed {
		t.Fatal("the ANSI body was not changed, so the count below measured nothing")
	}
	if got := validations.Load() - before; got != 2 {
		t.Fatalf("changed request validated %d times, want 2 (input and output)", got)
	}
}
