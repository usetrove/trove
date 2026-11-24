package probe

// The prompt-block check, for the cold-resume guard (docs/hooks.md). Claude Code's documentation says a
// UserPromptSubmit hook can block a prompt, but gives a hook no way to tell an interactive session from an unwatched
// `claude -p` run (code.claude.com/docs/en/hooks, /env-vars, /headless, read 2026-11-23). A block in a run nobody
// watches would stop automation, so the guard blocks only on a version where this check, in the user's own session,
// saw that:
//   - the first prompt was stopped and the user saw the reason, and
//   - the hooks were told CLAUDE_CODE_ENTRYPOINT=cli in the interactive session and something else under -p
//     (observed, not documented).
//
// As with the tool check, Trove starts nothing: `trove hooks verify --prompt-block` writes a settings file with one
// test UserPromptSubmit hook, the user runs one interactive session and one `claude -p` run with it, and
// `trove hooks verify --prompt-block --finish` reads what the hook recorded. The hook records only a call number and
// the entrypoint word, never the prompt.
//
// The check also answers whether a UserPromptSubmit systemMessage reaches the model (the documentation says only
// that it is "shown to the user"): the hook's second call sends a marker in systemMessage, and the user says
// whether the model's reply repeated it.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/hookcaps"
	"github.com/usetrove/trove/internal/hooks"
)

// EntrypointEnv is the variable the check reads. Claude Code is seen to set it; it is not documented.
const EntrypointEnv = "CLAUDE_CODE_ENTRYPOINT"

var entryRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

type promptSession struct {
	Armed   string `json:"armed"`
	Expires string `json:"expires"`
	Nonce   string `json:"nonce"`
	Version string `json:"agent_version,omitempty"`
}

// promptCall is what the test hook records for one call: its number and the entrypoint word, nothing else.
type promptCall struct {
	N     int    `json:"n"`
	Entry string `json:"entry"`
}

// ArmedPrompt is what ArmPrompt prepared.
type ArmedPrompt struct {
	State        string `json:"state_dir"`
	SettingsPath string `json:"settings_file"`
	Expires      string `json:"expires"`
	BlockMarker  string `json:"block_marker"`
	SystemMarker string `json:"system_marker"`
	// SystemPrompt is the prompt to send again after the block: it asks the model to repeat any marker it was shown.
	SystemPrompt string `json:"system_prompt"`
}

// ArmPromptOptions configure ArmPrompt.
type ArmPromptOptions struct {
	// HookCommand returns the program and arguments for the test hook, given the state directory.
	HookCommand func(state string) (command string, args []string)
	Version     string
	Now         func() time.Time
}

// ArmPrompt prepares the check in state. It runs nothing.
func ArmPrompt(state string, o ArmPromptOptions) (ArmedPrompt, error) {
	if o.HookCommand == nil {
		return ArmedPrompt{}, errors.New("internal error: the prompt check needs a hook command")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if err := os.RemoveAll(state); err != nil {
		return ArmedPrompt{}, err
	}
	if err := os.MkdirAll(state, 0o700); err != nil {
		return ArmedPrompt{}, err
	}
	now := o.Now().UTC()
	nonce := fmt.Sprintf("%x", now.UnixNano()&0xffffff)
	s := promptSession{Armed: now.Format(time.RFC3339), Expires: now.Add(SessionTTL).Format(time.RFC3339), Nonce: nonce, Version: o.Version}
	sb, _ := json.Marshal(s)
	if err := os.WriteFile(filepath.Join(state, "prompt-session.json"), sb, 0o600); err != nil {
		return ArmedPrompt{}, err
	}
	cmd, args := o.HookCommand(state)
	settings, _ := json.MarshalIndent(map[string]any{"hooks": map[string]any{"UserPromptSubmit": []any{
		map[string]any{"hooks": []any{map[string]any{"type": "command", "command": cmd, "args": args, "timeout": 10}}}}}}, "", "  ")
	a := ArmedPrompt{State: state, SettingsPath: filepath.Join(state, "settings.json"), Expires: s.Expires,
		BlockMarker: "TROVE_PROBE_BLOCK_" + nonce, SystemMarker: "TROVE_PROBE_SYS_" + nonce,
		SystemPrompt: "Reply with any text you were given that starts with TROVE_PROBE_SYS_, or with NONE if there is none. Use no tools."}
	if err := os.WriteFile(a.SettingsPath, settings, 0o600); err != nil {
		return ArmedPrompt{}, err
	}
	return a, nil
}

// PromptHookMain is the test UserPromptSubmit hook. The first call is stopped with the block marker; every later
// call goes through with the system marker in systemMessage. It records the call number and the entrypoint word
// and never fails the prompt.
func PromptHookMain(stdin io.Reader, stdout io.Writer, getenv func(string) string) int {
	state := getenv(StateEnv)
	if state == "" {
		return 0
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(stdin, 1<<20))           // the prompt is not read
	b, err := os.ReadFile(filepath.Join(state, "prompt-session.json")) //nolint:gosec // our own state dir
	var s promptSession
	if err != nil || json.Unmarshal(b, &s) != nil {
		return 0
	}
	if exp, err := time.Parse(time.RFC3339, s.Expires); err != nil || time.Now().After(exp) {
		return 0
	}
	calls := readPromptCalls(state)
	entry := getenv(EntrypointEnv)
	if entry != "" && !entryRe.MatchString(entry) {
		entry = "other"
	}
	c := promptCall{N: len(calls) + 1, Entry: entry}
	line, _ := json.Marshal(c)
	f, err := os.OpenFile(filepath.Join(state, "prompt-calls.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // our own state dir
	if err != nil {
		return 0
	}
	_, werr := f.Write(append(line, '\n'))
	if cerr := f.Close(); werr != nil || cerr != nil {
		return 0 // unrecorded: let the prompt through and do not block
	}
	var out []byte
	if c.N == 1 {
		out, _ = json.Marshal(map[string]any{"decision": "block",
			"reason": "TROVE_PROBE_BLOCK_" + s.Nonce + ": Trove's test stopped this prompt once. Send the second prompt now."})
	} else {
		out, _ = json.Marshal(map[string]any{"systemMessage": "TROVE_PROBE_SYS_" + s.Nonce})
	}
	_, _ = stdout.Write(out)
	return 0
}

func readPromptCalls(state string) []promptCall {
	b, err := os.ReadFile(filepath.Join(state, "prompt-calls.jsonl")) //nolint:gosec // our own state dir
	if err != nil {
		return nil
	}
	var out []promptCall
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var c promptCall
		if json.Unmarshal([]byte(l), &c) == nil && c.N > 0 {
			out = append(out, c)
		}
	}
	return out
}

// FinishPromptOptions configure FinishPrompt. SawBlock and SawSystem are the user's answers; nil means unanswered.
type FinishPromptOptions struct {
	SawBlock  *bool
	SawSystem *bool
	Version   string
	Now       func() time.Time
}

// PromptResult is the check's outcome: the capability entry, and what was learned about systemMessage.
type PromptResult struct {
	Capability hooks.Capability `json:"capability"`
	// SystemToModel is "yes" when the user said the model repeated the systemMessage marker, "no" when it did not,
	// and "untested" when there was no answer.
	SystemToModel string `json:"system_message_reaches_model"`
}

// FinishPrompt reads what the test hook recorded. The interactive session must come first (its first prompt is the
// one stopped) and the `claude -p` run last.
func FinishPrompt(state string, o FinishPromptOptions) (PromptResult, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	b, err := os.ReadFile(filepath.Join(state, "prompt-session.json")) //nolint:gosec // our own state dir
	var s promptSession
	if err != nil || json.Unmarshal(b, &s) != nil {
		return PromptResult{}, errors.New("no prompt check is waiting. Run `trove hooks verify --prompt-block` first")
	}
	ver := o.Version
	if ver == "" {
		ver = s.Version
	}
	if ver == "" {
		ver = "unknown"
	}
	c := hooks.Capability{Agent: "claude", AgentVersion: ver, Tool: hookcaps.ToolPrompt, Method: "session",
		ProbedAt: o.Now().UTC().Format(time.RFC3339), InputRewrite: hooks.CapNo, OutputRewrite: hooks.CapNo, Block: hooks.CapUntested}
	res := PromptResult{SystemToModel: hooks.CapUntested}
	if o.SawSystem != nil {
		res.SystemToModel = yesNo(*o.SawSystem)
	}
	calls := readPromptCalls(state)
	first, last := "", ""
	if len(calls) > 0 {
		first, last = calls[0].Entry, calls[len(calls)-1].Entry
	}
	switch {
	case len(calls) < 3:
		c.Evidence = fmt.Sprintf("the test hook ran %d %s; it needs two prompts in an interactive session and then one `claude -p` run", len(calls), plural(len(calls)))
	case o.SawBlock == nil:
		c.Evidence = "you did not say whether the first prompt was stopped with " + "TROVE_PROBE_BLOCK_" + s.Nonce
	case !*o.SawBlock:
		c.Block = hooks.CapNo
		c.Evidence = "you reported that the first prompt was not stopped"
	case first != "cli":
		c.Block = hooks.CapNo
		c.Evidence = fmt.Sprintf("the interactive session's hook saw %s=%q, not \"cli\", so Trove cannot tell it from an unwatched run", EntrypointEnv, first)
	case last == "" || last == "cli":
		c.Block = hooks.CapNo
		c.Evidence = fmt.Sprintf("the `claude -p` run's hook saw %s=%q, the same as or no better than an interactive session, so Trove cannot tell them apart", EntrypointEnv, last)
	default:
		c.Block = hooks.CapYes
		c.Evidence = fmt.Sprintf("in your own session the first prompt was stopped and you saw the reason (reported by you); hooks saw %s=cli interactively and %s=%s under -p (observed, not documented)",
			EntrypointEnv, EntrypointEnv, last)
	}
	if res.SystemToModel != hooks.CapUntested {
		c.Evidence += "; a UserPromptSubmit systemMessage reached the model: " + res.SystemToModel + " (reported by you)"
	}
	res.Capability = c
	return res, nil
}

func yesNo(b bool) string {
	if b {
		return hooks.CapYes
	}
	return hooks.CapNo
}

func plural(n int) string {
	if n == 1 {
		return "time"
	}
	return "times"
}
