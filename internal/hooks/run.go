// Package hooks implements `trove hook <event>`, the small programs Claude Code
// and Codex CLI run around tool calls and at session end. They skip re-reads of
// unchanged files, suggest trove_run for noisy commands, and write the session
// receipt when a session closes.
//
// Rules every hook follows:
//   - Fail open. Any error, panic or timeout ends in exit 0 with no output, which
//     both agents read as "go ahead". The reason goes to <data dir>/hooks.log.
//   - Metadata only. State holds hashes, sizes and times; never file contents,
//     paths, commands or raw session ids.
//   - No database on the hot path. The per-call hooks read two small JSON files
//     and append counters to a spool that session end (or `trove hooks sync`)
//     moves into SQLite.
//
// Contract facts (verified 2026-10-29/30): see docs/hooks.md.
package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/usetrove/trove/internal/optimize"
	"io"
	"os"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/gateway/session"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/redact"
)

// Event names accepted by `trove hook <event>`.
const (
	EventPreRead  = "pre_tool_repeat_read"
	EventPostRead = "post_tool_read_record"
	// EventPostEdit records an edit for the session features; it never changes anything.
	EventPostEdit    = "post_tool_edit_record"
	EventReset       = "session_reset"
	EventSessionEnd  = "session_end_receipt"
	EventLargeOutput = "pre_tool_large_output"
	// EventPostOutput runs after Bash, Grep and Glob and may replace the output
	// the model sees (PostToolUse updatedToolOutput).
	EventPostOutput = "post_tool_output"
	// EventPostFetch runs after the trove_fetch_full MCP tool and counts fetch-backs
	// of output that a hook trimmed.
	EventPostFetch = "post_tool_fetch"
)

// Events lists every event name.
var Events = []string{EventPreRead, EventPostRead, EventPostEdit, EventReset, EventSessionEnd, EventLargeOutput, EventPostOutput, EventPostFetch}

const (
	// maxHashBytes is the largest file the repeat-read hook will hash. Bigger
	// files are always allowed, which keeps the hook fast.
	maxHashBytes = optimize.MaxHashBytes
	// endDeadline fits inside the 3 s timeout the installer sets on SessionEnd
	// (Claude Code's default budget is 1.5 s, Codex's 1 s with a 3 s maximum).
	endDeadline = 2500 * time.Millisecond
)

// callDeadline is the safety net for the per-call hooks. They normally take a
// few milliseconds; past this the agent is let through (fail open). It is a
// variable only so that tests which are about the hook's decision, not its
// speed, can lift it on a loaded machine; TestCallDeadlineLetsTheAgentThroughWhenAHookIsSlow keeps the real behavior covered.
var callDeadline = 150 * time.Millisecond

// postDeadline is the safety net for post_tool_output. It is longer than
// callDeadline because shortening output writes the full text to the recovery
// store first; past it the original output goes through untouched. It is a
// variable only so that tests, which run under a race detector and a loaded CI
// machine, are not decided by the clock.
var postDeadline = 250 * time.Millisecond

// Env is everything Main needs from its surroundings, so tests can supply them.
type Env struct {
	Args   []string // after "hook": the event, then flags
	Stdin  io.Reader
	Stdout io.Writer
	Getenv func(string) string
	Now    func() time.Time
	// Notify shows a desktop notification for a receipt, synchronously. May be nil.
	Notify func(measure.Receipt)
	// NotifyText shows a short desktop notification, synchronously. May be nil.
	NotifyText func(title, body string)
}

// Main runs one hook and returns the process exit code, which is always 0: a
// hook must never be the reason an agent stops (CLAUDE.md rule 4).
func Main(e Env) (code int) {
	// Exit code 2 would BLOCK the tool call, so nothing may escape as a panic.
	defer func() {
		if recover() != nil {
			code = 0
		}
	}()
	if e.Now == nil {
		e.Now = time.Now
	}
	if e.Getenv == nil {
		e.Getenv = os.Getenv
	}
	event, dataFlag, agent := parseArgs(e.Args)
	dir, err := config.ResolveDataDir(dataFlag, e.Getenv)
	if err != nil {
		return 0
	}
	deadline := callDeadline
	switch event {
	case EventSessionEnd:
		deadline = endDeadline
	case EventPostOutput:
		deadline = postDeadline
	}
	done := make(chan []byte, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logf(dir, event, "recovered from a panic: %s", redact.FormatPanic(r, nil))
				done <- nil
			}
		}()
		done <- run(dir, event, agent, e)
	}()
	select {
	case out := <-done:
		if len(out) > 0 && e.Stdout != nil {
			_, _ = e.Stdout.Write(out)
		}
	case <-time.After(deadline):
		logf(dir, event, "took longer than %s; the call was let through", deadline)
	}
	return 0
}

// parseArgs reads `<event> [--data-dir DIR] [--agent claude|codex]`.
func parseArgs(args []string) (event, dataDir, agent string) {
	agent = "claude"
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--data-dir" && i+1 < len(args):
			i++
			dataDir = args[i]
		case strings.HasPrefix(a, "--data-dir="):
			dataDir = strings.TrimPrefix(a, "--data-dir=")
		case a == "--agent" && i+1 < len(args):
			i++
			agent = args[i]
		case strings.HasPrefix(a, "--agent="):
			agent = strings.TrimPrefix(a, "--agent=")
		case !strings.HasPrefix(a, "-") && event == "":
			event = a
		}
	}
	return event, dataDir, agent
}

type handler struct {
	dir    string
	agent  string
	getenv func(string) string
	now    func() time.Time
	notify func(measure.Receipt)
	// notifyText shows a short desktop notification; nil where there is none.
	notifyText func(title, body string)
	snap       Snapshot
	eng        *optimize.Engine
	// budget is the engine's time budget for this process: the hook's deadline
	// less a margin. recover is set when the output trimmer may run for real, so
	// the engine gets the recovery store.
	budget  time.Duration
	recover bool
	caps    *Capabilities
}

func run(dir, event, agent string, e Env) []byte {
	valid := false
	for _, v := range Events {
		valid = valid || v == event
	}
	if !valid {
		logf(dir, event, "unknown hook event %q; known: %s", event, strings.Join(Events, ", "))
		return nil
	}
	in, err := ParseInput(e.Stdin)
	if err != nil || in.SessionID == "" {
		logf(dir, event, "could not read the hook input: %v", orEmpty(err))
		return nil
	}
	snap, _ := LoadSnapshot(dir)
	h := &handler{dir: dir, agent: agent, getenv: e.Getenv, now: e.Now, notify: e.Notify, notifyText: e.NotifyText, snap: snap, budget: callDeadline}
	if event == EventPostOutput {
		h.budget = postDeadline
	}
	key := h.sessionKey(in.SessionID)
	var out []byte
	switch event {
	case EventPreRead:
		out, err = h.preRead(in, key)
	case EventPostRead:
		err = h.postRead(in, key)
	case EventPostEdit:
		err = h.postEdit(in, key)
	case EventReset:
		err = h.reset(in, key)
	case EventLargeOutput:
		out, err = h.largeOutput(in, key)
	case EventPostOutput:
		out, err = h.postOutput(in, key)
	case EventPostFetch:
		err = h.postFetch(in, key)
	case EventSessionEnd:
		err = h.sessionEnd(in, key)
	}
	if err != nil {
		logf(dir, event, "%v", err)
		return nil
	}
	return out
}

func orEmpty(err error) error {
	if err == nil {
		return fmt.Errorf("no session id")
	}
	return err
}

// sessionKey is the hashed key rows are stored under. For Claude Code it is the
// same key the gateway and history parsers use, so hook counts join their data.
func (h *handler) sessionKey(id string) string {
	if h.agent == "codex" {
		sum := sha256.Sum256([]byte("codex\x00" + id))
		return "x-" + hex.EncodeToString(sum[:])[:24]
	}
	return session.ClaudeCodeKey(id)
}

// estTokens is ceil(bytes / 3.5), Trove's usual bytes-per-token prior. It is an
// estimate and is always labelled so.
func estTokens(b int64) int64 { return (b*2 + 6) / 7 }

func (h *handler) emit(key, hook, kind string, bytes, tokens int64) error {
	return record(h.dir, event{T: h.now().UnixMilli(), Key: key, Hook: hook, Kind: kind, Bytes: bytes, Tokens: tokens})
}

func denyJSON(reason string) []byte {
	b, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": "PreToolUse", "permissionDecision": "deny", "permissionDecisionReason": reason}})
	return b
}

func contextJSON(text string) []byte {
	b, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": "PreToolUse", "additionalContext": text}})
	return b
}
