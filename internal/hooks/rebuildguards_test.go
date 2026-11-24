package hooks

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/measure/limitfit"
	"github.com/usetrove/trove/internal/technique"
)

// guardRig is a coldRig whose snapshot sets one rebuild guard's state. The decision tests lift both safety nets,
// as the other hook tests do; TestModelSwitchFailsOpen and the timing tests cover speed.
func newGuardRig(t *testing.T, optimizer, state string) *coldRig {
	t.Helper()
	r := newColdRig(t, "off", config.ColdResumeWarn, false)
	old := callDeadline
	callDeadline = 10 * time.Second
	t.Cleanup(func() { callDeadline = old })
	r.snapshot(Snapshot{States: map[string]string{optimizer: state}, ColdResume: ColdResumeSettings{MinTokens: 150000, MinWeekPct: 3}})
	return r
}

func (r *coldRig) switchModel(to, source string, tokens int64, warm bool) (string, int) {
	r.t.Helper()
	return r.run(EventModelSwitch, r.fill("claude_pre_model_switch.json", "{{TO}}", to, "{{SOURCE}}", source,
		"{{TOKENS}}", strconv.FormatInt(tokens, 10), "{{WARM}}", strconv.FormatBool(warm)))
}

func (r *coldRig) switched(source string) {
	r.t.Helper()
	if out, code := r.run(EventPostModelSwitch, r.fill("claude_post_model_switch.json", "{{SOURCE}}", source)); out != "" || code != 0 {
		r.t.Fatalf("PostModelSwitch must print nothing: %q %d", out, code)
	}
}

func guardKinds(r *rig, optimizer string) []string {
	var out []string
	for _, e := range r.events() {
		if e.Hook == optimizer {
			out = append(out, e.Kind)
		}
	}
	return out
}

// switchReply checks that the hook printed at most a systemMessage, never a decision of any kind.
func switchReply(t *testing.T, out string) string {
	t.Helper()
	if out == "" {
		return ""
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("not JSON: %q", out)
	}
	for k := range v {
		if k != "systemMessage" {
			t.Fatalf("the model-switch guard must never decide; it printed %q", out)
		}
	}
	msg, _ := v["systemMessage"].(string)
	return msg
}

func TestModelSwitchGuardModes(t *testing.T) {
	cases := []struct {
		name     string
		state    string
		to       string
		tokens   int64
		warm     bool
		wantWarn bool
		wantKind string
	}{
		{"off", "off", "claude-opus-5", 182340, true, false, ""},
		{"shadow, large and warm", "shadow", "claude-opus-5", 182340, true, false, KindWouldAdvise},
		{"on, large and warm", "on", "claude-opus-5", 182340, true, true, KindAdvised},
		{"on, cold: nothing to lose", "on", "claude-opus-5", 182340, false, false, ""},
		{"on, small", "on", "claude-opus-5", 20000, true, false, ""},
		{"on, same model", "on", "claude-sonnet-5", 182340, true, false, ""},
		{"on, before the first response", "on", "claude-opus-5", 0, true, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newGuardRig(t, config.OptimizerHookModelSwitch, c.state)
			r.begin("startup", false, 0, 0)
			out, code := r.switchModel(c.to, "command", c.tokens, c.warm)
			if code != 0 {
				t.Fatalf("exit %d", code)
			}
			msg := switchReply(t, out)
			if (msg != "") != c.wantWarn {
				t.Errorf("warning = %q, want warn %v", msg, c.wantWarn)
			}
			var want []string
			if c.wantKind != "" {
				want = []string{c.wantKind}
			}
			if got := guardKinds(r.rig, config.OptimizerHookModelSwitch); strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("kinds %v, want %v", got, want)
			}
		})
	}
}

func TestModelSwitchWarningFiguresAndCap(t *testing.T) {
	r := newGuardRig(t, config.OptimizerHookModelSwitch, "on")
	if err := limitfit.WriteCache(r.dir, limitfit.Result{
		FiveHour: limitfit.MeterFit{Meter: limitfit.FiveHour, Tier: limitfit.TierPrices, PerUSD: &limitfit.Range{Point: 10.5, Lo: 8.25, Hi: 12.75}},
		Week:     limitfit.MeterFit{Meter: limitfit.SevenDay, Tier: limitfit.TierPrices, PerUSD: &limitfit.Range{Point: 1.55, Lo: 1.25, Hi: 1.85}},
	}); err != nil {
		t.Fatal(err)
	}
	r.begin("startup", false, 0, 0)
	out, _ := r.switchModel("claude-opus-5", "picker", 1999999, true)
	msg := switchReply(t, out)
	for _, want := range []string{"about 2,000,000 tokens", "about $1.14 at API prices (estimated by Claude Code)", "of your 5-hour limit", "of your weekly limit (estimated from API prices)"} {
		if !strings.Contains(msg, want) {
			t.Errorf("warning lacks %q: %q", want, msg)
		}
	}
	if len(msg) > coldWarnMaxBytes {
		t.Errorf("warning is %d bytes, cap %d", len(msg), coldWarnMaxBytes)
	}
	if !strings.HasSuffix(msg, ".") {
		t.Errorf("the worst-case warning was cut: %q", msg)
	}
}

// Rule 15: the declared per-warning overhead covers the capped warning.
func TestSwitchWarningFitsItsDeclaredOverhead(t *testing.T) {
	o, ok := technique.OverheadOf(config.OptimizerHookModelSwitch)
	if !ok || o.PerSession != 0 || o.PerUse < estTokens(coldWarnMaxBytes) {
		t.Errorf("declared %+v (ok %v); a warning can cost %d tokens", o, ok, estTokens(coldWarnMaxBytes))
	}
}

func TestModelSwitchOncePerSessionAndOutcome(t *testing.T) {
	r := newGuardRig(t, config.OptimizerHookModelSwitch, "on")
	r.begin("startup", false, 0, 0)
	if msg := switchReply(t, first(r.switchModel("claude-opus-5", "command", 182340, true))); msg == "" {
		t.Fatal("no warning")
	}
	r.switched("auto") // Claude Code's own fallback is not the user's answer
	r.switched("command")
	r.switched("command") // counted once
	if msg := switchReply(t, first(r.switchModel("claude-sonnet-5-5", "command", 190000, true))); msg != "" {
		t.Fatalf("warned twice in a session: %q", msg)
	}
	if got := guardKinds(r.rig, config.OptimizerHookModelSwitch); strings.Join(got, ",") != "advised,overridden" {
		t.Errorf("kinds %v", got)
	}
}

func first(s string, _ int) string { return s }

func TestModelSwitchRespectsPinsAndKillSwitch(t *testing.T) {
	r := newGuardRig(t, config.OptimizerHookModelSwitch, "on")
	if out, _ := r.switchModel("claude-opus-5", "command", 182340, true); out != "" {
		t.Fatalf("acted in a session it was never pinned for: %q", out)
	}
	r.begin("startup", false, 0, 0)
	r.env = func(k string) string {
		if k == config.EnvOptimize {
			return "off"
		}
		return ""
	}
	if out, _ := r.switchModel("claude-opus-5", "command", 182340, true); out != "" {
		t.Fatalf("the kill switch must turn it off: %q", out)
	}
}

// Rule 4, and more: a PreModelSwitch hook that times out blocks the switch. On any failure the hook prints nothing
// and exits 0 well before its 5 s timeout.
func TestModelSwitchFailsOpen(t *testing.T) {
	t.Run("panic", func(t *testing.T) {
		r := newGuardRig(t, config.OptimizerHookModelSwitch, "on")
		r.begin("startup", false, 0, 0)
		coldWarnTestHook = func() { panic("boom") }
		t.Cleanup(func() { coldWarnTestHook = nil })
		if out, code := r.switchModel("claude-opus-5", "command", 182340, true); out != "" || code != 0 {
			t.Fatalf("out=%q code=%d", out, code)
		}
	})
	t.Run("stalled", func(t *testing.T) {
		r := newGuardRig(t, config.OptimizerHookModelSwitch, "on")
		r.begin("startup", false, 0, 0)
		callDeadline = 20 * time.Millisecond
		release := make(chan struct{})
		coldWarnTestHook = func() { <-release }
		t.Cleanup(func() {
			close(release)
			time.Sleep(50 * time.Millisecond)
			coldWarnTestHook = nil
		})
		start := time.Now()
		out, code := r.switchModel("claude-opus-5", "command", 182340, true)
		if out != "" || code != 0 || time.Since(start) > 2*time.Second {
			t.Fatalf("out=%q code=%d after %v", out, code, time.Since(start))
		}
	})
	t.Run("damaged inputs", func(t *testing.T) {
		for _, in := range []string{"", "{", `{"session_id":"x","hook_event_name":"PreModelSwitch","context_tokens":"many"}`} {
			r := newGuardRig(t, config.OptimizerHookModelSwitch, "on")
			if out, code := r.run(EventModelSwitch, in); out != "" || code != 0 {
				t.Errorf("%q: out=%q code=%d", in, out, code)
			}
		}
		r := newGuardRig(t, config.OptimizerHookModelSwitch, "on")
		r.begin("startup", false, 0, 0)
		_ = os.WriteFile(filepath.Join(r.dir, limitfit.CacheFile), []byte("{"), 0o600)
		if out, code := r.switchModel("claude-opus-5", "command", 182340, true); code != 0 || (out != "" && switchReply(t, out) == "") {
			t.Fatalf("a damaged limit fit: out=%q code=%d", out, code)
		}
	})
}

func TestModelSwitchTiming(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	r := newGuardRig(t, config.OptimizerHookModelSwitch, "on")
	callDeadline = 150 * time.Millisecond // the real safety net
	r.begin("startup", false, 0, 0)
	in := r.fill("claude_pre_model_switch.json", "{{TO}}", "claude-opus-5", "{{SOURCE}}", "command", "{{TOKENS}}", "182340", "{{WARM}}", "true")
	var ds []time.Duration
	for i := 0; i < 40; i++ {
		_ = os.RemoveAll(coldMarkerDir(r.dir))
		var out bytes.Buffer
		start := time.Now()
		Main(Env{Args: []string{EventModelSwitch, "--data-dir", r.dir}, Stdin: strings.NewReader(in), Stdout: &out, Getenv: r.env, Now: func() time.Time { return r.now }})
		ds = append(ds, time.Since(start))
	}
	sortDurations(ds)
	p95 := ds[len(ds)*95/100]
	t.Logf("model-switch guard warning: p50=%v p95=%v max=%v", ds[len(ds)/2], p95, ds[len(ds)-1])
	if runtime.GOOS != "windows" && p95 > 50*time.Millisecond {
		t.Errorf("p95 = %v", p95)
	}
}
