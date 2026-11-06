package optimize

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	"github.com/usetrove/trove/internal/config"
)

func tune(kv ...any) Tuning {
	t := Tuning{}
	for i := 0; i+1 < len(kv); i += 2 {
		t[TuningKey(config.OptimizerOutputTrimmer, kv[i].(string))] = kv[i+1].(int)
	}
	return t
}

func TestTrimParamsMatchTheShippedDefaults(t *testing.T) {
	var got TrimThresholds
	got = TrimThresholdsFrom(nil)
	if got != DefaultTrimThresholds {
		t.Fatalf("no tuning must give the shipped values: %+v vs %+v", got, DefaultTrimThresholds)
	}
	if TrimThresholdsFrom(Tuning{}) != DefaultTrimThresholds {
		t.Error("an empty tuning must give the shipped values")
	}
	for _, p := range TrimParams {
		if p.Min > p.Default || p.Default > p.Max {
			t.Errorf("%s: default %d is outside [%d, %d]", p.Name, p.Default, p.Min, p.Max)
		}
	}
	if len(TrimParams) != reflect.TypeOf(TrimThresholds{}).NumField() {
		t.Error("every threshold needs a TuneParam, so the tuner and the clamps cover it")
	}
}

func TestTuningIsClampedAndIgnoresStrangers(t *testing.T) {
	got := TrimThresholdsFrom(Tuning{
		TuningKey(config.OptimizerOutputTrimmer, ParamMinBytes): 1, // far below the range
		TuningKey(config.OptimizerOutputTrimmer, ParamKeepTail): 1 << 30,
		TuningKey(config.OptimizerOutputTrimmer, ParamKeepHead): -5,
		TuningKey("some_other_optimizer", ParamMinLines):        999999,
		"garbage": 7,
	})
	want := DefaultTrimThresholds
	want.MinBytes, want.KeepTail, want.KeepHead = 2000, 40, 2
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestTunedThresholdsChangeWhatIsTrimmed(t *testing.T) {
	in := goTestOutput(150)
	base, changed := trimWith(in, DefaultTrimThresholds)
	if !changed {
		t.Fatal("the default thresholds should trim this output")
	}

	// A higher size floor means this output is left alone.
	high := TrimThresholdsFrom(tune(ParamMinBytes, 50000))
	if out, changed := trimWith(in, high); changed || out != in {
		t.Error("with min_bytes above the output's size nothing should be trimmed")
	}
	// Keeping more head and tail lines keeps more text.
	wide := TrimThresholdsFrom(tune(ParamKeepHead, 20, ParamKeepTail, 40))
	w, changed := trimWith(in, wide)
	if !changed || len(w) <= len(base) {
		t.Errorf("keeping more head and tail should keep more text: %d vs %d", len(w), len(base))
	}
	// Tuned trimming is still deterministic and idempotent.
	for name, th := range map[string]TrimThresholds{"high": high, "wide": wide, "tight": TrimThresholdsFrom(tune(ParamKeepHead, 2, ParamKeepTail, 5))} {
		a, _ := trimWith(in, th)
		b, _ := trimWith(in, th)
		if a != b {
			t.Errorf("%s: not deterministic", name)
		}
		if again, changed := trimWith(a, th); changed || again != a {
			t.Errorf("%s: trimming already-trimmed output must remove nothing", name)
		}
	}
	// Failures survive whatever the tuning.
	tight, _ := trimWith(in, TrimThresholdsFrom(tune(ParamKeepHead, 2, ParamKeepTail, 5)))
	for _, must := range []string{"--- FAIL: TestBroken", "broken_test.go:42: expected 3, got 4", "goroutine 7 [running]:"} {
		if !bytes.Contains([]byte(tight), []byte(must)) {
			t.Errorf("tight tuning dropped %q", must)
		}
	}
}

// TestRequestPathUsesSessionTuning drives the engine's own entry point (guard),
// which must hand the session's tuning to the trimmer, and checks that without
// tuning the bytes are exactly what Transform produces.
func TestRequestPathUsesSessionTuning(t *testing.T) {
	body := req{msgs: []msg{user(toolResult("t1", goTestOutput(150)))}}.body()
	parse := func() *Request {
		r, err := ParseRequest(body)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	run := func(s Session) []byte {
		t.Helper()
		out, rep, fail := guard(context.Background(), OutputTrimmer{}, parse(), s)
		if fail != nil {
			t.Fatalf("failure: %+v", fail)
		}
		_ = rep
		return out.Raw
	}
	plain, _ := OutputTrimmer{}.Transform(context.Background(), parse())
	if got := run(Session{}); !bytes.Equal(got, plain.Raw) {
		t.Error("with no tuning the engine path must match Transform byte for byte")
	}
	if got := run(Session{Tuning: tune(ParamMinBytes, 50000)}); !bytes.Equal(got, body) {
		t.Error("a min_bytes above the output must leave the request untouched")
	}
	wide := run(Session{Tuning: tune(ParamKeepHead, 20, ParamKeepTail, 40)})
	if bytes.Equal(wide, plain.Raw) || len(wide) <= len(plain.Raw) {
		t.Error("the session's tuning did not reach the trimmer")
	}
	if !bytes.Equal(wide, run(Session{Tuning: tune(ParamKeepHead, 20, ParamKeepTail, 40)})) {
		t.Error("the same history and tuning must give byte-identical output")
	}
	if (OutputTrimmer{}).Applies(parse(), Session{Tuning: tune(ParamMinBytes, 50000)}) {
		t.Error("Applies must use the tuned size floor")
	}
}

func TestToolOutputPathUsesSessionTuning(t *testing.T) {
	in := goTestOutput(150)
	out := ToolOutput{Parts: []Part{{Text: in}}}
	def, _ := OutputTrimmer{}.TransformOutput(context.Background(), ToolCall{}, out, Session{})
	if def.Parts[0].Text == in {
		t.Fatal("the default should trim")
	}
	tuned, rep := OutputTrimmer{}.TransformOutput(context.Background(), ToolCall{}, out, Session{Tuning: tune(ParamMinBytes, 50000)})
	if tuned.Parts[0].Text != in || len(rep.Originals) != 0 {
		t.Error("tuned min_bytes must apply on the tool-output path too")
	}
}

func TestSessionTransformerIsImplementedByTheTrimmer(t *testing.T) {
	var o Optimizer = OutputTrimmer{}
	if _, ok := o.(SessionTransformer); !ok {
		t.Fatal("the engine only passes tuning to optimizers that implement SessionTransformer")
	}
}
