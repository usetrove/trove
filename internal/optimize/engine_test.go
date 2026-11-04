package optimize

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
)

func TestParseRequest(t *testing.T) {
	body := req{
		system: "be brief",
		tools:  []string{"bash"},
		msgs: []msg{
			user(text("hi")),
			assistant(toolUse("t1", "bash")),
			user(toolResult("t1", "out"), toolResultBlocks("t2", "a", "b")),
		},
	}.body()
	r, err := ParseRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	if r.Model != "claude-opus-4-8" || len(r.Messages) != 3 || r.System.E == 0 || r.Tools.E == 0 {
		t.Fatalf("unexpected parse: %+v", r)
	}
	b := r.Messages[2].Blocks
	if len(b) != 2 || b[0].ToolUseID != "t1" || len(b[0].Texts) != 1 || !b[0].TextOnly || len(b[1].Texts) != 2 {
		t.Fatalf("tool results parsed wrong: %+v", b)
	}
	if got, _ := r.DecodeString(b[0].Texts[0]); got != "out" {
		t.Errorf("decoded %q", got)
	}
	if r.HasCacheControl() {
		t.Error("no cache_control expected")
	}
}

func TestParseRequestMalformed(t *testing.T) {
	for name, body := range map[string]string{
		"empty":           ``,
		"array":           `[1,2]`,
		"truncated":       `{"model":"x","messages":[{"role":"user"`,
		"bad key":         `{model:"x"}`,
		"unterminated":    `{"model":"x`,
		"unbalanced":      `{"messages":[}}`,
		"missing colon":   `{"model" "x"}`,
		"text not json":   `hello`,
		"only whitespace": "   ",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseRequest([]byte(body)); err == nil {
				t.Errorf("expected an error for %q", body)
			}
		})
	}
}

func TestApplyPreservesUntouchedBytes(t *testing.T) {
	body := []byte("{ \"model\" : \"m\",\n \"messages\": [ ] , \"x\":  1 }")
	r, err := ParseRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	i := bytes.Index(body, []byte(`"m"`))
	out, err := r.Apply([]Edit{{Span: Span{i, i + 3}, New: []byte(`"n"`)}})
	if err != nil {
		t.Fatal(err)
	}
	want := "{ \"model\" : \"n\",\n \"messages\": [ ] , \"x\":  1 }"
	if string(out.Raw) != want {
		t.Errorf("got %q want %q", out.Raw, want)
	}
	if _, err := r.Apply([]Edit{{Span: Span{0, 5}}, {Span: Span{3, 8}}}); err == nil {
		t.Error("overlapping edits must be rejected")
	}
}

func TestEncodeStringRoundTrip(t *testing.T) {
	for _, s := range []string{"", "plain", "a\"b\\c\n\t", "<html>&", "unicode ✓  "} {
		enc := EncodeString(s)
		got, ok := decodeString(enc)
		if !ok || got != s {
			t.Errorf("%q -> %s -> %q", s, enc, got)
		}
		if !bytes.Equal(enc, EncodeString(s)) {
			t.Errorf("EncodeString not deterministic for %q", s)
		}
	}
}

func TestEngineSkips(t *testing.T) {
	on := map[string]string{"a": "on"}
	pass := stub{name: "a", applies: true, transform: replaceFirst("hi", "HI")}
	body := req{msgs: []msg{user(text("hi"))}}.body()

	tests := []struct {
		name string
		mod  func(*Input)
		want string
	}{
		{"openai", func(i *Input) { i.Provider = "openai" }, "not an Anthropic"},
		{"other path", func(i *Input) { i.Path = "/v1/messages/count_tokens" }, "not an Anthropic"},
		{"compressed", func(i *Input) { i.Header = map[string][]string{"Content-Encoding": {"gzip"}} }, "compressed"},
		{"no key", func(i *Input) { i.SessionKey = "" }, "no session key"},
		{"empty", func(i *Input) { i.Body = nil }, "empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := New(Options{Config: cfgWith(on), Pipeline: []Optimizer{pass}})
			in := input(body)
			tt.mod(&in)
			res := e.Run(context.Background(), in)
			if !bytes.Equal(res.Body, in.Body) || res.Changed || res.Skipped == "" {
				t.Errorf("expected skip, got %+v", res)
			}
		})
	}
}

func cfgWith(states map[string]string) config.Config {
	c := config.Default()
	for k, v := range states {
		c.Optimizers[k] = v
	}
	return c
}

func TestEngineAllOffIsNoOp(t *testing.T) {
	e := New(Options{Config: config.Default(), Pipeline: []Optimizer{stub{name: "a", applies: true, transform: replaceFirst("hi", "HI")}}})
	body := req{msgs: []msg{user(text("hi"))}}.body()
	res := e.Run(context.Background(), input(body))
	if res.Changed || !bytes.Equal(res.Body, body) || res.Skipped == "" {
		t.Errorf("%+v", res)
	}
}

func TestEngineOnChangesShadowDoesNot(t *testing.T) {
	// Pin states by name through the pipeline: the stubs use real pipeline names
	// because the pin only snapshots PipelineNames.
	body := req{msgs: []msg{user(text("hello hello hello hello"))}}.body()
	mk := func(state string) Result {
		e := testEngine(t, map[string]string{"noise_normalizer": state},
			stub{name: "noise_normalizer", applies: true, transform: replaceFirst("hello hello", "hello")})
		return e.Run(context.Background(), input(body))
	}
	on, shadow := mk("on"), mk("shadow")
	if !on.Changed || bytes.Equal(on.Body, body) {
		t.Fatal("on must change the request")
	}
	if shadow.Changed || !bytes.Equal(shadow.Body, body) {
		t.Fatal("shadow must forward the original")
	}
	if len(on.Applied) != 1 || len(shadow.Applied) != 1 {
		t.Fatalf("applied: %+v %+v", on.Applied, shadow.Applied)
	}
	// Acceptance: shadow and on report the same tokens-removed figure.
	if on.Applied[0].TokensRemoved != shadow.Applied[0].TokensRemoved || on.Applied[0].BytesRemoved != shadow.Applied[0].BytesRemoved {
		t.Errorf("shadow %+v differs from on %+v", shadow.Applied[0], on.Applied[0])
	}
	if on.Applied[0].BytesRemoved != 6 || on.Applied[0].TokensRemoved != 2 {
		t.Errorf("unexpected figures: %+v", on.Applied[0])
	}
	if on.TokensRemoved() != 2 || shadow.TokensRemoved() != 0 {
		t.Errorf("only on-state tokens count as removed: on=%d shadow=%d", on.TokensRemoved(), shadow.TokensRemoved())
	}
}

func TestEngineFailOpen(t *testing.T) {
	body := req{msgs: []msg{user(text("hello"))}}.body()
	good := stub{name: "volatile_prefix_mover", applies: true, transform: replaceFirst("hello", "HELLO")}
	tests := []struct {
		name  string
		bad   Optimizer
		class string
	}{
		{"panic in Transform", stub{name: "noise_normalizer", applies: true, transform: func(context.Context, *Request) (*Request, Report) { panic("boom") }}, "panic"},
		{"panic in Applies", panicApplies{stub{name: "noise_normalizer"}}, "panic"},
		{"slow", stub{name: "noise_normalizer", applies: true, transform: func(_ context.Context, r *Request) (*Request, Report) {
			time.Sleep(300 * time.Millisecond)
			return r, Report{}
		}}, "timeout"},
		{"error report", stub{name: "noise_normalizer", applies: true, transform: func(_ context.Context, r *Request) (*Request, Report) {
			return r, Report{Err: "bad"}
		}}, "error"},
		{"nil request", stub{name: "noise_normalizer", applies: true, transform: func(context.Context, *Request) (*Request, Report) { return nil, Report{} }}, "error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := cfgWith(map[string]string{"volatile_prefix_mover": "on", "noise_normalizer": "on"})
			cfg.Optimize.BudgetMS = 50
			e := New(Options{Config: cfg, Pipeline: []Optimizer{good, tt.bad}})
			res := e.Run(context.Background(), input(body))
			// The earlier, healthy optimizer's change must be discarded too.
			if res.Changed || !bytes.Equal(res.Body, body) {
				t.Fatalf("original request not forwarded: %s", res.Body)
			}
			if len(res.Failures) != 1 || res.Failures[0].Class != tt.class || res.Failures[0].Optimizer != "noise_normalizer" {
				t.Errorf("failure not recorded: %+v", res.Failures)
			}
		})
	}
}

type panicApplies struct{ stub }

func (panicApplies) Applies(*Request, Session) bool { panic("boom") }

func TestEngineShadowFailureDoesNotBlockOn(t *testing.T) {
	body := req{msgs: []msg{user(text("hello"))}}.body()
	cfg := cfgWith(map[string]string{"volatile_prefix_mover": "on", "noise_normalizer": "shadow"})
	e := New(Options{Config: cfg, Pipeline: []Optimizer{
		stub{name: "volatile_prefix_mover", applies: true, transform: replaceFirst("hello", "HELLO")},
		stub{name: "noise_normalizer", applies: true, transform: func(context.Context, *Request) (*Request, Report) { panic("x") }},
	}})
	res := e.Run(context.Background(), input(body))
	if !res.Changed || len(res.Failures) != 1 {
		t.Errorf("a failing shadow optimizer must not undo an on optimizer: %+v", res)
	}
}

func TestEngineMalformedForwardsOriginal(t *testing.T) {
	e := testEngine(t, map[string]string{"noise_normalizer": "on"},
		stub{name: "noise_normalizer", applies: true, transform: replaceFirst("a", "b")})
	for _, b := range []string{`{"messages":[{"role":`, `not json`, `[]`, `{"messages":[}}`} {
		res := e.Run(context.Background(), input([]byte(b)))
		if string(res.Body) != b || res.Changed {
			t.Errorf("%q was modified", b)
		}
		if len(res.Failures) != 1 || res.Failures[0].Class != "malformed" {
			t.Errorf("%q: failures %+v", b, res.Failures)
		}
	}
}

func TestEngineKillSwitch(t *testing.T) {
	dir := t.TempDir()
	kill := KillFile(dir)
	env := ""
	e := New(Options{
		Config:   cfgWith(map[string]string{"noise_normalizer": "on"}),
		Pipeline: []Optimizer{stub{name: "noise_normalizer", applies: true, transform: replaceFirst("hello", "HELLO")}},
		KillFile: kill,
		Getenv:   func(k string) string { return map[string]string{config.EnvOptimize: env}[k] },
	})
	body := req{msgs: []msg{user(text("hello"))}}.body()
	if res := e.Run(context.Background(), input(body)); !res.Changed {
		t.Fatal("expected a change before the kill switch")
	}
	env = "off"
	if res := e.Run(context.Background(), input(body)); res.Changed || res.Skipped == "" {
		t.Errorf("env var must disable instantly: %+v", res)
	}
	env = ""
	if err := os.WriteFile(kill, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if res := e.Run(context.Background(), input(body)); res.Changed {
		t.Errorf("kill file must disable instantly: %+v", res)
	}
	if err := os.Remove(filepath.Clean(kill)); err != nil {
		t.Fatal(err)
	}
	if res := e.Run(context.Background(), input(body)); !res.Changed {
		t.Error("removing the kill file must resume")
	}
}

type memPins struct {
	m     map[string]Pin
	saves int
	fail  bool
}

func (p *memPins) Load(_ context.Context, k string) (Pin, bool, error) {
	v, ok := p.m[k]
	return v, ok, nil
}
func (p *memPins) Save(_ context.Context, pin Pin) error {
	p.saves++
	if p.fail {
		return os.ErrPermission
	}
	p.m[pin.SessionKey] = pin
	return nil
}

func TestPinningHoldsStateUntilNextSession(t *testing.T) {
	store := &memPins{m: map[string]Pin{}}
	body := req{msgs: []msg{user(text("hello"))}}.body()
	opt := stub{name: "noise_normalizer", applies: true, transform: replaceFirst("hello", "HELLO")}

	e1 := New(Options{Config: cfgWith(map[string]string{"noise_normalizer": "on"}), Pipeline: []Optimizer{opt}, Pins: store})
	if !e1.Run(context.Background(), input(body)).Changed {
		t.Fatal("session s1 should be optimized")
	}
	// The user turns the optimizer off in config, and the gateway restarts
	// mid-session: s1 must keep its pinned state, a new session must not.
	e2 := New(Options{Config: cfgWith(nil), Pipeline: []Optimizer{opt}, Pins: store})
	if !e2.Run(context.Background(), input(body)).Changed {
		t.Error("restart mid-session changed the output: pin was not reloaded")
	}
	in2 := input(body)
	in2.SessionKey = "s2"
	if e2.Run(context.Background(), in2).Changed {
		t.Error("a new session must use the new config")
	}
	if store.saves != 2 {
		t.Errorf("expected 2 pin saves (s1, s2), got %d", store.saves)
	}
}

func TestPinStoreFailureFailsOpenToMemory(t *testing.T) {
	store := &memPins{m: map[string]Pin{}, fail: true}
	e := New(Options{Config: cfgWith(map[string]string{"noise_normalizer": "on"}),
		Pipeline: []Optimizer{stub{name: "noise_normalizer", applies: true, transform: replaceFirst("hello", "HELLO")}}, Pins: store})
	body := req{msgs: []msg{user(text("hello"))}}.body()
	if !e.Run(context.Background(), input(body)).Changed {
		t.Error("a pin-store failure must not stop optimizing")
	}
}

func TestTrimmerNotOnWithoutRecovery(t *testing.T) {
	// "on" is valid config, but the pin clamps it to shadow unless trimmed output
	// can be fetched back.
	cfg := config.Default()
	cfg.Optimizers[config.OptimizerOutputTrimmer] = config.StateOn
	p := newPin("k", cfg, 3.5, time.Now(), nil)
	if got := p.States[config.OptimizerOutputTrimmer]; got != config.StateShadow {
		t.Errorf("trimmer state = %q, want shadow", got)
	}
	p = newPinFor("k", cfg, 3.5, time.Now(), nil, true)
	if got := p.States[config.OptimizerOutputTrimmer]; got != config.StateOn {
		t.Errorf("with recovery the trimmer state = %q, want on", got)
	}
}

func TestCustomAgentNeverClaudeCodeOrCodex(t *testing.T) {
	cfg := config.Default()
	cfg.Optimize.CustomAgents = []string{"sdk", "claude_code", "codex"}
	p := newPin("k", cfg, 3.5, time.Now(), nil)
	for agent, want := range map[string]bool{"sdk": true, "other": false, "claude_code": false, "codex": false} {
		if got := p.customFor(agent); got != want {
			t.Errorf("customFor(%s) = %v, want %v", agent, got, want)
		}
	}
}

func TestAppliedJSON(t *testing.T) {
	if AppliedJSON(nil, nil) != nil {
		t.Error("nothing applied must store NULL")
	}
	b := AppliedJSON([]Report{{Name: "noise_normalizer", State: "on", BytesRemoved: 70, TokensRemoved: 20}}, []Failure{{Optimizer: "x", Class: "panic"}})
	want := `{"label":"estimated","optimizers":[{"name":"noise_normalizer","state":"on","bytes_removed":70,"tokens_removed":20},{"name":"x","state":"failed","bytes_removed":0,"tokens_removed":0,"failed":"panic"}]}`
	if string(b) != want {
		t.Errorf("got %s", b)
	}
}

func TestEngineNeverForwardsAMalformedResult(t *testing.T) {
	body := req{msgs: []msg{user(text("hello"))}}.body()
	breaks := stub{name: "noise_normalizer", applies: true, transform: replaceFirst(`"messages"`, `"messages`)} // drops a quote
	tests := []struct {
		name     string
		pipeline []Optimizer
	}{
		{"last optimizer breaks the body", []Optimizer{breaks}},
		{"a later optimizer receives the broken body", []Optimizer{breaks, stub{name: "duplicate_result_collapser", applies: true, transform: replaceFirst("x", "y")}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := testEngine(t, map[string]string{"noise_normalizer": "on", "duplicate_result_collapser": "on"}, tt.pipeline...)
			res := e.Run(context.Background(), input(body))
			if res.Changed || !bytes.Equal(res.Body, body) {
				t.Fatalf("a malformed body was forwarded: %s", res.Body)
			}
			if len(res.Failures) == 0 || res.Failures[len(res.Failures)-1].Class != "malformed" {
				t.Errorf("failure not recorded: %+v", res.Failures)
			}
		})
	}
}

func TestGateOnlyLowersStatesAndOnlyForNewSessions(t *testing.T) {
	cfg := config.Default()
	cfg.Optimizers["noise_normalizer"] = config.StateOn
	cfg.Optimizers["cache_breakpoints"] = config.StateShadow
	raise := func(_, _, _ string, _ time.Time) string { return config.StateOn } // a misbehaving gate
	p := newPin("k", cfg, 3.5, time.Now(), raise)
	if p.States["noise_normalizer"] != config.StateOn || p.States["cache_breakpoints"] != config.StateShadow || p.States["volatile_prefix_mover"] != config.StateOff {
		t.Errorf("a gate must never raise a state: %v", p.States)
	}
	lower := func(_, name, configured string, _ time.Time) string {
		if name == "noise_normalizer" {
			return config.StateShadow
		}
		return configured
	}
	p = newPin("k", cfg, 3.5, time.Now(), lower)
	if p.States["noise_normalizer"] != config.StateShadow || p.States["cache_breakpoints"] != config.StateShadow {
		t.Errorf("gate did not lower noise_normalizer: %v", p.States)
	}

	// A pinned session keeps its states even if the gate changes its mind.
	calls := 0
	gate := func(_, _, configured string, _ time.Time) string {
		calls++
		if calls > len(PipelineNames) {
			return config.StateOff
		}
		return configured
	}
	book := newPinBook(nil)
	first, _ := book.get(context.Background(), "s", cfg, 3.5, time.Now(), gate)
	second, _ := book.get(context.Background(), "s", cfg, 3.5, time.Now(), gate)
	if first.States["noise_normalizer"] != config.StateOn || second.States["noise_normalizer"] != config.StateOn {
		t.Errorf("the gate changed a running session: %v then %v", first.States, second.States)
	}
}

func TestPinIncludesExtraToolsUngated(t *testing.T) {
	book := newPinBook(nil)
	book.extras = func() map[string]string { return map[string]string{"ext_rules": "on"} }
	lowerAll := func(_, _, _ string, _ time.Time) string { return config.StateOff }
	p, err := book.get(context.Background(), "s1", config.Config{}, 3.5, time.Now(), lowerAll)
	if err != nil {
		t.Fatal(err)
	}
	if p.States["ext_rules"] != config.StateOn {
		t.Errorf("external tool state = %q, want the real state \"on\" (the gate cannot switch it)", p.States["ext_rules"])
	}
	for _, n := range PipelineNames {
		if p.States[n] != config.StateOff {
			t.Errorf("built-in %s = %q, want gate to apply", n, p.States[n])
		}
	}
}
