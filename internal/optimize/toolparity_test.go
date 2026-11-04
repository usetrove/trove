package optimize

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/usetrove/trove/internal/config"
)

// Compile-time proof that every optimizer declares its capabilities.
var (
	_ Capable             = VolatilePrefixMover{}
	_ Capable             = NoiseNormalizer{}
	_ Capable             = OutputTrimmer{}
	_ Capable             = DuplicateCollapser{}
	_ Capable             = CacheBreakpoints{}
	_ ToolOutputOptimizer = NoiseNormalizer{}
	_ ToolOutputOptimizer = OutputTrimmer{}
)

func TestPipelineOptimizersAllDeclareCapabilities(t *testing.T) {
	for _, o := range Pipeline() {
		c, ok := o.(Capable)
		if !ok || len(c.Points()) == 0 {
			t.Errorf("%s does not declare where it can act", o.Name())
		}
	}
}

// toolResultText pulls the first tool_result text out of a request body.
func toolResultText(t *testing.T, body []byte) string {
	t.Helper()
	var doc struct {
		Messages []struct {
			Content []struct {
				Type    string `json:"type"`
				Content any    `json:"content"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	for _, m := range doc.Messages {
		for _, b := range m.Content {
			if b.Type == "tool_result" {
				if s, ok := b.Content.(string); ok {
					return s
				}
			}
		}
	}
	t.Fatal("no tool_result in body")
	return ""
}

// The request-level path and the tool-level path must give the same text,
// because adapters differ only in how they reach the engine.
func TestToolLevelMatchesRequestLevel(t *testing.T) {
	for _, tc := range []struct {
		name, optimizer, text string
	}{
		{"noise", "noise_normalizer", noisyOutput()},
		{"trimmer", config.OptimizerOutputTrimmer, goTestOutput(150)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Optimizers[tc.optimizer] = config.StateOn
			e := New(Options{Config: cfg, Getenv: func(string) string { return "" }, Recovery: &memRecovery{}})
			body := req{msgs: []msg{user(text("go")), assistant(toolUse("t1", "Bash")), user(toolResult("t1", tc.text))}}.body()
			res := e.TransformRequest(context.Background(), RequestIn{Provider: "anthropic", Path: "/v1/messages", Body: body}, Session{Key: "s", Agent: "sdk"})
			if !res.Changed {
				t.Fatal("fixture did not trigger the optimizer on the request path")
			}
			want := toolResultText(t, res.Body)

			out, reps := e.TransformToolOutput(context.Background(), ToolCall{Name: "Bash"},
				ToolOutput{Parts: []Part{{Text: tc.text}}},
				Session{Key: "s", State: func(n string) string {
					if n == tc.optimizer {
						return config.StateOn
					}
					return config.StateOff
				}})
			if len(reps) != 1 || reps[0].Err != "" || !reps[0].Changed {
				t.Fatalf("tool-level reports: %+v", reps)
			}
			if out.Parts[0].Text != want {
				t.Fatalf("tool-level and request-level text differ:\n%q\nvs\n%q", out.Parts[0].Text, want)
			}
		})
	}
}

func TestToolLevelTrimmerNeedsRecovery(t *testing.T) {
	cfg := config.Default()
	e := New(Options{Config: cfg, Getenv: func(string) string { return "" }}) // no recovery store
	in := ToolOutput{Parts: []Part{{Text: goTestOutput(150)}}}
	out, reps := e.TransformToolOutput(context.Background(), ToolCall{Name: "Bash"}, in,
		Session{Key: "s", State: func(string) string { return config.StateOn }})
	for _, r := range reps {
		if r.Name == config.OptimizerOutputTrimmer && r.State == config.StateOn {
			t.Fatal("trimmer must be clamped to shadow without a recovery store")
		}
	}
	if out.Parts[0].Text == in.Parts[0].Text {
		return // noise may have cleaned it; trimmer must not have trimmed it
	}
	if len(out.Parts[0].Text) < len(in.Parts[0].Text)/2 {
		t.Fatal("output was trimmed without a recovery store")
	}
}
