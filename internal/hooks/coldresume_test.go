package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/hookcaps"
	"github.com/usetrove/trove/internal/measure/limitfit"
	"github.com/usetrove/trove/internal/store"
	"github.com/usetrove/trove/internal/technique"
)

// The transcript fixture (testdata/cold_transcript.jsonl) is written in the shape Claude Code's transcripts are
// observed to have (docs/logs.md), with made-up content. Its newest main-conversation request ends at 09:02:30
// and re-read 8 + 160,000 + 3,000 = 163,008 tokens, writing to the 1-hour cache.
var coldLastRequest = time.Date(2026, 10, 30, 9, 2, 30, 0, time.UTC)

const coldLastPrompt = 163008

type coldRig struct {
	*rig
	transcript string
	entry      string // CLAUDE_CODE_ENTRYPOINT
}

// newColdRig sets the guard's state and action, writes the transcript and, when canBlock, a capability record that
// says a block reaches a typed prompt on this version.
func newColdRig(t *testing.T, state, action string, canBlock bool) *coldRig {
	t.Helper()
	r := &coldRig{rig: newRig(t, nil), entry: "cli"}
	// These tests are about the guard's decisions, not its speed: on a loaded machine the 250 ms safety net would
	// let prompts through (correctly) and hide them. TestColdResumeFailsOpen and TestColdResumeTiming cover speed.
	old := postDeadline
	postDeadline = 10 * time.Second
	t.Cleanup(func() { postDeadline = old })
	r.env = func(k string) string {
		if k == "CLAUDE_CODE_ENTRYPOINT" {
			return r.entry
		}
		return ""
	}
	r.coldSnapshot(state, action)
	b, err := os.ReadFile(filepath.Join("testdata", "cold_transcript.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	r.transcript = filepath.Join(r.files, "s.jsonl")
	if err := os.WriteFile(r.transcript, []byte(strings.ReplaceAll(string(b), "{{CWD}}", jsonEscape(r.files))), 0o600); err != nil {
		t.Fatal(err)
	}
	if canBlock {
		r.capability(hookcaps.Yes)
	}
	return r
}

func (r *coldRig) coldSnapshot(state, action string) {
	r.snapshot(Snapshot{States: map[string]string{config.OptimizerHookColdResume: state},
		ColdResume: ColdResumeSettings{Action: action, MinTokens: 150000, MinWeekPct: 3}})
}

func (r *coldRig) capability(block string) {
	r.t.Helper()
	c := hookcaps.Capabilities{V: hookcaps.Version, Entries: []hookcaps.Capability{
		{Agent: "claude", Tool: hookcaps.ToolPrompt, Block: block, Method: "session", AgentVersion: "2.1.400"}}}
	b, _ := json.Marshal(c)
	if err := os.MkdirAll(filepath.Dir(hookcaps.Path(r.dir)), 0o700); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(hookcaps.Path(r.dir), b, 0o600); err != nil {
		r.t.Fatal(err)
	}
}

func jsonEscape(s string) string {
	b, _ := json.Marshal(s)
	return strings.Trim(string(b), `"`)
}

func (r *coldRig) fill(fixture string, kv ...string) string {
	return r.input(fixture, append(kv, "{{TRANSCRIPT}}", jsonEscape(r.transcript), "{{CWD}}", jsonEscape(r.files))...)
}

// begin runs SessionStart: a fresh start, or a resume with Claude Code's documented resume fields.
func (r *coldRig) begin(source string, expired bool, tokens int64, usd float64) {
	r.t.Helper()
	in := r.fill("claude_session_start.json", "{{SOURCE}}", source)
	if source == "resume" || source == "fork" {
		in = r.fill("claude_session_start_resume.json", "{{SOURCE}}", source, "{{SECONDS}}", "7200",
			"{{TOKENS}}", strconv.FormatInt(tokens, 10), "{{EXPIRED}}", strconv.FormatBool(expired),
			"{{USD}}", strconv.FormatFloat(usd, 'f', -1, 64))
	}
	if out, code := r.run(EventReset, in); out != "" || code != 0 {
		r.t.Fatalf("session start: out=%q code=%d", out, code)
	}
}

func (r *coldRig) prompt(id string) string {
	r.t.Helper()
	out, code := r.run(EventPromptSubmit, r.fill("claude_user_prompt_submit.json", "{{PROMPT}}", id))
	if code != 0 {
		r.t.Fatalf("exit %d", code)
	}
	return out
}

// reply is what the hook printed: "block" with its reason, "warn" with its message, or "".
func coldReply(t *testing.T, out string) (kind, text string) {
	t.Helper()
	if out == "" {
		return "", ""
	}
	var v struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
		System   string `json:"systemMessage"`
		H        any    `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("hook output is not JSON: %q", out)
	}
	if v.H != nil {
		t.Errorf("the guard must add nothing to the model's context, got %q", out)
	}
	switch {
	case v.Decision == "block" && v.Reason != "" && v.System == "":
		return "block", v.Reason
	case v.Decision == "" && v.System != "":
		return "warn", v.System
	}
	t.Fatalf("unexpected reply %q", out)
	return "", ""
}

func coldKinds(r *rig) []string {
	var out []string
	for _, e := range r.events() {
		if e.Hook == config.OptimizerHookColdResume {
			out = append(out, e.Kind)
		}
	}
	return out
}

func TestColdResumeModes(t *testing.T) {
	type scenario struct {
		name   string
		resume bool
		expire bool
		tokens int64
		now    time.Time
	}
	resumeBig := scenario{"resume, cold, large", true, true, 200000, coldLastRequest.Add(3 * time.Hour)}
	resumeSmall := scenario{"resume, cold, small", true, true, 20000, coldLastRequest.Add(3 * time.Hour)}
	resumeWarm := scenario{"resume, warm", true, false, 200000, coldLastRequest.Add(30 * time.Minute)}
	idleBig := scenario{"idle past the 1-hour lifetime", false, false, 0, coldLastRequest.Add(61 * time.Minute)}
	idleWarm := scenario{"idle within the 1-hour lifetime", false, false, 0, coldLastRequest.Add(50 * time.Minute)}

	cases := []struct {
		state, action string
		canBlock      bool
		entry         string
		sc            scenario
		wantReply     string
		wantKind      string
	}{
		{"off", "ask", true, "cli", resumeBig, "", ""},
		{"off", "warn", true, "cli", idleBig, "", ""},
		{"shadow", "ask", true, "cli", resumeBig, "", KindWouldBlock},
		{"shadow", "warn", true, "cli", resumeBig, "", KindWouldBlock},
		{"shadow", "ask", true, "cli", idleBig, "", KindWouldAdvise},
		{"on", "warn", true, "cli", resumeBig, "warn", KindAdvised},
		{"on", "warn", true, "cli", idleBig, "warn", KindAdvised},
		{"on", "ask", true, "cli", resumeBig, "block", KindBlocked},
		// A mid-session idle gap may be a turn Claude Code started itself: never blocked.
		{"on", "ask", true, "cli", idleBig, "warn", KindAdvised},
		// No record that a block reaches a typed prompt on this version: warn instead.
		{"on", "ask", false, "cli", resumeBig, "warn", KindAdvised},
		// A print-mode run, or one that does not say: nobody may be there to see a block.
		{"on", "ask", true, "sdk-cli", resumeBig, "warn", KindAdvised},
		{"on", "ask", true, "", resumeBig, "warn", KindAdvised},
		// Below both thresholds, or still warm: nothing at all.
		{"on", "ask", true, "cli", resumeSmall, "", ""},
		{"on", "ask", true, "cli", resumeWarm, "", ""},
		{"on", "warn", true, "cli", idleWarm, "", ""},
		{"shadow", "ask", true, "cli", idleWarm, "", ""},
	}
	for _, c := range cases {
		name := c.state + "/" + c.action + "/" + c.sc.name
		if !c.canBlock {
			name += "/no capability"
		}
		if c.entry != "cli" {
			name += "/entry=" + c.entry
		}
		t.Run(name, func(t *testing.T) {
			r := newColdRig(t, c.state, c.action, c.canBlock)
			r.entry = c.entry
			r.now = c.sc.now
			if c.sc.resume {
				r.begin("resume", c.sc.expire, c.sc.tokens, 0.75)
			} else {
				r.begin("startup", false, 0, 0)
			}
			kind, text := coldReply(t, r.prompt("p1"))
			if kind != c.wantReply {
				t.Errorf("reply = %q (%q), want %q", kind, text, c.wantReply)
			}
			var want []string
			if c.wantKind != "" {
				want = []string{c.wantKind}
			}
			if got := coldKinds(r.rig); strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("spool kinds = %v, want %v", got, want)
			}
		})
	}
}

// The resume figures are Claude Code's own; the idle figures come from the transcript and are priced by Trove.
func TestColdResumeFiguresAndTheirLabels(t *testing.T) {
	r := newColdRig(t, "on", "ask", true)
	r.now = coldLastRequest.Add(3 * time.Hour)
	r.begin("resume", true, 182345, 0.6789)
	kind, text := coldReply(t, r.prompt("p1"))
	if kind != "block" {
		t.Fatalf("want a block, got %q", kind)
	}
	for _, want := range []string{"about 182,000 tokens", "about $0.68 at API prices (estimated by Claude Code)", "send your prompt again", "/clear", "--- note ---"} {
		if !strings.Contains(text, want) {
			t.Errorf("block reason lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "limit") {
		t.Errorf("no limit fit exists, so no share of a limit may be shown:\n%s", text)
	}
	if e := r.events(); len(e) != 1 || e[0].Tokens != 182345 {
		t.Errorf("the event must carry Claude Code's token count: %+v", e)
	}

	idle := newColdRig(t, "on", "warn", false)
	idle.now = coldLastRequest.Add(2 * time.Hour)
	idle.begin("startup", false, 0, 0)
	kind, text = coldReply(t, idle.prompt("p1"))
	if kind != "warn" || !strings.Contains(text, "about 163,000 tokens") || !strings.Contains(text, "(estimated by Trove)") ||
		!strings.Contains(text, "1-hour prompt cache lifetime") {
		t.Errorf("idle warning: %q", text)
	}
}

// With a limit fit, the share of each limit is shown as a range with its basis, and the weekly low end alone can
// make a small rebuild worth stopping.
func TestColdResumeLimitShares(t *testing.T) {
	r := newColdRig(t, "on", "ask", true)
	r.now = coldLastRequest.Add(3 * time.Hour)
	if err := limitfit.WriteCache(r.dir, limitfit.Result{
		FiveHour: limitfit.MeterFit{Meter: limitfit.FiveHour, Tier: limitfit.TierPrices, PerUSD: &limitfit.Range{Point: 10, Lo: 8, Hi: 12}},
		Week:     limitfit.MeterFit{Meter: limitfit.SevenDay, Tier: limitfit.TierPrices, PerUSD: &limitfit.Range{Point: 5, Lo: 4, Hi: 6}},
	}); err != nil {
		t.Fatal(err)
	}
	r.begin("resume", true, 20000, 1.0) // below 150,000 tokens, but the week's low end is 4% >= 3%
	kind, text := coldReply(t, r.prompt("p1"))
	if kind != "block" {
		t.Fatalf("the weekly share should have triggered the guard, got %q", kind)
	}
	for _, want := range []string{"~10% (8–12%) of your 5-hour limit", "~5% (4–6%) of your weekly limit (estimated from API prices)"} {
		if !strings.Contains(text, want) {
			t.Errorf("reason lacks %q:\n%s", want, text)
		}
	}
}

func TestColdResumeActsOncePerSession(t *testing.T) {
	r := newColdRig(t, "on", "ask", true)
	r.now = coldLastRequest.Add(3 * time.Hour)
	r.begin("resume", true, 200000, 0.75)
	if k, _ := coldReply(t, r.prompt("p1")); k != "block" {
		t.Fatalf("first prompt: %q", k)
	}
	if out := r.prompt("p1"); out != "" {
		t.Fatalf("the prompt sent again must go through, got %q", out)
	}
	if out := r.prompt("p2"); out != "" {
		t.Fatalf("a later prompt must go through, got %q", out)
	}
	// The session is resumed again later, still cold: the marker survives, so it is never stopped twice.
	r.coldSnapshot("on", "ask")
	r.begin("resume", true, 250000, 0.9)
	if out := r.prompt("p3"); out != "" {
		t.Fatalf("stopped a second time: %q", out)
	}
	if got := coldKinds(r.rig); strings.Join(got, ",") != "blocked,overridden" {
		t.Errorf("kinds = %v", got)
	}
}

func TestColdResumeWarnsOncePerSession(t *testing.T) {
	r := newColdRig(t, "on", "warn", false)
	r.now = coldLastRequest.Add(2 * time.Hour)
	r.begin("startup", false, 0, 0)
	if k, _ := coldReply(t, r.prompt("p1")); k != "warn" {
		t.Fatalf("first prompt: %q", k)
	}
	if out := r.prompt("p2"); out != "" {
		t.Fatalf("warned twice: %q", out)
	}
}

// What the user did after a block: cleared the session, or left it. Session end moves the spool into the database.
func TestColdResumeOutcomeAtSessionEnd(t *testing.T) {
	for _, c := range []struct {
		reason string
		want   string
	}{
		{"clear", "blocked,cleared"},
		{"prompt_input_exit", "blocked"},
		{"other", "blocked"},
	} {
		t.Run(c.reason, func(t *testing.T) {
			r := newColdRig(t, "on", "ask", true)
			r.now = coldLastRequest.Add(3 * time.Hour)
			r.begin("resume", true, 200000, 0.75)
			if k, _ := coldReply(t, r.prompt("p1")); k != "block" {
				t.Fatalf("no block: %q", k)
			}
			if out, code := r.run(EventSessionEnd, r.fill("claude_session_end_reason.json", "{{REASON}}", c.reason)); out != "" || code != 0 {
				t.Fatalf("session end: %q %d", out, code)
			}
			db, err := store.Open(r.dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			rows, err := db.QueryContext(context.Background(), `SELECT kind, tokens FROM hook_events WHERE hook = ? ORDER BY id`, config.OptimizerHookColdResume)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rows.Close() }()
			var got []string
			for rows.Next() {
				var k string
				var n int64
				if err := rows.Scan(&k, &n); err != nil {
					t.Fatal(err)
				}
				if n != 200000 {
					t.Errorf("%s carries %d tokens, want the rebuild's 200000", k, n)
				}
				got = append(got, k)
			}
			if strings.Join(got, ",") != c.want {
				t.Errorf("hook_events = %v, want %s", got, c.want)
			}
		})
	}
}

// A session that started before the guard was installed has no pin and stays off (rule 5); the kill switch and a
// guardrail rollback lower the state at once.
func TestColdResumeRespectsPinsKillSwitchAndRollback(t *testing.T) {
	t.Run("no pin", func(t *testing.T) {
		r := newColdRig(t, "on", "ask", true)
		r.now = coldLastRequest.Add(3 * time.Hour)
		if out := r.prompt("p1"); out != "" {
			t.Fatalf("acted in a session it was never pinned for: %q", out)
		}
	})
	t.Run("config changed mid-session", func(t *testing.T) {
		r := newColdRig(t, "off", "ask", true)
		r.now = coldLastRequest.Add(3 * time.Hour)
		r.begin("startup", false, 0, 0)
		r.coldSnapshot("on", "ask")
		if out := r.prompt("p1"); out != "" {
			t.Fatalf("a change mid-session must wait for the next session: %q", out)
		}
	})
	t.Run("kill switch", func(t *testing.T) {
		r := newColdRig(t, "on", "ask", true)
		r.now = coldLastRequest.Add(3 * time.Hour)
		r.begin("resume", true, 200000, 0.75)
		r.env = func(k string) string {
			if k == config.EnvOptimize {
				return "off"
			}
			return ""
		}
		if out := r.prompt("p1"); out != "" || len(coldKinds(r.rig)) != 0 {
			t.Fatalf("the kill switch must turn the guard off at once: %q %v", out, coldKinds(r.rig))
		}
	})
	t.Run("rolled back", func(t *testing.T) {
		r := newColdRig(t, "on", "ask", true)
		r.now = coldLastRequest.Add(3 * time.Hour)
		r.begin("resume", true, 200000, 0.75)
		r.snapshot(Snapshot{States: map[string]string{config.OptimizerHookColdResume: "on"}, RolledBack: []string{config.OptimizerHookColdResume},
			ColdResume: ColdResumeSettings{Action: "ask"}})
		if out := r.prompt("p1"); out != "" {
			t.Fatalf("a rolled-back guard runs in shadow: %q", out)
		}
		if got := coldKinds(r.rig); strings.Join(got, ",") != KindWouldBlock {
			t.Errorf("kinds = %v", got)
		}
	})
}

// Rule 4: whatever goes wrong, the prompt goes through with no reply.
func TestColdResumeFailsOpen(t *testing.T) {
	t.Run("panic", func(t *testing.T) {
		r := newColdRig(t, "on", "ask", true)
		r.now = coldLastRequest.Add(3 * time.Hour)
		r.begin("resume", true, 200000, 0.75)
		coldResumeTestHook = func() { panic("boom") }
		t.Cleanup(func() { coldResumeTestHook = nil })
		if out, code := r.run(EventPromptSubmit, r.fill("claude_user_prompt_submit.json")); out != "" || code != 0 {
			t.Fatalf("out=%q code=%d", out, code)
		}
	})
	t.Run("past the deadline", func(t *testing.T) {
		r := newColdRig(t, "on", "ask", true)
		r.now = coldLastRequest.Add(3 * time.Hour)
		r.begin("resume", true, 200000, 0.75)
		old := postDeadline
		postDeadline = 20 * time.Millisecond
		release := make(chan struct{})
		coldResumeTestHook = func() { <-release }
		t.Cleanup(func() {
			close(release)
			time.Sleep(50 * time.Millisecond)
			postDeadline, coldResumeTestHook = old, nil
		})
		if out, code := r.run(EventPromptSubmit, r.fill("claude_user_prompt_submit.json")); out != "" || code != 0 {
			t.Fatalf("out=%q code=%d", out, code)
		}
	})
	t.Run("missing or damaged inputs", func(t *testing.T) {
		for _, c := range []struct {
			name  string
			setup func(r *coldRig)
		}{
			{"no transcript", func(r *coldRig) { r.transcript = filepath.Join(r.files, "missing.jsonl") }},
			{"garbage transcript", func(r *coldRig) { _ = os.WriteFile(r.transcript, []byte("\x00{not json\n[1,2]\n"), 0o600) }},
			{"damaged snapshot", func(r *coldRig) {
				_ = os.WriteFile(snapshotPath(r.dir), []byte("{"), 0o600)
			}},
			{"damaged limit fit", func(r *coldRig) {
				_ = os.WriteFile(filepath.Join(r.dir, limitfit.CacheFile), []byte("{"), 0o600)
			}},
			{"damaged capability record", func(r *coldRig) { _ = os.WriteFile(hookcaps.Path(r.dir), []byte("{"), 0o600) }},
		} {
			t.Run(c.name, func(t *testing.T) {
				r := newColdRig(t, "on", "ask", true)
				r.now = coldLastRequest.Add(3 * time.Hour)
				r.begin("startup", false, 0, 0) // the idle case reads the transcript
				c.setup(r)
				out, code := r.run(EventPromptSubmit, r.fill("claude_user_prompt_submit.json"))
				if code != 0 {
					t.Fatalf("exit %d", code)
				}
				if k, _ := coldReply(t, out); k == "block" {
					t.Fatalf("blocked on damaged input: %q", out)
				}
			})
		}
	})
	t.Run("marker cannot be written", func(t *testing.T) {
		r := newColdRig(t, "on", "ask", true)
		r.now = coldLastRequest.Add(3 * time.Hour)
		r.begin("resume", true, 200000, 0.75)
		if err := os.MkdirAll(filepath.Dir(coldMarkerDir(r.dir)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(coldMarkerDir(r.dir), []byte("not a folder"), 0o600); err != nil {
			t.Fatal(err)
		}
		if out := r.prompt("p1"); out != "" {
			t.Fatalf("without its once-per-session marker the guard must do nothing: %q", out)
		}
	})
}

// The note is built from the transcript in memory: typed prompts, edited files and the end of the last reply,
// never tool output, meta lines, slash-command wrappers or subagent text.
func TestColdNoteGolden(t *testing.T) {
	r := newColdRig(t, "on", "ask", true)
	note := buildColdNote(r.transcript, r.files)
	golden := filepath.Join("testdata", "cold_note.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(golden, []byte(note), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if note != strings.ReplaceAll(string(want), "\r\n", "\n") {
		t.Errorf("note changed; review it, then run with UPDATE_GOLDEN=1\ngot:\n%s", note)
	}
	for _, bad := range []string{"NEVER", r.files} {
		if strings.Contains(note, bad) {
			t.Errorf("the note contains %q", bad)
		}
	}
	if buildColdNote(filepath.Join(r.files, "missing.jsonl"), r.files) != "" {
		t.Error("no transcript, no note")
	}
}

func TestColdNoteIsCapped(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.jsonl")
	long := strings.Repeat("é", 2000)
	var b bytes.Buffer
	for i := 0; i < 5; i++ {
		line, _ := json.Marshal(map[string]any{"type": "user", "timestamp": "2026-10-30T09:00:00Z", "message": map[string]any{"role": "user", "content": long}})
		b.Write(append(line, '\n'))
	}
	for i := 0; i < 30; i++ {
		in, _ := json.Marshal(map[string]any{"file_path": filepath.Join(dir, "pkg", strings.Repeat("x", 40)+strconv.Itoa(i)+".go")})
		line, _ := json.Marshal(map[string]any{"type": "assistant", "timestamp": "2026-10-30T09:00:00Z", "message": map[string]any{
			"model": "claude-opus-5-5", "content": []map[string]any{{"type": "tool_use", "name": "Edit", "input": json.RawMessage(in)}, {"type": "text", "text": long}}}})
		b.Write(append(line, '\n'))
	}
	if err := os.WriteFile(p, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	note := buildColdNote(p, dir)
	if len(note) > coldNoteMaxBytes || !strings.HasPrefix(note, "Context from my previous session") {
		t.Errorf("note is %d bytes, cap %d", len(note), coldNoteMaxBytes)
	}
	if !utf8Valid(note) {
		t.Error("the cap split a character")
	}
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "\uFFFD") == s }

// Rule 15: the registry's declared overhead covers the largest note the user could paste.
func TestColdNoteFitsItsDeclaredOverhead(t *testing.T) {
	o, ok := technique.OverheadOf(config.OptimizerHookColdResume)
	if !ok {
		t.Fatal("hook_cold_resume declares no overhead")
	}
	if o.PerSession != 0 || o.PerUse < estTokens(coldNoteMaxBytes) {
		t.Errorf("declared %+v; the note can cost %d tokens", o, estTokens(coldNoteMaxBytes))
	}
}

// Rule 7: no prompt text, note text, file name or session id is written anywhere under the data dir.
func TestColdResumeWritesNoContent(t *testing.T) {
	r := newColdRig(t, "on", "ask", true)
	r.now = coldLastRequest.Add(3 * time.Hour)
	r.begin("resume", true, 200000, 0.75)
	if k, _ := coldReply(t, r.prompt("p1")); k != "block" {
		t.Fatal("no block")
	}
	r.prompt("p1")
	r.run(EventSessionEnd, r.fill("claude_session_end_reason.json", "{{REASON}}", "clear"))
	_ = filepath.Walk(r.dir, func(path string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		b, _ := os.ReadFile(path)
		for _, bad := range []string{"PROMPT-TEXT", "retry with backoff", "upload_test.go", "jitter", sessionID, r.files} {
			if bytes.Contains(b, []byte(bad)) {
				t.Errorf("%s contains %q", filepath.Base(path), bad)
			}
		}
		return nil
	})
	if _, err := os.Stat(filepath.Join(coldMarkerDir(r.dir), r.key())); err != nil {
		t.Errorf("the marker is named by the hashed key: %v", err)
	}
}

func TestDecideCold(t *testing.T) {
	s := coldSettings{action: config.ColdResumeAsk, minTokens: 150000, minWeekPct: 3}
	big := rebuild{tokens: 150000}
	small := rebuild{tokens: 149999}
	weekly := rebuild{tokens: 1000, limits: &limitfit.Estimate{Week: &limitfit.Share{Range: limitfit.Range{Point: 4, Lo: 3, Hi: 5}}}}
	weeklyLow := rebuild{tokens: 1000, limits: &limitfit.Estimate{Week: &limitfit.Share{Range: limitfit.Range{Point: 4, Lo: 2.9, Hi: 5}}}}
	cases := []struct {
		name     string
		state    string
		c        coldCase
		r        rebuild
		canBlock bool
		want     coldDecision
	}{
		{"at the token threshold", stateOn, caseResume, big, true, coldDecision{kind: KindBlocked, block: true}},
		{"just under it", stateOn, caseResume, small, true, coldDecision{}},
		{"weekly low end at the threshold", stateOn, caseResume, weekly, true, coldDecision{kind: KindBlocked, block: true}},
		{"weekly point over but low end under", stateOn, caseResume, weeklyLow, true, coldDecision{}},
		{"no case", stateOn, caseNone, big, true, coldDecision{}},
		{"idle never blocks", stateOn, caseIdle, big, true, coldDecision{kind: KindAdvised, warn: true}},
		{"shadow resume", stateShadow, caseResume, big, false, coldDecision{kind: KindWouldBlock}},
		{"shadow idle", stateShadow, caseIdle, big, false, coldDecision{kind: KindWouldAdvise}},
		{"off", stateOff, caseResume, big, true, coldDecision{}},
	}
	for _, c := range cases {
		if got := decideCold(c.state, s, c.c, c.r, c.canBlock); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
	if got := decideCold(stateOn, coldSettings{action: config.ColdResumeWarn, minTokens: 1, minWeekPct: 3}, caseResume, big, true); got.block {
		t.Error("warn must never block")
	}
}

func TestColdSettingsPin(t *testing.T) {
	for _, s := range []ColdResumeSettings{{}, {Action: "ask", MinTokens: 200000, MinWeekPct: 1.5}, {Action: "bogus"}} {
		got, ok := parseColdSettings(s.pin())
		if !ok || got != s.settings() {
			t.Errorf("%+v: round trip %+v %v", s, got, ok)
		}
		if strings.ContainsAny(s.pin(), " \n") {
			t.Errorf("a pin may not hold the ledger's separator: %q", s.pin())
		}
	}
	for _, bad := range []string{"", "ask", "ask|x|3", "block|1|1", "ask|0|3", "ask|1|0"} {
		if _, ok := parseColdSettings(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}

// The hook's worst case: it acts, on a transcript of over 1 MiB (the most it reads), building the note. It must
// stay well inside its 250 ms safety net, which itself is far inside the 5 s the install sets.
func TestColdResumeTiming(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	r := newColdRig(t, "on", "ask", true)
	r.now = coldLastRequest.Add(3 * time.Hour)
	line, _ := os.ReadFile(r.transcript)
	var big bytes.Buffer
	for big.Len() < 1<<20 {
		big.Write(line)
	}
	if err := os.WriteFile(r.transcript, big.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	in := r.fill("claude_user_prompt_submit.json")
	var ds []time.Duration
	for i := 0; i < 40; i++ {
		_ = os.RemoveAll(coldMarkerDir(r.dir))
		_ = os.Remove(sessionPath(r.dir, r.key()))
		r.begin("resume", true, 200000, 0.75)
		var out bytes.Buffer
		start := time.Now()
		Main(Env{Args: []string{EventPromptSubmit, "--data-dir", r.dir}, Stdin: strings.NewReader(in), Stdout: &out, Getenv: r.env, Now: func() time.Time { return r.now }})
		ds = append(ds, time.Since(start))
		if k, _ := coldReply(t, out.String()); k != "block" {
			t.Fatalf("run %d: no block (%q)", i, out.String())
		}
	}
	sortDurations(ds)
	p95 := ds[len(ds)*95/100]
	t.Logf("cold-resume hook acting, 1 MiB transcript: p50=%v p95=%v max=%v", ds[len(ds)/2], p95, ds[len(ds)-1])
	enforce := runtime.GOOS != "windows" // as in TestHandlerTiming: real-time antivirus adds a fixed cost per file open
	if enforce && p95 > 150*time.Millisecond {
		t.Errorf("acting: p95 = %v, want well under the 250 ms safety net", p95)
	}

	// The common case: a prompt while the cache is warm, which only reads the end of the transcript.
	r.now = coldLastRequest.Add(10 * time.Minute)
	_ = os.Remove(sessionPath(r.dir, r.key()))
	_ = os.RemoveAll(coldMarkerDir(r.dir))
	r.begin("startup", false, 0, 0)
	ds = ds[:0]
	for i := 0; i < 40; i++ {
		var out bytes.Buffer
		start := time.Now()
		Main(Env{Args: []string{EventPromptSubmit, "--data-dir", r.dir}, Stdin: strings.NewReader(in), Stdout: &out, Getenv: r.env, Now: func() time.Time { return r.now }})
		ds = append(ds, time.Since(start))
		if out.Len() != 0 {
			t.Fatalf("a warm prompt got %q", out.String())
		}
	}
	sortDurations(ds)
	p95 = ds[len(ds)*95/100]
	t.Logf("cold-resume hook passing a warm prompt, 1 MiB transcript: p50=%v p95=%v max=%v", ds[len(ds)/2], p95, ds[len(ds)-1])
	if enforce && p95 > 100*time.Millisecond {
		t.Errorf("passing: p95 = %v", p95)
	}
}

func sortDurations(d []time.Duration) {
	for i := 1; i < len(d); i++ {
		for j := i; j > 0 && d[j] < d[j-1]; j-- {
			d[j], d[j-1] = d[j-1], d[j]
		}
	}
}
