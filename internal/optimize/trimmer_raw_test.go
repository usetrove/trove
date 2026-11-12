package optimize

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/recovery"
)

// ansiTestOutput is large test output with color codes. noise_normalizer removes
// the codes before the trimmer runs, so the trimmer's input differs from what the
// tool printed. Recovery must still return what the tool printed.
func ansiTestOutput() string {
	return strings.ReplaceAll(goTestOutput(200), "PASS", "\x1b[32mPASS\x1b[0m")
}

func rawRecoveryConfig() config.Config {
	cfg := trimmerOn()
	cfg.Optimizers[config.OptimizerNoiseNormalizer] = config.StateOn
	return cfg
}

// The model receives the normalized text, the marker names the raw output's hash,
// and the store keeps the raw output byte for byte.
func TestRequestRecoveryReturnsRawOutputByteForByte(t *testing.T) {
	raw := ansiTestOutput()
	if !strings.Contains(raw, "\x1b[") {
		t.Fatal("test input must contain ANSI codes")
	}
	body := req{msgs: []msg{user(toolResult("t1", raw))}}.body()
	store := &memRecovery{}
	e := New(Options{Config: rawRecoveryConfig(), Getenv: func(string) string { return "" }, Recovery: store})
	res := e.Run(context.Background(), input(body))
	if !res.Changed {
		t.Fatalf("expected the request to change: %+v", res.Failures)
	}
	if strings.Contains(string(res.Body), `\u001b`) {
		t.Error("the model must not receive ANSI codes after noise_normalizer ran")
	}
	id := recovery.ID([]byte(raw))
	if !strings.Contains(string(res.Body), "full output sha256:"+id) {
		t.Fatalf("marker must name the raw output's hash %s", id)
	}
	if store.got[id] != raw {
		t.Fatalf("recovered output differs from the raw tool output (stored %d bytes, raw %d bytes)", len(store.got[id]), len(raw))
	}
}

// The same guarantee on the tool-output path, where the tool output is a
// separate event and not part of a request.
func TestToolRecoveryReturnsRawOutputByteForByte(t *testing.T) {
	raw := ansiTestOutput()
	store := &memRecovery{}
	e := New(Options{Config: rawRecoveryConfig(), Getenv: func(string) string { return "" }, Recovery: store})
	s := Session{Key: "tool", Agent: "claude_code", TroveRun: true, State: func(name string) string {
		if name == config.OptimizerNoiseNormalizer || name == config.OptimizerOutputTrimmer {
			return config.StateOn
		}
		return config.StateOff
	}}
	out, reps := e.TransformToolOutput(context.Background(), ToolCall{Name: "Bash"}, ToolOutput{Parts: []Part{{Text: raw}}}, s)
	if len(out.Parts) != 1 || out.Parts[0].Text == raw {
		t.Fatalf("expected the output to be shortened: reps=%+v", reps)
	}
	id := recovery.ID([]byte(raw))
	if !strings.Contains(out.Parts[0].Text, "full output sha256:"+id) {
		t.Fatalf("marker must name the raw output's hash %s", id)
	}
	if store.got[id] != raw {
		t.Fatal("recovered output differs from the raw tool output byte for byte")
	}
}

// The model-facing text and the stored original are both valid, and the stored
// original decodes to exactly the tool's text even after JSON escaping.
func TestRecoveredOutputSurvivesJSONEscaping(t *testing.T) {
	raw := ansiTestOutput()
	body := req{msgs: []msg{user(toolResult("t1", raw))}}.body()
	store := &memRecovery{}
	e := New(Options{Config: rawRecoveryConfig(), Getenv: func(string) string { return "" }, Recovery: store})
	res := e.Run(context.Background(), input(body))
	var v any
	if err := json.Unmarshal(res.Body, &v); err != nil {
		t.Fatalf("forwarded body is not valid JSON: %v", err)
	}
	if store.got[recovery.ID([]byte(raw))] != raw {
		t.Fatal("stored output changed through the JSON round trip")
	}
}
