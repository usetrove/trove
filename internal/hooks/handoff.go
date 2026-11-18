package hooks

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/external"
)

// EventHandoff is `trove hook handoff <tool>`: the Bash PreToolUse hook Trove writes for a third-party hook tool
// (RTK, ZTK) instead of the tool's own hook (plan item 9.3). Trove owns the hook, so it can run a randomized test:
// each new session is assigned an arm at SessionStart by a hash of its key, and the call is handed to the tool only in
// the "on" arm. In that arm the hand-off is transparent: the tool gets the agent's hook input byte for byte, and its
// stdout, stderr and exit code go back to the agent unchanged, so the agent sees exactly what the tool's own hook would
// have produced. In the "off" arm nothing is printed and the call goes ahead as if no tool were installed.
const EventHandoff = "handoff"

// handoffDeadline bounds the tool's run; the agent's own hook timeout applies as well.
const handoffDeadline = 30 * time.Second

// Handoff runs one hand-off and returns the exit code to use. Anything Trove cannot work out (no session, an
// unreadable state file, an unknown tool) falls back to what the user had before the test: the tool runs (rule 4,
// and the tool stays the user's choice). A tool that cannot be started lets the call through with no output.
func Handoff(e Env, stderr io.Writer) int {
	tool, dataFlag := handoffArgs(e.Args)
	if e.Getenv == nil {
		e.Getenv = os.Getenv
	}
	dir, err := config.ResolveDataDir(dataFlag, e.Getenv)
	if err != nil {
		return 0
	}
	in, err := io.ReadAll(io.LimitReader(e.Stdin, 8<<20))
	if err != nil {
		return 0
	}
	argv := toolCommandFn(tool)
	if len(argv) == 0 {
		logf(dir, EventHandoff, "unknown tool %q; known hook tools come from Trove's built-in manifests", tool)
		return 0
	}
	if arm := handoffArm(dir, tool, in); arm == stateOff {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), handoffDeadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // the command comes from Trove's own manifest
	cmd.Stdin = bytes.NewReader(in)
	cmd.Stdout = e.Stdout
	cmd.Stderr = stderr
	err = cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee):
		return ee.ExitCode()
	default:
		logf(dir, EventHandoff, "could not start %s: %v", tool, err)
		return 0
	}
}

func handoffArgs(args []string) (tool, dataDir string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == EventHandoff:
		case a == "--data-dir" && i+1 < len(args):
			i++
			dataDir = args[i]
		case strings.HasPrefix(a, "--data-dir="):
			dataDir = strings.TrimPrefix(a, "--data-dir=")
		case !strings.HasPrefix(a, "-") && tool == "":
			tool = a
		}
	}
	return tool, dataDir
}

// toolCommandFn finds a tool's hook command; tests replace it with a stand-in.
var toolCommandFn = toolCommand

// toolCommand is the hook command a built-in hook tool's manifest names, split into argv (no shell).
func toolCommand(tool string) []string {
	ms, err := external.Builtin()
	if err != nil {
		return nil
	}
	for _, m := range ms {
		if m.Name == tool && m.ActsAs == external.ActsHook && m.Wire.HookCommand != "" {
			return strings.Fields(m.Wire.HookCommand)
		}
	}
	return nil
}

// handoffArm is the session's pinned arm for the tool, or "" when the session was never pinned (it began before
// the test, or the state cannot be read): such a session keeps the tool, as before the test.
func handoffArm(dir, tool string, in []byte) string {
	parsed, err := parseFor(AgentClaude, bytes.NewReader(in))
	if err != nil || parsed.SessionID == "" {
		return ""
	}
	return loadSession(dir, agentSessionKey(AgentClaude, parsed.SessionID)).Pins[external.Prefix+tool]
}

// HandoffTools lists the enabled hook tools whose sessions the test splits, as trial names (ext_<tool>).
func HandoffTools(states map[string]string) []string {
	ms, err := external.Builtin()
	if err != nil {
		return nil
	}
	var out []string
	for _, m := range ms {
		if m.ActsAs == external.ActsHook && states[external.Prefix+m.Name] == config.StateOn {
			out = append(out, external.Prefix+m.Name)
		}
	}
	return out
}
