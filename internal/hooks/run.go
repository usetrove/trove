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
	"encoding/json"
	"fmt"
	"github.com/usetrove/trove/internal/optimize"
	"io"
	"os"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/config"
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
	// EventPostRun runs after the trove_run MCP tool and counts that the agent used it, which is how the
	// guardrail for hook_large_output sees whether its hint was followed.
	EventPostRun = "post_tool_run"
	// EventSubagent runs before an Agent (formerly Task) call and declines the scout in the off arm of its trial.
	EventSubagent = "pre_tool_subagent"
	// EventTestResult runs after Bash (PostToolUse and PostToolUseFailure) and records the exit code of an
	// allowlisted test command, the test-pass signal. It never changes anything.
	EventTestResult = "post_tool_test_result"
	// EventCodeWrite runs before Write, Edit and MultiEdit: hook_write_to_edit may deny a Write once, and
	// hook_edit_batch may add a note to an Edit.
	EventCodeWrite = "pre_tool_code_write"
)

// Events lists every event name.
var Events = []string{EventPreRead, EventPostRead, EventPostEdit, EventReset, EventSessionEnd, EventLargeOutput, EventPostOutput, EventPostFetch, EventPostRun, EventSubagent, EventTestResult, EventPromptSubmit, EventModelSwitch, EventPostModelSwitch, EventCompactCheck, EventSubagentRecord, EventCodeWrite}

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
	// Stderr receives the message of a hook that blocks by exiting 2 (only the cold-compact guard does). Without it
	// such a hook exits 0 and blocks nothing, because a block the user cannot read must not happen.
	Stderr io.Writer
}

// blockExit is a decision to block by exiting 2 with msg on stderr. It is the only way a handler can end in a
// non-zero exit; every error, panic and deadline still ends in 0.
type blockExit struct{ msg string }

func (b *blockExit) Error() string { return "block: " + b.msg }

// Main runs one hook and returns the process exit code. It is 0 except for a deliberate block by the
// cold-compact guard (2, with its message on stderr): a hook must never be the reason an agent stops
// (PRINCIPLES.md rule 4), so every error, panic and timeout ends in 0.
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
	case EventPostOutput, EventPostRead:
		deadline = postDeadline // both may store the full output before replacing it
	case EventPromptSubmit, EventCompactCheck:
		deadline = postDeadline // read up to 1 MiB of the transcript
	}
	type result struct {
		out   []byte
		block *blockExit
	}
	done := make(chan result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logf(dir, event, "recovered from a panic: %s", redact.FormatPanic(r, nil))
				done <- result{}
			}
		}()
		out, block := run(dir, event, agent, e)
		done <- result{out, block}
	}()
	select {
	case res := <-done:
		if res.block != nil && e.Stderr != nil {
			if _, err := io.WriteString(e.Stderr, res.block.msg); err == nil {
				return 2
			}
			return 0
		}
		if len(res.out) > 0 && e.Stdout != nil {
			_, _ = e.Stdout.Write(res.out)
		}
	case <-time.After(deadline):
		logf(dir, event, "took longer than %s; the call was let through", deadline)
	}
	return 0
}

// parseArgs reads `<event> [--data-dir DIR] [--agent claude|codex|cursor|gemini]`.
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
	// bpt and bptN are the learned bytes-per-token ratio of the session being handled and its sample
	// size (zero: the default ratio). They are set by sess and recorded with each estimate.
	bpt  float64
	bptN int
}

func run(dir, event, agent string, e Env) ([]byte, *blockExit) {
	valid := false
	for _, v := range Events {
		valid = valid || v == event
	}
	if !valid {
		logf(dir, event, "unknown hook event %q; known: %s", event, strings.Join(Events, ", "))
		return nil, nil
	}
	in, err := parseFor(agent, e.Stdin)
	if err != nil || in.SessionID == "" {
		logf(dir, event, "could not read the hook input: %v", orEmpty(err))
		return nil, nil
	}
	snap, _ := LoadSnapshot(dir)
	h := &handler{dir: dir, agent: agent, getenv: e.Getenv, now: e.Now, notify: e.Notify, notifyText: e.NotifyText, snap: snap, budget: callDeadline}
	if event == EventPostOutput || event == EventPostRead {
		h.budget = postDeadline
	}
	key := h.sessionKey(in.SessionID)
	var out []byte
	switch event {
	case EventPreRead:
		out, err = h.preRead(in, key)
	case EventPostRead:
		out, err = h.postRead(in, key)
	case EventPostEdit:
		err = h.postEdit(in, key)
	case EventReset:
		out, err = h.reset(in, key)
	case EventLargeOutput:
		out, err = h.largeOutput(in, key)
	case EventCodeWrite:
		out, err = h.codeWrite(in, key)
	case EventPostOutput:
		out, err = h.postOutput(in, key)
	case EventPostFetch:
		err = h.postFetch(in, key)
	case EventPostRun:
		err = h.postRun(in, key)
	case EventSubagent:
		out, err = h.subagent(in, key)
	case EventTestResult:
		err = h.testResult(in, key)
	case EventSubagentRecord:
		err = h.subagentRecord(in, key)
	case EventPromptSubmit:
		out, err = h.promptSubmit(in, key)
	case EventModelSwitch:
		out, err = h.modelSwitch(in, key)
	case EventPostModelSwitch:
		err = h.postModelSwitch(in, key)
	case EventCompactCheck:
		var b *blockExit
		if b, err = h.compactCheck(in, key); b != nil && err == nil {
			return nil, b
		}
	case EventSessionEnd:
		err = h.sessionEnd(in, key)
	}
	if err != nil {
		logf(dir, event, "%v", err)
		return nil, nil
	}
	return reply(agent, out), nil
}

func orEmpty(err error) error {
	if err == nil {
		return fmt.Errorf("no session id")
	}
	return err
}

// sessionKey is the hashed key rows are stored under. For Claude Code it is the
// same key the gateway and history parsers use, so hook counts join their data.
func (h *handler) sessionKey(id string) string { return agentSessionKey(h.agent, id) }

// estTokens is ceil(bytes / 3.5), Trove's usual bytes-per-token prior. It is an
// estimate and is always labelled so.
func estTokens(b int64) int64 { return (b*2 + 6) / 7 }

func (h *handler) emit(key, hook, kind string, bytes, tokens int64) error {
	e := event{T: h.now().UnixMilli(), Key: key, Hook: hook, Kind: kind, Bytes: bytes, Tokens: tokens}
	if tokens > 0 && h.bptN > 0 {
		e.Ratio, e.Samples = h.bpt, h.bptN // the estimate used a learned ratio: say which and how well founded
	}
	return record(h.dir, e)
}

func denyJSON(reason string) []byte {
	b, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": "PreToolUse", "permissionDecision": "deny", "permissionDecisionReason": reason}})
	return b
}

// sessionContextJSON adds text to the session's context at SessionStart (hookSpecificOutput.additionalContext,
// code.claude.com/docs/en/hooks, checked 2026-11-17; strings over 10,000 characters are replaced by a file path
// and a preview, so every rule set Trove ships stays under that, see TestRuleSetsFitTheContextLimit).
func sessionContextJSON(text string) []byte {
	b, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": "SessionStart", "additionalContext": text}})
	return b
}

func contextJSON(text string) []byte {
	b, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": "PreToolUse", "additionalContext": text}})
	return b
}
