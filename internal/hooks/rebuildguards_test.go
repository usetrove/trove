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

// compact runs PreCompact through Main with a stderr writer, as `trove hook` does.
func (r *coldRig) compact(trigger string) (stdout, stderr string, code int) {
	r.t.Helper()
	var out, errb bytes.Buffer
	code = Main(Env{Args: []string{EventCompactCheck, "--data-dir", r.dir}, Stdin: strings.NewReader(r.fill("claude_pre_compact_trigger.json", "{{TRIGGER}}", trigger)),
		Stdout: &out, Stderr: &errb, Getenv: r.env, Now: func() time.Time { return r.now }})
	return out.String(), errb.String(), code
}

func TestColdCompactGuardModes(t *testing.T) {
	cases := []struct {
		name     string
		state    string
		trigger  string
		now      time.Time
		wantCode int
		wantKind string
	}{
		{"off", "off", "manual", coldLastRequest.Add(2 * time.Hour), 0, ""},
		{"shadow, cold", "shadow", "manual", coldLastRequest.Add(2 * time.Hour), 0, KindWouldBlock},
		{"on, cold", "on", "manual", coldLastRequest.Add(2 * time.Hour), 2, KindBlocked},
		{"on, warm: the summary reads the cache", "on", "manual", coldLastRequest.Add(30 * time.Minute), 0, ""},
		{"on, auto: never stopped", "on", "auto", coldLastRequest.Add(2 * time.Hour), 0, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newGuardRig(t, config.OptimizerHookColdCompact, c.state)
			r.now = c.now
			r.begin("startup", false, 0, 0)
			out, msg, code := r.compact(c.trigger)
			if code != c.wantCode || out != "" {
				t.Fatalf("code %d (want %d), stdout %q, stderr %q", code, c.wantCode, out, msg)
			}
			if (msg != "") != (c.wantCode == 2) {
				t.Errorf("stderr %q", msg)
			}
			var want []string
			if c.wantKind != "" {
				want = []string{c.wantKind}
			}
			if got := guardKinds(r.rig, config.OptimizerHookColdCompact); strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("kinds %v, want %v", got, want)
			}
		})
	}
}

func TestColdCompactMessage(t *testing.T) {
	r := newGuardRig(t, config.OptimizerHookColdCompact, "on")
	r.now = coldLastRequest.Add(2 * time.Hour)
	r.begin("startup", false, 0, 0)
	_, msg, code := r.compact("manual")
	if code != 2 {
		t.Fatalf("code %d", code)
	}
	for _, want := range []string{"1-hour prompt cache lifetime", "uncached input", "about 163,000 tokens", "(estimated by Trove)",
		"type /compact again", "/clear", "--- note ---", "Files edited: internal/upload/upload.go"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
}

// The second /compact goes through and counts as compacting anyway; a /clear after a stop counts as cleared.
func TestColdCompactOncePerSessionAndOutcomes(t *testing.T) {
	r := newGuardRig(t, config.OptimizerHookColdCompact, "on")
	r.now = coldLastRequest.Add(2 * time.Hour)
	r.begin("startup", false, 0, 0)
	if _, _, code := r.compact("manual"); code != 2 {
		t.Fatal("first /compact not stopped")
	}
	if _, _, code := r.compact("manual"); code != 0 {
		t.Fatal("second /compact stopped")
	}
	if _, _, code := r.compact("manual"); code != 0 {
		t.Fatal("third /compact stopped")
	}
	if got := guardKinds(r.rig, config.OptimizerHookColdCompact); strings.Join(got, ",") != "blocked,overridden" {
		t.Errorf("kinds %v", got)
	}

	c := newGuardRig(t, config.OptimizerHookColdCompact, "on")
	c.now = coldLastRequest.Add(2 * time.Hour)
	c.begin("startup", false, 0, 0)
	if _, _, code := c.compact("manual"); code != 2 {
		t.Fatal("not stopped")
	}
	h := &handler{dir: c.dir, agent: AgentClaude, now: func() time.Time { return c.now }}
	h.coldSessionEnd(Input{Reason: "clear"}, c.key())
	if got := guardKinds(c.rig, config.OptimizerHookColdCompact); strings.Join(got, ",") != "blocked,cleared" {
		t.Errorf("kinds %v", got)
	}
}

// Rule 4: errors, panics and the deadline end in exit 0, and without a stderr writer nothing is ever stopped.
func TestColdCompactFailsOpen(t *testing.T) {
	t.Run("no stderr", func(t *testing.T) {
		r := newGuardRig(t, config.OptimizerHookColdCompact, "on")
		r.now = coldLastRequest.Add(2 * time.Hour)
		r.begin("startup", false, 0, 0)
		if out, code := r.run(EventCompactCheck, r.fill("claude_pre_compact_trigger.json", "{{TRIGGER}}", "manual")); code != 0 || out != "" {
			t.Fatalf("a block nobody can read: code %d out %q", code, out)
		}
	})
	t.Run("panic", func(t *testing.T) {
		r := newGuardRig(t, config.OptimizerHookColdCompact, "on")
		r.now = coldLastRequest.Add(2 * time.Hour)
		r.begin("startup", false, 0, 0)
		coldCompactTestHook = func() { panic("boom") }
		t.Cleanup(func() { coldCompactTestHook = nil })
		if _, msg, code := r.compact("manual"); code != 0 || msg != "" {
			t.Fatalf("code %d stderr %q", code, msg)
		}
	})
	t.Run("stalled", func(t *testing.T) {
		r := newGuardRig(t, config.OptimizerHookColdCompact, "on")
		r.now = coldLastRequest.Add(2 * time.Hour)
		r.begin("startup", false, 0, 0)
		old := postDeadline
		postDeadline = 20 * time.Millisecond
		release := make(chan struct{})
		coldCompactTestHook = func() { <-release }
		t.Cleanup(func() {
			close(release)
			time.Sleep(50 * time.Millisecond)
			postDeadline, coldCompactTestHook = old, nil
		})
		if _, msg, code := r.compact("manual"); code != 0 || msg != "" {
			t.Fatalf("code %d stderr %q", code, msg)
		}
	})
	t.Run("damaged transcript", func(t *testing.T) {
		r := newGuardRig(t, config.OptimizerHookColdCompact, "on")
		r.now = coldLastRequest.Add(2 * time.Hour)
		r.begin("startup", false, 0, 0)
		_ = os.WriteFile(r.transcript, []byte("\x00{nope\n"), 0o600)
		if _, msg, code := r.compact("manual"); code != 0 || msg != "" {
			t.Fatalf("code %d stderr %q", code, msg)
		}
	})
}

// Rule 7: the note and the session's text never reach a file.
func TestColdCompactWritesNoContent(t *testing.T) {
	r := newGuardRig(t, config.OptimizerHookColdCompact, "on")
	r.now = coldLastRequest.Add(2 * time.Hour)
	r.begin("startup", false, 0, 0)
	if _, _, code := r.compact("manual"); code != 2 {
		t.Fatal("not stopped")
	}
	_ = filepath.Walk(r.dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		b, _ := os.ReadFile(p)
		for _, bad := range []string{"retry with backoff", "upload_test.go", "jitter", sessionID, r.files} {
			if bytes.Contains(b, []byte(bad)) {
				t.Errorf("%s contains %q", filepath.Base(p), bad)
			}
		}
		return nil
	})
}
