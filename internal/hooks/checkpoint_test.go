package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/gateway/session"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/recovery"
	"github.com/usetrove/trove/internal/store"
	"github.com/usetrove/trove/internal/technique"
)

// ckRig drives the session checkpoint through Main with inputs shaped like Claude Code's (code.claude.com/docs/en/
// hooks, read 2026-11-26): SessionStart with a source, PreCompact with a trigger, SessionEnd with a reason.
type ckRig struct {
	*rig
	transcript string
	id         string // the current session id
}

// ckSession returns a session id whose arm in the checkpoint's A/B is want ("on" or "off").
func ckSession(t *testing.T, want string, skip ...string) string {
	t.Helper()
next:
	for i := 0; i < 1000; i++ {
		id := fmt.Sprintf("ckpt-session-%d", i)
		for _, s := range skip {
			if s == id {
				continue next
			}
		}
		if measure.Arm(session.ClaudeCodeKey(id), config.OptimizerHookSessionCheckpoint) == want {
			return id
		}
	}
	t.Fatal("no session id in that arm")
	return ""
}

func newCkRig(t *testing.T, state string, store bool, arm string) *ckRig {
	t.Helper()
	r := &ckRig{rig: newRig(t, nil)}
	old, oldPost := callDeadline, postDeadline
	callDeadline, postDeadline = 10*time.Second, 10*time.Second // decisions, not speed; TestCheckpointFailsOpen covers errors
	t.Cleanup(func() { callDeadline, postDeadline = old, oldPost })
	r.snapshot(Snapshot{States: map[string]string{config.OptimizerHookSessionCheckpoint: state}, Checkpoints: store})
	// The same settings in config.toml, because session end rebuilds the snapshot from it (and this checks that
	// recovery.session_checkpoints reaches the snapshot).
	toml := fmt.Sprintf("[optimizers]\n%s = %q\n\n[recovery]\nsession_checkpoints = %t\n", config.OptimizerHookSessionCheckpoint, state, store)
	if err := os.WriteFile(filepath.Join(r.dir, "config.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join("testdata", "checkpoint_transcript.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	r.transcript = filepath.Join(r.files, "s.jsonl")
	if err := os.WriteFile(r.transcript, []byte(strings.ReplaceAll(string(b), "{{CWD}}", jsonEscape(r.files))), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(r.files, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	r.file(filepath.Join(".git", "HEAD"), "ref: refs/heads/fix/retry-test\n")
	r.id = ckSession(t, arm)
	r.now = time.Date(2026, 10, 30, 11, 5, 0, 0, time.UTC)
	return r
}

func (r *ckRig) in(event string, kv ...any) string {
	r.t.Helper()
	m := map[string]any{"session_id": r.id, "transcript_path": r.transcript, "cwd": r.files, "hook_event_name": event}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// start runs SessionStart with source and returns the additionalContext it added.
func (r *ckRig) start(source string) string {
	r.t.Helper()
	out, code := r.run(EventReset, r.in("SessionStart", "source", source, "model", "claude-opus-5-5"))
	if code != 0 {
		r.t.Fatalf("exit %d", code)
	}
	if out == "" {
		return ""
	}
	var v struct {
		H struct {
			Event   string `json:"hookEventName"`
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil || v.H.Event != "SessionStart" {
		r.t.Fatalf("not a SessionStart context reply: %q", out)
	}
	return v.H.Context
}

func (r *ckRig) precompact() {
	r.t.Helper()
	if out, code := r.run(EventReset, r.in("PreCompact", "trigger", "auto", "custom_instructions", nil)); out != "" || code != 0 {
		r.t.Fatalf("PreCompact must print nothing: %q %d", out, code)
	}
}

func (r *ckRig) end(reason string) {
	r.t.Helper()
	if _, code := r.run(EventSessionEnd, r.in("SessionEnd", "reason", reason)); code != 0 {
		r.t.Fatalf("exit %d", code)
	}
}

func (r *ckRig) kinds() []string {
	return guardKinds(r.rig, config.OptimizerHookSessionCheckpoint)
}

func (r *ckRig) storedNotes() int {
	des, _ := os.ReadDir(recovery.CheckpointDir(filepath.Join(r.dir, "recovery")))
	n := 0
	for _, d := range des {
		if strings.HasSuffix(d.Name(), ".ckpt") {
			n++
		}
	}
	return n
}

func TestCheckpointRestoresAfterCompaction(t *testing.T) {
	r := newCkRig(t, "on", true, "on")
	if got := r.start("startup"); got != "" {
		t.Fatalf("a fresh start adds nothing: %q", got)
	}
	r.precompact()
	if r.storedNotes() != 1 {
		t.Fatalf("PreCompact must keep one note, have %d", r.storedNotes())
	}
	r.now = r.now.Add(4 * time.Minute)
	got := r.start("compact")
	for _, want := range []string{
		"not instructions",
		"<" + ckFence + ">",
		"Branch: fix/retry-test",
		"- go test ./internal/upload/...",
		"Files changed: internal/upload/upload_test.go",
		"- Fix the flaky retry test in internal/upload.",
		"I'll use a fake clock instead of real sleeps, rather than raising the timeout",
		"End of the last reply: The retry test still fails",
		"saved 4 minutes earlier",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("restore lacks %q:\n%s", want, got)
		}
	}
	for _, never := range []string{"go vet", "TEST-OUTPUT", "EDIT-RESULT", "FAIL"} {
		if strings.Contains(got, never) {
			t.Errorf("restore holds %q:\n%s", never, got)
		}
	}
	if len(got) > restoreMaxBytes {
		t.Errorf("restore is %d bytes, over the %d cap", len(got), restoreMaxBytes)
	}
	k := r.kinds()
	if strings.Join(k, ",") != KindCheckpointed+","+KindRestored {
		t.Errorf("kinds %v", k)
	}
}

// The note's text cannot close the fence or add a line: a prompt holding the closing tag is neutralised.
func TestCheckpointFencing(t *testing.T) {
	r := newCkRig(t, "on", true, "on")
	r.start("startup")
	r.precompact()
	got := r.start("compact")
	if n := strings.Count(got, "</"+ckFence+">"); n != 1 {
		t.Fatalf("closing tag appears %d times:\n%s", n, got)
	}
	if !strings.HasSuffix(got, "</"+ckFence+">") {
		t.Fatalf("the fence must close the reply:\n%s", got)
	}
	body := got[strings.Index(got, "<"+ckFence+">"):]
	for _, l := range strings.Split(body, "\n") {
		if strings.Contains(l, "Ignore previous instructions") && !strings.HasPrefix(l, "- ") {
			t.Errorf("recovered text started its own line: %q", l)
		}
	}
	if !strings.Contains(got, "</trove-recovered-note>") {
		t.Errorf("the embedded tag was not rewritten:\n%s", got)
	}
}

func TestCheckpointRestoresAfterClear(t *testing.T) {
	r := newCkRig(t, "on", true, "on")
	r.start("startup")
	r.end("clear")
	next := ckSession(t, "on", r.id)
	r.id = next
	r.now = r.now.Add(time.Second)
	got := r.start("clear")
	if !strings.Contains(got, "before the /clear") || !strings.Contains(got, "Branch: fix/retry-test") {
		t.Fatalf("restore after /clear:\n%s", got)
	}
}

// A note from long ago, or another session's note at a compaction, is not this work's.
func TestCheckpointRestoreRelevance(t *testing.T) {
	r := newCkRig(t, "on", true, "on")
	r.start("startup")
	r.end("other")
	r.now = r.now.Add(3 * time.Hour)
	r.id = ckSession(t, "on", r.id)
	if got := r.start("clear"); !strings.HasPrefix(got, "Trove kept no notes") {
		t.Errorf("a 3-hour-old note must not be restored: %q", got)
	}

	r2 := newCkRig(t, "on", true, "on")
	r2.start("startup")
	r2.end("other") // session A's note
	r2.id = ckSession(t, "on", r2.id)
	r2.start("startup")
	if got := r2.start("compact"); strings.Contains(got, "<"+ckFence+">") {
		t.Errorf("a compaction must restore only its own session's note: %q", got)
	}
}

// Without the recovery store no text is written anywhere and the restore is the one-line pointer.
func TestCheckpointStoreOffPointsAndWritesNoText(t *testing.T) {
	r := newCkRig(t, "on", false, "on")
	r.start("startup")
	r.precompact()
	got := r.start("compact")
	if !strings.HasPrefix(got, "Trove kept no notes from before the compaction") || strings.Contains(got, "\n") {
		t.Fatalf("pointer: %q", got)
	}
	if len(got) > 300 {
		t.Errorf("the pointer is %d bytes; keep it one short line", len(got))
	}
	if r.storedNotes() != 0 {
		t.Fatal("a note was stored with the store off")
	}
	if k := r.kinds(); strings.Join(k, ",") != KindPointed {
		t.Errorf("kinds %v", k)
	}
	assertNoText(t, r.dir)
}

// Rule 7: nothing from the session reaches any file outside the recovery store, even with the store on.
func TestCheckpointWritesNoTextOutsideTheStore(t *testing.T) {
	r := newCkRig(t, "on", true, "on")
	r.start("startup")
	r.readFile(filepath.Join(r.files, "a.go"), "package a")
	r.precompact()
	r.start("compact")
	r.end("other")
	assertNoText(t, r.dir, filepath.Join(r.dir, "recovery"))
}

func assertNoText(t *testing.T, dir string, except ...string) {
	t.Helper()
	_ = filepath.Walk(dir, func(path string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		for _, e := range except {
			if strings.HasPrefix(path, e) {
				return nil
			}
		}
		b, _ := os.ReadFile(path) //nolint:gosec // test temp dir
		for _, s := range []string{"retry", "upload", "fake clock", "go test", "a.go", "fix/", "ckpt-session-"} {
			if strings.Contains(string(b), s) {
				t.Errorf("%s holds %q", path, s)
			}
		}
		return nil
	})
}

func TestCheckpointShadowAndOffArmRestoreNothing(t *testing.T) {
	for _, c := range []struct{ name, state, arm string }{{"shadow", "shadow", "on"}, {"on, off arm", "on", "off"}} {
		t.Run(c.name, func(t *testing.T) {
			r := newCkRig(t, c.state, true, c.arm)
			r.start("startup")
			r.precompact()
			if got := r.start("compact"); got != "" {
				t.Fatalf("added %q", got)
			}
			k := r.kinds()
			if len(k) == 0 || k[len(k)-1] != KindWouldRestore {
				t.Fatalf("kinds %v", k)
			}
			for _, e := range r.events() {
				if e.Kind == KindWouldRestore && (e.Bytes <= 0 || e.Tokens <= 0) {
					t.Errorf("would_restore must carry what it would have added: %+v", e)
				}
			}
			if c.state == "shadow" && r.storedNotes() != 0 {
				t.Error("shadow stored a note")
			}
		})
	}
}

func TestCheckpointOffDoesNothing(t *testing.T) {
	r := newCkRig(t, "off", true, "on")
	r.start("startup")
	r.precompact()
	if got := r.start("compact"); got != "" {
		t.Fatalf("added %q", got)
	}
	r.end("clear")
	if len(r.kinds()) != 0 || r.storedNotes() != 0 {
		t.Fatalf("off recorded %v, stored %d", r.kinds(), r.storedNotes())
	}
	if _, err := os.Stat(boundaryDir(r.dir)); err == nil {
		t.Error("off wrote a boundary record")
	}
}

func (r *ckRig) readFile(path, content string) {
	r.t.Helper()
	in := r.in("PostToolUse", "tool_name", "Read", "tool_input", map[string]any{"file_path": path},
		"tool_response", map[string]any{"type": "text", "file": map[string]any{"filePath": path, "content": content}})
	if _, code := r.run(EventPostRead, in); code != 0 {
		r.t.Fatal(code)
	}
}

func (r *ckRig) bashFail(cmd string) {
	r.t.Helper()
	in := r.in("PostToolUseFailure", "tool_name", "Bash", "tool_input", map[string]any{"command": cmd}, "error", "Exit code 1\nboom", "is_interrupt", false)
	if _, code := r.run(EventTestResult, in); code != 0 {
		r.t.Fatal(code)
	}
}

func (r *ckRig) edit(path string) {
	r.t.Helper()
	in := r.in("PostToolUse", "tool_name", "Edit", "tool_input", map[string]any{"file_path": path, "old_string": "a", "new_string": "b"})
	if _, code := r.run(EventPostEdit, in); code != 0 {
		r.t.Fatal(code)
	}
}

func (r *ckRig) windowEvents() map[string][]int64 {
	out := map[string][]int64{}
	for _, e := range r.events() {
		if e.Hook == config.OptimizerHookSessionCheckpoint {
			out[e.Kind] = append(out[e.Kind], e.Bytes)
		}
	}
	return out
}

func TestCheckpointWindowCounts(t *testing.T) {
	for _, arm := range []string{"on", "off"} {
		t.Run(arm, func(t *testing.T) {
			r := newCkRig(t, "on", true, arm)
			r.start("startup")
			a, b := filepath.Join(r.files, "a.go"), filepath.Join(r.files, "b.go")
			r.readFile(a, "A1")
			r.readFile(b, "B1")
			r.bashFail("go build ./...")
			r.precompact()
			r.start("compact")
			r.readFile(a, "A1")                  // 1: unchanged re-read
			r.readFile(b, "B2")                  // 2: changed, not counted
			r.bashFail("go   build ./...")       // 3: the same command again
			r.bashFail("go test ./...")          // 4: a new failure
			r.edit(a)                            // 5: recovered at call 5
			r.edit(b)                            // 6: only the first edit counts
			for i := 0; i < ckWindowCalls; i++ { // past the window nothing more is counted
				r.readFile(a, "A1")
			}
			got := r.windowEvents()
			if fmt.Sprint(got[KindRereadUnchanged]) != "[1 7 8 9 10 11 12 13 14 15 16 17 18 19 20]" {
				t.Errorf("rereads at %v", got[KindRereadUnchanged])
			}
			if fmt.Sprint(got[KindRepeatFailure]) != "[3]" || fmt.Sprint(got[KindRecovered]) != "[5]" {
				t.Errorf("events %v", got)
			}
		})
	}
}

func TestCheckpointFillMilestone(t *testing.T) {
	r := newCkRig(t, "on", true, "on")
	r.start("startup")
	prompt := func() {
		if _, code := r.run(EventPromptSubmit, r.in("UserPromptSubmit", "prompt_id", "p", "prompt", "go on")); code != 0 {
			t.Fatal(code)
		}
	}
	prompt() // 600k of a 1M window: past 50%
	prompt() // the same milestone is saved once
	n := 0
	for _, k := range r.kinds() {
		if k == KindCheckpointed {
			n++
		}
	}
	if n != 1 || r.storedNotes() != 1 {
		t.Fatalf("checkpoints %d, notes %d", n, r.storedNotes())
	}
	s, err := recovery.OpenCheckpoints(recovery.CheckpointDir(filepath.Join(r.dir, "recovery")), func() time.Time { return r.now })
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Latest(recovery.Slot(r.files), time.Hour)
	if err != nil || c.Reason != "fill_50" {
		t.Fatalf("note %+v, %v", c, err)
	}
}

func TestContextWindow(t *testing.T) {
	for m, want := range map[string]int64{"claude-opus-5-5": 1_000_000, "claude-sonnet-5-5[1m]": 1_000_000,
		"claude-fable-5-2": 1_000_000, "claude-haiku-5-5": 1_000_000, "claude-opus-4-1": 200_000, "": 200_000} {
		if got := contextWindow(m); got != want {
			t.Errorf("%q: %d, want %d", m, got, want)
		}
	}
}

// Rule 4: a broken store, a damaged note or a missing transcript never stops a session or fails a hook.
func TestCheckpointFailsOpen(t *testing.T) {
	t.Run("store folder is a file", func(t *testing.T) {
		r := newCkRig(t, "on", true, "on")
		if err := os.MkdirAll(filepath.Join(r.dir, "recovery"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(recovery.CheckpointDir(filepath.Join(r.dir, "recovery")), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		r.start("startup")
		r.precompact()
		if got := r.start("compact"); !strings.HasPrefix(got, "Trove kept no notes") {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("damaged note", func(t *testing.T) {
		r := newCkRig(t, "on", true, "on")
		r.start("startup")
		r.precompact()
		p := filepath.Join(recovery.CheckpointDir(filepath.Join(r.dir, "recovery")), recovery.Slot(r.files)+".ckpt")
		if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := r.start("compact"); strings.Contains(got, "<"+ckFence+">") {
			t.Fatalf("a damaged note was restored: %q", got)
		}
		if _, err := os.Stat(p); err == nil {
			t.Error("the damaged note was kept")
		}
	})
	t.Run("no transcript", func(t *testing.T) {
		r := newCkRig(t, "on", true, "on")
		r.transcript = filepath.Join(r.files, "missing.jsonl")
		r.start("startup")
		r.precompact()
		got := r.start("compact")
		// The branch is still known, so a short note is kept and restored.
		if !strings.Contains(got, "Branch: fix/retry-test") || strings.Contains(got, "Latest requests") {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("damaged boundary record", func(t *testing.T) {
		r := newCkRig(t, "on", true, "on")
		r.start("startup")
		r.precompact()
		p := filepath.Join(boundaryDir(r.dir), recovery.Slot(r.files)+".json")
		if err := os.WriteFile(p, []byte(`{"t":1,"r":{"x y":"z\nc"},"f":["\nw 1 1"]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		r.start("compact")
		j, _ := os.ReadFile(ckJournalPath(r.dir, session.ClaudeCodeKey(r.id))) //nolint:gosec // test temp dir
		if strings.Count(string(j), "w ") != 1 {
			t.Fatalf("a damaged record added journal lines:\n%s", j)
		}
	})
}

func TestRestoreFitsItsDeclaredOverhead(t *testing.T) {
	long := strings.Repeat("word ", 200)
	var c recovery.Checkpoint
	for i := 0; i < 20; i++ {
		c.Task = append(c.Task, long)
		c.Decisions = append(c.Decisions, long)
		c.Files = append(c.Files, long)
		c.FailingTests = append(c.FailingTests, long)
	}
	c.Branch, c.LastReply, c.SavedAt = long, long, time.Now().UnixMilli()
	got := renderRestore(c.Cap(), "clear", time.Now())
	if len(got) > restoreMaxBytes {
		t.Errorf("render is %d bytes, over %d", len(got), restoreMaxBytes)
	}
	if !strings.HasSuffix(got, "</"+ckFence+">") {
		t.Error("an over-long note lost its closing fence")
	}
	if restoreTokens(restoreMaxBytes) > RestoreTokens {
		t.Errorf("the cap can cost %d tokens; %d declared", restoreTokens(restoreMaxBytes), RestoreTokens)
	}
	o, ok := technique.OverheadOf(config.OptimizerHookSessionCheckpoint)
	if !ok || o.PerSession != 0 || o.PerUse != RestoreTokens {
		t.Errorf("registry declares %+v, want per use %d", o, RestoreTokens)
	}
	if restoreTokens(len(pointerText("compact"))) > RestoreTokens {
		t.Error("the pointer costs more than the declared overhead")
	}
}

func TestGitBranch(t *testing.T) {
	dir := t.TempDir()
	// The temp folder may itself sit inside a repository (a home folder under git); then the walk finds that one.
	if got, outer := gitBranch(dir), gitBranch(filepath.Dir(dir)); got != outer {
		t.Errorf("no repo of its own: %q, the folder above says %q", got, outer)
	}
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(dir, ".git"), 0o700))
	must(os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o600))
	sub := filepath.Join(dir, "a", "b")
	must(os.MkdirAll(sub, 0o700))
	if got := gitBranch(sub); got != "main" {
		t.Errorf("from a subfolder: %q", got)
	}
	must(os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("0123456789abcdef0123456789abcdef01234567\n"), 0o600))
	if got := gitBranch(dir); got != "detached at 0123456789ab" {
		t.Errorf("detached: %q", got)
	}
	wt, gd := t.TempDir(), t.TempDir()
	must(os.WriteFile(filepath.Join(gd, "HEAD"), []byte("ref: refs/heads/feature\n"), 0o600))
	must(os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+gd+"\n"), 0o600))
	if got := gitBranch(wt); got != "feature" {
		t.Errorf("worktree: %q", got)
	}
}

func TestCheckpointFilesAreOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	r := newCkRig(t, "on", true, "on")
	r.start("startup")
	r.precompact()
	d := recovery.CheckpointDir(filepath.Join(r.dir, "recovery"))
	fi, err := os.Stat(filepath.Join(d, recovery.Slot(r.files)+".ckpt"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("note mode %v, %v", fi, err)
	}
	if di, err := os.Stat(d); err != nil || di.Mode().Perm() != 0o700 {
		t.Fatalf("folder mode %v, %v", di, err)
	}
}

// Every kind the session checkpoint and the routine steer record reaches hook_events: validKind and the CHECK
// constraint (migrations 0028 and 0030) must agree, or the measurements read nothing.
func TestCheckpointAndSteerKindsAreIngested(t *testing.T) {
	r := newRig(t, nil)
	kinds := []string{KindCheckpointed, KindRestored, KindPointed, KindWouldRestore, KindRereadUnchanged, KindRepeatFailure,
		KindRecovered, KindSteered, KindWouldSteer, KindReprompted, KindTurnEnd}
	for i, k := range kinds {
		if err := record(r.dir, event{T: int64(i + 1), Key: "c-k", Hook: config.OptimizerHookSessionCheckpoint, Kind: k, Bytes: 3}); err != nil {
			t.Fatal(err)
		}
	}
	db, err := store.Open(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := IngestSpool(context.Background(), db, r.dir); err != nil {
		t.Fatal(err)
	}
	for _, k := range kinds {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM hook_events WHERE kind = ?`, k).Scan(&n); err != nil || n != 1 {
			t.Errorf("%s: %d rows, %v", k, n, err)
		}
	}
}
