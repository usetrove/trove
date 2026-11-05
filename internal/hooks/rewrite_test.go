package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/recovery"
)

// The inputs these tests use have the shape the hooks reference documents
// (Bash tool_response is {stdout, stderr, interrupted, isImage}); they are not
// captured from a live agent. `trove hooks probe --live --record-dir` writes
// real ones, and TestRecordedInputs runs against any that are committed.

const (
	rwQuiet = config.OptimizerHookQuietCommands
	rwLimit = config.OptimizerHookReadLimit
	rwNoise = config.OptimizerNoiseNormalizer
	rwTrim  = config.OptimizerOutputTrimmer
)

// allow records a live probe result in the rig's data dir.
func (r *rig) allow(entries ...Capability) {
	r.t.Helper()
	var c Capabilities
	for _, e := range entries {
		e.Agent, e.Method = "claude", "live"
		if e.InputRewrite == "" {
			e.InputRewrite = CapNo
		}
		if e.OutputRewrite == "" {
			e.OutputRewrite = CapNo
		}
		c.Entries = append(c.Entries, e)
	}
	if err := SaveCapabilities(r.dir, c); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) bash(command string) (string, int) {
	r.t.Helper()
	return r.run(EventLargeOutput, r.input("claude_pre_bash_pip.json", "{{COMMAND}}", command))
}

type hookOut struct {
	H struct {
		Event   string          `json:"hookEventName"`
		Updated json.RawMessage `json:"updatedInput"`
		Output  json.RawMessage `json:"updatedToolOutput"`
		Context string          `json:"additionalContext"`
		Perm    string          `json:"permissionDecision"`
	} `json:"hookSpecificOutput"`
}

func parseOut(t *testing.T, s string) hookOut {
	t.Helper()
	var o hookOut
	if err := json.Unmarshal([]byte(s), &o); err != nil {
		t.Fatalf("not hook output: %q: %v", s, err)
	}
	return o
}

func (r *rig) kinds() []string {
	var out []string
	for _, e := range r.events() {
		out = append(out, e.Hook+":"+e.Kind)
	}
	return out
}

func TestQuietFlagIsAddedAndEveryOtherFieldIsCarriedOver(t *testing.T) {
	r := newRig(t, map[string]string{rwQuiet: "on"})
	r.allow(Capability{Tool: "Bash", InputRewrite: CapYes})
	r.start()
	out, code := r.bash("pip install -r requirements.txt")
	if code != 0 {
		t.Fatal(code)
	}
	o := parseOut(t, out)
	if o.H.Event != "PreToolUse" || o.H.Perm != "" {
		t.Fatalf("a rewrite must not approve the call: %+v", o.H)
	}
	var in map[string]any
	if err := json.Unmarshal(o.H.Updated, &in); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"command": "pip install -r requirements.txt -q", "description": "Install dependencies",
		"timeout": float64(120000), "run_in_background": false}
	if fmt.Sprint(in) != fmt.Sprint(want) {
		t.Fatalf("updatedInput = %v; want %v (updatedInput replaces the whole input, so nothing may be dropped)", in, want)
	}
	if got := r.kinds(); len(got) != 1 || got[0] != rwQuiet+":rewritten" {
		t.Fatalf("events = %v", got)
	}
}

func TestQuietFlagStaysShadowWhenNotAllowedProbedOrVerified(t *testing.T) {
	cases := []struct {
		name    string
		state   string
		allow   bool
		command string
	}{
		{"shadow state", "shadow", true, "pip install x"},
		{"agent not probed", "on", false, "pip install x"},
		{"rule not verified", "on", true, "cargo build"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, map[string]string{rwQuiet: c.state})
			if c.allow {
				r.allow(Capability{Tool: "Bash", InputRewrite: CapYes})
			}
			r.start()
			if out, _ := r.bash(c.command); out != "" {
				t.Fatalf("nothing may be rewritten, got %q", out)
			}
			if got := r.kinds(); len(got) != 1 || got[0] != rwQuiet+":would_rewrite" {
				t.Fatalf("events = %v", got)
			}
		})
	}
}

func TestCommandsThatAreNotSimpleAreNeverRewritten(t *testing.T) {
	for _, cmd := range []string{
		"pip install x | tee log.txt", "pip install x && rm -rf build", "pip install x; ls", "pip install 'x y'", `pip install "$PKG"`,
		"pip install x > out.txt", "pip install $(cat reqs)", "pip install x &", "pip install -q x", "pip install --quiet x",
		"pip install -- -weird", "sudo pip install x", "FOO=1 pip install x", "pip install x\nrm -rf /", "npm install --silent", "pip", "echo pip install x",
	} {
		if q, _, ok := optimize.QuietRewrite(cmd); ok {
			t.Errorf("%q was rewritten to %q", cmd, q)
		}
	}
}

func TestEveryAllowlistRuleOnlyAddsItsFlags(t *testing.T) {
	for _, rule := range optimize.QuietRules {
		cmd := strings.Join(append(append([]string{}, rule.Prefix...), "pkg"), " ")
		q, got, ok := optimize.QuietRewrite(cmd)
		if !ok || got.Source == "" || got.VerifiedAt == "" {
			t.Errorf("%v: ok=%v source=%q verified_at=%q (every rule needs the page it was read from)", rule.Prefix, ok, got.Source, got.VerifiedAt)
			continue
		}
		want := cmd + " " + strings.Join(rule.Add, " ")
		if q != want {
			t.Errorf("%q -> %q; want %q", cmd, q, want)
		}
		for _, f := range rule.Add {
			if !strings.HasPrefix(f, "-") {
				t.Errorf("%v: %q is not a flag", rule.Prefix, f)
			}
		}
	}
}

func TestLargeReadGetsALimitAndSmallReadsDoNot(t *testing.T) {
	r := newRig(t, map[string]string{rwLimit: "on", config.OptimizerHookRepeatRead: "on"})
	r.allow(Capability{Tool: "Read", InputRewrite: CapYes})
	r.start()
	big := r.file("big.go", strings.Repeat("package x // a line of code\n", 3000)) // ~84 KB
	out, _ := r.run(EventPreRead, r.input("claude_pre_read.json", "{{FILE}}", big))
	o := parseOut(t, out)
	var in map[string]any
	_ = json.Unmarshal(o.H.Updated, &in)
	if in["limit"] != float64(optimize.ReadLimitLines) || in["file_path"] != big {
		t.Fatalf("updatedInput = %v", in)
	}
	if !strings.Contains(o.H.Context, "limited to the first 400 lines") || !strings.Contains(o.H.Context, "offset and limit") {
		t.Fatalf("the model must be told it got a partial read: %q", o.H.Context)
	}
	small := r.file("small.go", "package x\n")
	if out, _ := r.run(EventPreRead, r.input("claude_pre_read.json", "{{FILE}}", small)); out != "" {
		t.Fatalf("a small file must be read whole: %q", out)
	}
	// A read that already asks for a range is left alone.
	if out, _ := r.run(EventPreRead, r.input("claude_pre_read_range.json", "{{FILE}}", big)); out != "" {
		t.Fatalf("a read with its own range must not be changed: %q", out)
	}
}

func TestRepeatReadDenialWinsOverALimit(t *testing.T) {
	r := newRig(t, map[string]string{rwLimit: "on", config.OptimizerHookRepeatRead: "on"})
	r.allow(Capability{Tool: "Read", InputRewrite: CapYes})
	r.start()
	big := r.file("big.go", strings.Repeat("package x // a line of code\n", 3000))
	// First read passes (rewritten); the agent then reads the whole-range version again.
	r.run(EventPostRead, r.input("claude_post_read.json", "{{FILE}}", big, "{{PROMPT}}", "p1"))
	out, _ := r.run(EventPreRead, r.input("claude_pre_read.json", "{{FILE}}", big, "{{PROMPT}}", "p2"))
	o := parseOut(t, out)
	if o.H.Perm != "deny" || o.H.Updated != nil {
		t.Fatalf("expected a plain deny, got %s", out)
	}
	for _, k := range r.kinds() {
		if strings.HasSuffix(k, "rewritten") {
			t.Fatalf("a denied call must not count as rewritten: %v", r.kinds())
		}
	}
}

// ---- output

// goTestOutput is a failing `go test` run: many passing tests, one failure.
func goTestOutput(passing int) (text string, failureLines []string) {
	var b strings.Builder
	for i := 0; i < passing; i++ {
		fmt.Fprintf(&b, "=== RUN   TestCase%03d\n--- PASS: TestCase%03d (0.00s)\n", i, i)
	}
	fail := []string{
		"--- FAIL: TestBroken (0.02s)",
		"    broken_test.go:42: got 3, want 4",
		"panic: runtime error: index out of range [5] with length 3",
		"FAIL\tgithub.com/example/pkg\t0.321s",
	}
	b.WriteString("=== RUN   TestBroken\n" + strings.Join(fail, "\n") + "\n")
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&b, "=== RUN   TestAfter%03d\n--- PASS: TestAfter%03d (0.00s)\n", i, i)
	}
	b.WriteString("FAIL\n")
	return b.String(), fail
}

func (r *rig) post(fixture, stdout string, interrupted bool) (string, int) {
	r.t.Helper()
	j, _ := json.Marshal(stdout)
	return r.run(EventPostOutput, r.input(fixture, "{{STDOUT}}", strings.Trim(string(j), `"`), "{{INTERRUPTED}}", fmt.Sprint(interrupted)))
}

func trimRig(t *testing.T, troveRun bool) *rig {
	r := newRig(t, nil)
	r.snapshot(Snapshot{States: map[string]string{rwTrim: "on", rwNoise: "on"}, TroveRun: troveRun})
	r.allow(Capability{Tool: "Bash", OutputRewrite: CapYes}, Capability{Tool: "Grep", OutputRewrite: CapYes})
	r.start()
	return r
}

var markerRe = regexp.MustCompile(`\[Trove: (\d+) lines trimmed; full output sha256:([0-9a-f]{16})\]`)

func TestNoisyBashOutputIsTrimmedRecoverablyAndFailuresSurvive(t *testing.T) {
	r := trimRig(t, true)
	full, failures := goTestOutput(400)
	out, code := r.post("claude_post_bash.json", full, false)
	if code != 0 || out == "" {
		t.Fatalf("code %d output %q", code, out)
	}
	o := parseOut(t, out)
	if o.H.Event != "PostToolUse" {
		t.Fatal(o.H.Event)
	}
	var resp map[string]any
	if err := json.Unmarshal(o.H.Output, &resp); err != nil {
		t.Fatalf("updatedToolOutput must keep the tool's object shape: %s", o.H.Output)
	}
	if resp["interrupted"] != false || resp["isImage"] != false || resp["stderr"] != "" {
		t.Fatalf("fields other than the text must be carried over: %v", resp)
	}
	short := resp["stdout"].(string)
	if len(short) >= len(full)/2 {
		t.Fatalf("not shortened: %d of %d bytes", len(short), len(full))
	}
	for _, l := range failures {
		if !strings.Contains(short, l) {
			t.Errorf("the failure line %q was trimmed away", l)
		}
	}
	m := markerRe.FindStringSubmatch(short)
	if m == nil {
		t.Fatalf("no trim marker in:\n%s", short)
	}
	// The full text is in the recovery store under the id the marker names.
	rec, err := recovery.Open(filepath.Join(r.dir, "recovery"), recovery.Options{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := rec.Get(m[2], 1, 1<<20)
	if err != nil {
		t.Fatalf("trove_fetch_full could not get the original back: %v", err)
	}
	if strings.TrimRight(got, "\n") != strings.TrimRight(full, "\n") {
		t.Fatal("the stored original differs from the real output")
	}
	if k := r.kinds(); len(k) == 0 || k[len(k)-1] != rwTrim+":rewritten" {
		t.Fatalf("events = %v", k)
	}
	for _, e := range r.events() {
		if e.Kind == "rewritten" && (e.Bytes <= 0 || e.Tokens <= 0) {
			t.Fatalf("the saving must be recorded (estimated from sizes): %+v", e)
		}
	}
}

func TestTrimmingNeedsTheRecoveryPathTheProbeAndAnUninterruptedCommand(t *testing.T) {
	full, _ := goTestOutput(400)
	t.Run("MCP server not registered", func(t *testing.T) {
		r := trimRig(t, false)
		if out, _ := r.post("claude_post_bash.json", full, false); out != "" {
			t.Fatalf("output cannot be fetched back, so it must not be trimmed: %q", out)
		}
	})
	t.Run("agent not probed for Bash output", func(t *testing.T) {
		r := newRig(t, nil)
		r.snapshot(Snapshot{States: map[string]string{rwTrim: "on"}, TroveRun: true})
		r.allow(Capability{Tool: "Bash", OutputRewrite: CapNo})
		r.start()
		if out, _ := r.post("claude_post_bash.json", full, false); out != "" {
			t.Fatalf("an unprobed or failing capability must keep the optimizer in shadow: %q", out)
		}
		if k := r.kinds(); len(k) == 0 || k[len(k)-1] != rwTrim+":would_rewrite" {
			t.Fatalf("shadow should still estimate: %v", k)
		}
	})
	t.Run("interrupted command", func(t *testing.T) {
		r := trimRig(t, true)
		if out, _ := r.post("claude_post_bash.json", full, true); out != "" {
			t.Fatalf("%q", out)
		}
	})
	t.Run("recovery store cannot be opened", func(t *testing.T) {
		r := trimRig(t, true)
		if err := os.WriteFile(filepath.Join(r.dir, "recovery"), []byte("not a folder"), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, code := r.post("claude_post_bash.json", full, false); out != "" || code != 0 {
			t.Fatalf("fail open: out=%q code=%d", out, code)
		}
	})
	t.Run("short output", func(t *testing.T) {
		r := trimRig(t, true)
		if out, _ := r.post("claude_post_bash.json", "ok  \tpkg\t0.1s\n", false); out != "" {
			t.Fatalf("%q", out)
		}
	})
}

func TestSearchResultsAreNeverTrimmedEvenWhenTheyLookLikeTestOutput(t *testing.T) {
	r := trimRig(t, true)
	full, _ := goTestOutput(400) // a grep through a log of test output
	if out, _ := r.post("claude_post_grep.json", full, false); out != "" {
		t.Fatalf("Grep results are content the model asked for and must not be trimmed: %q", out)
	}
}

func TestEveryKindOfFailureLineSurvivesTrimming(t *testing.T) {
	failures := map[string][]string{
		"python": {"Traceback (most recent call last):", `  File "app.py", line 10, in run`, "AssertionError: 1 != 2", "FAILED tests/test_a.py::test_x - AssertionError"},
		"jest":   {"● suite › does the thing", "    Expected: 1", "    at Object.<anonymous> (src/a.test.js:12:5)", "Tests:       1 failed, 80 passed, 81 total"},
		"cargo":  {"error[E0308]: mismatched types", "thread 'main' panicked at src/main.rs:4:5:", "test result: FAILED. 80 passed; 1 failed"},
		"maven":  {"[ERROR] Tests run: 81, Failures: 1, Errors: 0", "[ERROR] BUILD FAILURE"},
	}
	for name, lines := range failures {
		t.Run(name, func(t *testing.T) {
			var b strings.Builder
			for i := 0; i < 300; i++ {
				fmt.Fprintf(&b, "--- PASS: TestOK%03d (0.00s)\nok  \tpkg%03d\t0.01s\n", i, i)
			}
			b.WriteString(strings.Join(lines, "\n") + "\n")
			for i := 0; i < 100; i++ {
				fmt.Fprintf(&b, "--- PASS: TestLater%03d (0.00s)\n", i)
			}
			text := b.String()
			short, _ := optimize.Condense(text)
			for _, l := range lines {
				if !strings.Contains(short, l) {
					t.Errorf("%q was trimmed away", l)
				}
			}
		})
	}
}

// TestRecordedInputs runs every input a live probe has recorded and committed
// under testdata/recorded/<version>/ through the hooks. Nothing is recorded in a
// fresh checkout, so it skips until `trove hooks probe --live --record-dir` has.
func TestRecordedInputs(t *testing.T) {
	dirs, _ := filepath.Glob(filepath.Join("testdata", "recorded", "*"))
	if len(dirs) == 0 {
		t.Skip("no live-recorded hook inputs committed yet; run: go run ./scripts/hookprobe --live --max-usd 0.25 --record-dir internal/hooks/testdata/recorded")
	}
	for _, d := range dirs {
		files, _ := filepath.Glob(filepath.Join(d, "*.json"))
		for _, f := range files {
			t.Run(filepath.Base(d)+"/"+filepath.Base(f), func(t *testing.T) {
				b, err := os.ReadFile(f)
				if err != nil {
					t.Fatal(err)
				}
				in, err := ParseInput(strings.NewReader(string(b)))
				if err != nil {
					t.Fatalf("the hook cannot parse a real input: %v", err)
				}
				if len(in.ToolInput.Raw) == 0 {
					t.Fatal("tool_input missing")
				}
				if strings.Contains(f, "_post") {
					if _, _, ok := responseTexts(in.ToolResponse); !ok {
						t.Logf("response shape not understood (the hook would let it through unchanged): %s", in.ToolResponse)
					}
				}
			})
		}
	}
}
