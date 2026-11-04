package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/hooks"
	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/recovery"
)

// Parity: the same tool output, sent through the gateway adapter (as a
// tool_result inside an API request) and through the hooks adapter (as a
// PostToolUse tool_response), must come out as identical text from every
// optimizer that both adapters can run. The optimizers to test come from the
// capability matrix, so a new optimizer that both adapters reach fails here
// until it has a fixture.
//
// What is compared is the text and whether it changed. Byte and token counts are
// not compared: the request level measures the JSON-escaped body (a newline is
// two bytes there) and the tool level measures the decoded text.

// parityFixtures is the fixture session: one tool output per optimizer, each
// large or noisy enough that the optimizer acts.
func parityFixtures() map[string]string {
	var gotest strings.Builder
	gotest.WriteString("=== RUN   TestSetup\n")
	for i := 0; i < 150; i++ {
		fmt.Fprintf(&gotest, "=== RUN   TestCase%03d\n--- PASS: TestCase%03d (0.00s)\n", i, i)
	}
	gotest.WriteString("=== RUN   TestBroken\n--- FAIL: TestBroken (0.01s)\n    broken_test.go:42: expected 3, got 4\n")
	gotest.WriteString("panic: runtime error: index out of range [5] with length 3\n\ngoroutine 7 [running]:\npkg.Broken(...)\n\t/src/pkg/broken.go:17 +0x1d\n")
	for i := 150; i < 190; i++ {
		fmt.Fprintf(&gotest, "=== RUN   TestAfter%03d\n--- PASS: TestAfter%03d (0.00s)\n", i, i)
	}
	gotest.WriteString("FAIL\tpkg\t0.123s\nFAIL\n")
	return map[string]string{
		"noise_normalizer": "\x1b[31mERR\x1b[0m start\n" + strings.Repeat("retrying the cache server connection\n", 30) +
			"progress 10%\rprogress 60%\rprogress 100%\ndone\n",
		"output_trimmer": gotest.String(),
	}
}

func parityEngine(t *testing.T, name string) *optimize.Engine {
	t.Helper()
	st, err := recovery.Open(t.TempDir(), recovery.Options{MaxBytes: 64 << 20, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Optimizers[name] = config.StateOn
	cfg.Optimize.BudgetMS = 1000 // a slow machine must make this test slow, not make the engine fail open
	var only []optimize.Optimizer
	for _, o := range optimize.Pipeline() {
		if o.Name() == name {
			only = append(only, o)
		}
	}
	return optimize.New(optimize.Options{Config: cfg, Pipeline: only, Recovery: st, Getenv: func(string) string { return "" }})
}

// viaGateway sends text as a tool_result through the real gateway and returns
// the tool_result text the upstream received.
func viaGateway(t *testing.T, name, text string) string {
	t.Helper()
	enc, _ := json.Marshal(text)
	body := []byte(`{"model":"claude-opus-5-5","system":"s","messages":[` +
		`{"role":"user","content":"go"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"Bash","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":` + string(enc) + `}]}]}`)
	up := &capture{}
	eng := parityEngine(t, name)
	g := newRigWith(t, up.handler(t), func(o *Options) { o.Optimizer = eng })
	do(t, "POST", g.gw.URL+"/anthropic/v1/messages", anthropicHdr, body)
	g.rec.wait(t)
	got, _ := up.got()
	var doc struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("upstream body is not JSON: %v", err)
	}
	var blocks []struct {
		Type    string `json:"type"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(doc.Messages[len(doc.Messages)-1].Content, &blocks); err != nil || len(blocks) == 0 {
		t.Fatalf("no tool_result in upstream body: %v", err)
	}
	return blocks[0].Content
}

// viaHooks sends text as a PostToolUse tool_response through the hooks adapter.
func viaHooks(t *testing.T, name, text string) (string, optimize.Report) {
	t.Helper()
	resp, _ := json.Marshal(map[string]any{"stdout": text, "stderr": ""})
	in := hooks.Input{SessionID: "s", ToolName: "Bash", Event: "PostToolUse", ToolResponse: resp}
	out, reps := hooks.TransformOutput(context.Background(), parityEngine(t, name), in,
		func(n string) string {
			if n == name {
				return config.StateOn
			}
			return config.StateOff
		})
	if len(out.Parts) != 1 {
		t.Fatalf("hooks adapter returned %d parts", len(out.Parts))
	}
	var rep optimize.Report
	for _, r := range reps {
		if r.Name == name {
			rep = r
		}
	}
	return out.Parts[0].Text, rep
}

func TestGatewayAndHooksAgreeOnEveryOptimizerBothSupport(t *testing.T) {
	fixtures := parityFixtures()
	ran := 0
	for _, row := range optimize.Matrix() {
		if !row.Reaches(optimize.AdapterGateway) || !row.Reaches(optimize.AdapterHooks) {
			t.Logf("skip %s: gateway=%s (%s); hooks=%s (%s)", row.Optimizer,
				row.Support[optimize.AdapterGateway].Level, row.Support[optimize.AdapterGateway].Reason,
				row.Support[optimize.AdapterHooks].Level, row.Support[optimize.AdapterHooks].Reason)
			continue
		}
		t.Run(row.Optimizer, func(t *testing.T) {
			text, ok := fixtures[row.Optimizer]
			if !ok {
				t.Fatalf("%s runs on both the gateway and hooks adapters but has no parity fixture; add one to parityFixtures", row.Optimizer)
			}
			fromGateway := viaGateway(t, row.Optimizer, text)
			fromHooks, rep := viaHooks(t, row.Optimizer, text)
			if fromGateway == text || fromHooks == text {
				t.Fatalf("the fixture did not make %s act (gateway changed=%v, hooks changed=%v)", row.Optimizer, fromGateway != text, fromHooks != text)
			}
			if fromGateway != fromHooks {
				t.Fatalf("adapters disagree for %s:\ngateway: %q\nhooks:   %q", row.Optimizer, fromGateway, fromHooks)
			}
			if !rep.Changed || rep.State != config.StateOn || rep.Err != "" {
				t.Fatalf("hooks report = %+v", rep)
			}
		})
		ran++
	}
	if ran == 0 {
		t.Fatal("no optimizer is supported by both adapters; the parity test is vacuous")
	}
}
