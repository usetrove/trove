package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/hookcaps"
	"github.com/usetrove/trove/internal/hooks"
)

// behaviour says what the fake agent does for one tool.
type behaviour struct {
	honourInput, honourOutput bool
	noCall                    bool // the model never calls the tool
}

// fakeAgent stands in for `claude`. It runs the probe hooks in process, the way
// the real agent would run them as commands, and decides what the "model" saw.
type fakeAgent struct {
	version string
	by      map[string]behaviour
	spent   float64
	calls   int
	// shapes are the tool_response shapes the fake returns, per tool.
	shapes map[string]string
}

var defaultShapes = map[string]string{
	"Bash": `{"stdout":"TROVE_PROBE_SRC\n","stderr":"","interrupted":false,"isImage":false}`,
	"Read": `{"type":"text","file":{"filePath":"/p/probe.txt","content":"L01 probe line\nL02 probe line","numLines":2}}`,
	"Grep": `{"mode":"files_with_matches","filenames":["/p/a.txt"],"numFiles":1}`,
	"Glob": `{"filenames":["/p/a.src"],"numFiles":1,"durationMs":3,"truncated":false}`,
	// Grep in output_mode "content". A guess, not a recording: no live probe has captured this shape yet, and the
	// documentation does not give it. Replace it with the recorded fixture once one exists.
	ToolGrepContent: `{"mode":"content","numFiles":1,"filenames":[],"content":"/p/a.txt:1:TROVE_PROBE_SRC is here","numLines":1}`,
}

func (f *fakeAgent) Run(_ context.Context, _ string, env []string, name string, args ...string) ([]byte, error) {
	if len(args) == 1 && args[0] == "--version" {
		if name == "codex" {
			return nil, os.ErrNotExist
		}
		return []byte(f.version + " (Claude Code)\n"), nil
	}
	f.calls++
	var state, tool string
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, StateEnv+"="); ok {
			state = v
		}
	}
	for i, a := range args {
		if a == "--tools" {
			tool = args[i+1]
		}
	}
	b := f.by[tool]
	if b.noCall {
		return []byte(`{"result":"I did not use the tool","total_cost_usd":0.01}`), nil
	}
	f.spent += 0.01
	var plan plan
	pb, _ := os.ReadFile(filepath.Join(state, "plan.json"))
	_ = json.Unmarshal(pb, &plan)
	getenv := func(k string) string {
		if k == StateEnv {
			return state
		}
		return ""
	}
	input := map[string]any{"pattern": "TROVE_PROBE_SRC", "command": "echo TROVE_PROBE_SRC", "file_path": "/p/probe.txt"}
	ib, _ := json.Marshal(input)
	pre, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": tool, "tool_input": json.RawMessage(ib),
		"session_id": "s1", "cwd": "/tmp/x", "tool_use_id": "t1"})
	var out bytes.Buffer
	HookMain("pre", bytes.NewReader(pre), &out, getenv)
	ran := json.RawMessage(ib)
	if b.honourInput && out.Len() > 0 {
		var o struct {
			H struct {
				U json.RawMessage `json:"updatedInput"`
			} `json:"hookSpecificOutput"`
		}
		_ = json.Unmarshal(out.Bytes(), &o)
		ran = o.H.U
	}
	shape := f.shapes[plan.Tool] // by probe case first: two cases can call the same tool
	if shape == "" {
		shape = defaultShapes[plan.Tool]
	}
	if shape == "" {
		shape = defaultShapes[tool]
	}
	post, _ := json.Marshal(map[string]any{"hook_event_name": "PostToolUse", "tool_name": tool, "tool_input": ran,
		"tool_response": json.RawMessage(shape), "session_id": "s1", "cwd": "/tmp/x"})
	out.Reset()
	HookMain("post", bytes.NewReader(post), &out, getenv)
	reply := "the original output"
	if b.honourOutput && out.Len() > 0 {
		reply = plan.MarkerOut
	}
	r, _ := json.Marshal(map[string]any{"result": reply, "total_cost_usd": 0.01})
	return r, nil
}

func opts(f *fakeAgent) Options {
	return Options{Exec: f, Live: true, APIKeyAuth: true, MaxUSD: 0.5, Now: func() time.Time { return time.Unix(1_800_000_000, 0) },
		HookCommand: func(mode string) string { return "fake " + mode }, Env: []string{}}
}

func entry(t *testing.T, r Report, tool string) hooks.Capability {
	t.Helper()
	for _, e := range r.Capabilities.Entries {
		if e.Agent == "claude" && e.Tool == tool {
			return e
		}
	}
	t.Fatalf("no entry for %s in %+v", tool, r.Capabilities.Entries)
	return hooks.Capability{}
}

func TestLiveProbeReportsEachCapabilityCorrectly(t *testing.T) {
	f := &fakeAgent{version: "2.1.999", by: map[string]behaviour{
		"Bash": {honourInput: true, honourOutput: true},  // everything works
		"Read": {honourInput: false, honourOutput: true}, // input ignored
		"Grep": {honourInput: true, honourOutput: false}, // output silently ignored
		"Glob": {noCall: true},                           // the model never called it
	}}
	rep, err := Run(context.Background(), opts(f))
	if err != nil {
		t.Fatal(err)
	}
	if rep.AgentVersion != "2.1.999" || !rep.Live {
		t.Fatalf("%+v", rep)
	}
	for _, c := range []struct {
		tool, in, out string
	}{
		{"Bash", hooks.CapYes, hooks.CapYes},
		{"Read", hooks.CapNo, hooks.CapYes},
		{"Grep", hooks.CapYes, hooks.CapNo},
		{"Glob", hooks.CapUntested, hooks.CapUntested},
	} {
		e := entry(t, rep, c.tool)
		if e.InputRewrite != c.in || e.OutputRewrite != c.out || e.Method != "live" || e.AgentVersion != "2.1.999" || e.Evidence == "" {
			t.Errorf("%s: input=%s output=%s method=%s evidence=%q; want input=%s output=%s", c.tool, e.InputRewrite, e.OutputRewrite, e.Method, e.Evidence, c.in, c.out)
		}
	}
	if rep.SpentUSD <= 0 || rep.SpentUSD > 0.5 {
		t.Fatalf("spent %v", rep.SpentUSD)
	}
	// What the probe found is what hooks are allowed to do.
	caps := rep.Capabilities
	if !caps.Supports("claude", "Bash", hooks.RewriteOutput) || caps.Supports("claude", "Grep", hooks.RewriteOutput) ||
		caps.Supports("claude", "Read", hooks.RewriteInput) || caps.Supports("claude", "Glob", hooks.RewriteInput) {
		t.Fatalf("Supports disagrees with the probe: %+v", caps)
	}
}

func TestResponseShapeTheProbeCannotBuildIsNotReportedAsIgnored(t *testing.T) {
	f := &fakeAgent{version: "2.1.999", by: map[string]behaviour{"Glob": {honourInput: true, honourOutput: true}},
		shapes: nil}
	defaultShapes["Glob"] = `{"somethingNew":1}`
	defer func() {
		defaultShapes["Glob"] = `{"filenames":["/p/a.src"],"numFiles":1,"durationMs":3,"truncated":false}`
	}()
	o := opts(f)
	o.Tools = []string{"Glob"}
	rep, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	e := entry(t, rep, "Glob")
	if e.OutputRewrite != hooks.CapUntested || !strings.Contains(e.Evidence, "somethingNew") {
		t.Fatalf("an unknown shape must be 'untested' with the field names, not 'no': %+v", e)
	}
}

func TestOfflineProbeRunsNothingAndSupportsNothing(t *testing.T) {
	f := &fakeAgent{version: "2.1.999"}
	o := opts(f)
	o.Live = false
	rep, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if f.calls != 0 {
		t.Fatalf("an offline probe ran %d sessions", f.calls)
	}
	for _, tool := range DefaultTools {
		e := entry(t, rep, tool)
		if e.Method != "docs" || e.InputRewrite != hooks.CapUntested || rep.Capabilities.Supports("claude", tool, hooks.RewriteInput) {
			t.Fatalf("%s: %+v", tool, e)
		}
	}
}

func TestLiveProbeNeedsASpendingCap(t *testing.T) {
	o := opts(&fakeAgent{version: "2.1.999"})
	o.MaxUSD = 0
	if _, err := Run(context.Background(), o); err == nil || !strings.Contains(err.Error(), "--max-usd") {
		t.Fatalf("err = %v", err)
	}
}

func TestDocsOnlyAgentsAreRecordedButUnlockNothing(t *testing.T) {
	o := opts(&fakeAgent{version: "2.1.999"})
	o.Live = false
	rep, _ := Run(context.Background(), o)
	c := rep.Capabilities
	for _, agent := range []string{"codex", "cursor", "gemini"} {
		if _, ok := c.Find(agent, "Bash"); !ok {
			t.Errorf("%s: no docs entry recorded", agent)
		}
		for _, kind := range []string{hooks.RewriteInput, hooks.RewriteOutput} {
			if c.Supports(agent, "Bash", kind) {
				t.Errorf("%s Bash %s counts from documentation alone; it needs a live probe", agent, kind)
			}
		}
	}
	if e, _ := c.Find("codex", "Bash"); e.OutputRewrite != hooks.CapNo {
		t.Errorf("Codex documents no output replacement, got %q", e.OutputRewrite)
	}
}

// The cold-resume guard may block a prompt only after a real session showed it reaches a typed prompt and print-mode
// runs can be told apart; the documented entry is recorded for every probe, live or not, and unlocks nothing.
func TestPromptBlockIsRecordedFromTheDocsAndUnlocksNothing(t *testing.T) {
	for _, live := range []bool{false, true} {
		o := opts(&fakeAgent{version: "2.1.999"})
		o.Live = live
		rep, err := Run(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		e, ok := rep.Capabilities.Find("claude", hookcaps.ToolPrompt)
		if !ok {
			t.Fatalf("live=%v: no UserPromptSubmit entry", live)
		}
		if e.Method != "docs" || e.Block != hooks.CapUntested || e.InputRewrite != hooks.CapNo || e.AgentVersion != "2.1.999" {
			t.Errorf("live=%v: %+v", live, e)
		}
		if rep.Capabilities.Supports("claude", hookcaps.ToolPrompt, hookcaps.PromptBlock) {
			t.Errorf("live=%v: blocking counts from documentation alone", live)
		}
	}
}

func TestRecordedFixturesAreRedacted(t *testing.T) {
	dir := t.TempDir()
	o := opts(&fakeAgent{version: "2.1.999", by: map[string]behaviour{"Bash": {honourInput: true, honourOutput: true}}})
	o.Tools, o.RecordDir = []string{"Bash"}, dir
	if _, err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bash_pre.json", "bash_post.json"} {
		b, err := os.ReadFile(filepath.Join(dir, "2.1.999", name))
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		for _, leak := range []string{`"s1"`, "/tmp/x", `"t1"`} {
			if strings.Contains(s, leak) {
				t.Errorf("%s still contains %s:\n%s", name, leak, s)
			}
		}
		if !strings.Contains(s, "{{SESSION}}") {
			t.Errorf("%s: no placeholder:\n%s", name, s)
		}
	}
}

func TestHookNeverFailsTheCall(t *testing.T) {
	for _, in := range []string{"", "not json", `{"tool_name":"Bash"}`} {
		var out bytes.Buffer
		if code := HookMain("pre", strings.NewReader(in), &out, func(string) string { return t.TempDir() }); code != 0 || out.Len() != 0 {
			t.Errorf("input %q: code %d output %q", in, code, out.String())
		}
	}
	if code := HookMain("post", strings.NewReader("{}"), &bytes.Buffer{}, func(string) string { return "" }); code != 0 {
		t.Fatal(code)
	}
}

func TestRedactScrubsLocalPathsAnywhere(t *testing.T) {
	// Raw strings holding JSON: each backslash of a Windows path is written twice.
	in := `{"session_id":"abc","cwd":"C:\\Users\\someone\\AppData\\Local\\Temp\\trove-hookprobe-123\\project",` +
		`"tool_input":{"path":"C:\\Users\\someone\\AppData\\Local\\Temp\\trove-hookprobe-123\\project\\a.txt"},` +
		`"tool_response":{"stderr":"cwd was reset to /home/someone/tmp/trove-hookprobe-456/project","filenames":["/home/someone/tmp/trove-hookprobe-456/project/b"]}}`
	out, err := redact([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"someone", "Users", "/home", "AppData", "abc"} {
		if strings.Contains(string(out), leak) {
			t.Errorf("redacted fixture still contains %q:\n%s", leak, out)
		}
	}
	if !strings.Contains(string(out), "{{TMP}}") {
		t.Fatalf("nothing was replaced:\n%s", out)
	}
}

func TestLiveProbeIsRefusedWithoutAnAPIKeyAndRunsNothing(t *testing.T) {
	f := &fakeAgent{version: "2.1.999"}
	o := opts(f)
	o.APIKeyAuth = false
	_, err := Run(context.Background(), o)
	if !errors.Is(err, ErrLiveNeedsAPIKey) || !strings.Contains(err.Error(), "trove hooks verify") {
		t.Fatalf("err = %v", err)
	}
	if f.calls != 0 {
		t.Fatalf("a refused live probe ran %d sessions", f.calls)
	}
	// The documentation-only probe needs no key and still works.
	o.Live = false
	if _, err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
}

// recorder wraps an Executor and remembers every program the probe started.
type recorder struct {
	inner Executor
	calls [][]string
}

func (r *recorder) Run(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	return r.inner.Run(ctx, dir, env, name, args...)
}

// Codex CLI is never run for a model request by the probe, with or without --live:
// the only thing it does with `codex` is ask for its version. Codex also has no
// live path, so there is nothing that could use a ChatGPT login (PRINCIPLES.md rules 3
// and 10). If a live Codex probe is ever added it must require an OpenAI API key
// the way the Claude one requires APIKeyAuth, and this test must be updated with it.
func TestProbeOnlyEverAsksCodexForItsVersion(t *testing.T) {
	for _, live := range []bool{false, true} {
		f := &fakeAgent{version: "2.1.999"}
		rec := &recorder{inner: f}
		o := opts(f)
		o.Exec = rec
		o.Live = live
		if _, err := Run(context.Background(), o); err != nil {
			t.Fatal(err)
		}
		saw := false
		for _, c := range rec.calls {
			if c[0] != "codex" && !strings.HasSuffix(c[0], "codex") {
				continue
			}
			saw = true
			if len(c) != 2 || c[1] != "--version" {
				t.Errorf("live=%v: the probe ran codex with %v; it may only ask for --version", live, c[1:])
			}
		}
		if !saw {
			t.Errorf("live=%v: the probe never asked codex for its version; the check proves nothing", live)
		}
	}
}

func TestReplaceMarkerKnowsMCPShapes(t *testing.T) {
	for _, in := range []string{
		`{"content":[{"type":"text","text":"TROVE_PROBE_SRC is here"}]}`,
		`[{"type":"text","text":"TROVE_PROBE_SRC is here"}]`,
	} {
		out, note, ok := replaceMarker(json.RawMessage(in), "MARK")
		if !ok || !strings.Contains(string(out), `"text":"MARK"`) || !strings.Contains(string(out), `"type":"text"`) {
			t.Errorf("%s: %s %q %v", in, out, note, ok)
		}
	}
	if _, _, ok := replaceMarker(json.RawMessage(`[{"type":"image","data":"AA"}]`), "MARK"); ok {
		t.Error("an image block must not be replaced")
	}
}

func TestRewriteInputForTheNewCases(t *testing.T) {
	p := plan{Tool: ToolMCP, AgentTool: mcpProbeTool, MarkerIn: "TROVE_PROBE_IN_x"}
	out, ok := rewriteInput(p, json.RawMessage(`{"path":"/tmp/p/probe.txt"}`))
	if !ok || !strings.Contains(string(out), "TROVE_PROBE_IN_x.txt") || !inputApplied(p, out) {
		t.Fatalf("mcp: %s %v", out, ok)
	}
	p = plan{Tool: "PowerShell", MarkerIn: "TROVE_PROBE_IN_y"}
	out, ok = rewriteInput(p, json.RawMessage(`{"command":"Write-Output TROVE_PROBE_SRC"}`))
	if !ok || !strings.Contains(string(out), "Write-Output TROVE_PROBE_IN_y") {
		t.Fatalf("PowerShell: %s %v", out, ok)
	}
	// The large case keeps its command: rewriting it would make the output small.
	if _, ok := rewriteInput(plan{Tool: ToolBashLarge, AgentTool: "Bash"}, json.RawMessage(`{"command":"yes"}`)); ok {
		t.Fatal("the large Bash case must not rewrite its command")
	}
}

func TestLargeEvidenceSaysWhatTheHookSaw(t *testing.T) {
	full := strings.Repeat(bashLargeLine, bashLargeLines)
	whole, _ := json.Marshal(map[string]any{"stdout": full})
	if got := largeEvidence([]seen{{Mode: "post", Response: whole}}); !strings.Contains(got, "before Claude Code saves it") {
		t.Fatal(got)
	}
	short, _ := json.Marshal(map[string]any{"stdout": "Output too large. Full output saved to /x/tool-results/a.txt\n" + full[:2000]})
	if got := largeEvidence([]seen{{Mode: "post", Response: short}}); !strings.Contains(got, "less than the whole output") {
		t.Fatal(got)
	}
}

func TestGrepTextIsProbedOnItsOwnAndOnlyItsTextCounts(t *testing.T) {
	// Grep honours both rewrites. The text case gets its own entry, and only that entry unlocks Grep output rewrites.
	f := &fakeAgent{version: "2.1.999", by: map[string]behaviour{"Grep": {honourInput: true, honourOutput: true}}}
	o := opts(f)
	o.Tools = []string{"Grep", ToolGrepContent}
	rep, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if e := entry(t, rep, ToolGrepContent); e.InputRewrite != hooks.CapYes || e.OutputRewrite != hooks.CapYes {
		t.Fatalf("GrepContent: %+v", e)
	}
	if !rep.Capabilities.Supports("claude", hookcaps.ToolGrepContent, hooks.RewriteOutput) {
		t.Fatal("a probed text result must unlock Grep output rewrites")
	}

	// The model stayed in the default mode and returned a file list: the text case must not borrow the list's "yes".
	f = &fakeAgent{version: "2.1.999", by: map[string]behaviour{"Grep": {honourInput: true, honourOutput: true}},
		shapes: map[string]string{ToolGrepContent: defaultShapes["Grep"]}}
	o = opts(f)
	o.Tools = []string{ToolGrepContent}
	rep, err = Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	e := entry(t, rep, ToolGrepContent)
	if e.OutputRewrite != hooks.CapUntested || !strings.Contains(e.Evidence, "no text content; fields: filenames,mode,numFiles") {
		t.Fatalf("a file list must leave the text case untested: %+v", e)
	}
}

func TestReplaceGrepContentTouchesOnlyTheText(t *testing.T) {
	out, _, ok := replaceGrepContent(json.RawMessage(defaultShapes[ToolGrepContent]), "MARK")
	var m map[string]any
	if !ok || json.Unmarshal(out, &m) != nil || m["content"] != "MARK" || m["mode"] != "content" || m["numLines"] != float64(1) {
		t.Fatalf("%s %v", out, ok)
	}
	for _, in := range []string{`"text"`, `[{"type":"text","text":"x"}]`, `{"content":[{"type":"text","text":"x"}]}`} {
		if _, _, ok := replaceGrepContent(json.RawMessage(in), "MARK"); ok {
			t.Errorf("%s is not a Grep text result and must not be replaced", in)
		}
	}
}

func TestVerifySessionLeavesTheGrepTextCaseToTheLiveProbe(t *testing.T) {
	a, err := Arm(filepath.Join(t.TempDir(), "s"), ArmOptions{Tools: []string{"Grep", ToolGrepContent},
		HookCommand: func(mode, _ string) (string, []string) { return "fake", []string{mode} }})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Tools) != 1 || a.Tools[0] != "Grep" {
		t.Fatalf("armed %v", a.Tools)
	}
}
