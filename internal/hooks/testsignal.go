package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"

	"github.com/usetrove/trove/internal/mcp"
	"github.com/usetrove/trove/internal/mcp/root"
)

// exitLine is the first line of a PostToolUseFailure error for a Bash command that ran and exited
// (code.claude.com/docs/en/hooks, read 2026-11-17).
var exitLine = regexp.MustCompile(`^Exit code (\d{1,3})\s*$`)

// testResult is post_tool_test_result, the test-pass signal. Registered for PostToolUse and
// PostToolUseFailure on Bash, it records the exit code of a command that exactly matches one of
// the project's allowlisted test commands, as a hash of the command and its code. Anything else,
// an interrupted run, or a failure without an exit code is left out: no result is better than a
// wrong one. It never prints anything, so it changes nothing the agent sees.
func (h *handler) testResult(in Input, key string) error {
	if in.ToolName != "Bash" || strings.TrimSpace(in.ToolInput.Command) == "" {
		return nil
	}
	code, ok := exitCodeOf(in)
	if ok && code != 0 {
		h.ckCall(in, key, ckFail) // every Bash call reaches this hook once; the session checkpoint counts it
	} else {
		h.ckCall(in, key, ckOther)
	}
	if !ok || in.Cwd == "" {
		return nil
	}
	id, ok := matchTestCommand(in.Cwd, in.ToolInput.Command)
	if !ok {
		return nil
	}
	return record(h.dir, event{T: h.now().UnixMilli(), Key: key, Hook: id, Kind: KindTestRun, Exit: code})
}

// exitCodeOf reads the exit code from either event. PostToolUse fires only after a tool call
// succeeds, and its Bash response has no exit-code field, so a run that was not interrupted is 0.
func exitCodeOf(in Input) (int, bool) {
	switch in.Event {
	case "PostToolUse":
		if interrupted(in.ToolResponse) {
			return 0, false
		}
		return 0, true
	case "PostToolUseFailure":
		if in.IsInterrupt {
			return 0, false
		}
		first, _, _ := strings.Cut(in.Error, "\n")
		m := exitLine.FindStringSubmatch(strings.TrimRight(first, "\r"))
		if m == nil {
			return 0, false
		}
		n, err := strconv.Atoi(m[1])
		return n, err == nil
	}
	return 0, false
}

// isTestName says whether a commands.toml entry is a test command: "test", or a name that
// starts or ends with test followed or preceded by - or _ (test-unit, unit_test).
func isTestName(name string) bool {
	n := strings.ToLower(name)
	return n == "test" || strings.HasPrefix(n, "test-") || strings.HasPrefix(n, "test_") ||
		strings.HasSuffix(n, "-test") || strings.HasSuffix(n, "_test")
}

// matchTestCommand finds the allowlisted test command the shell command is, comparing argument
// lists split on whitespace. It returns a 16-character hash of the argument list. A missing or
// invalid commands.toml matches nothing.
func matchTestCommand(cwd, command string) (string, bool) {
	r, err := root.New(cwd)
	if err != nil {
		return "", false
	}
	cmds, err := mcp.LoadCommands(r)
	if err != nil {
		return "", false
	}
	fields := strings.Fields(command)
	for name, c := range cmds {
		if isTestName(name) && sameArgs(c.Run, fields) {
			return commandHash(c.Run), true
		}
	}
	return "", false
}

func sameArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func commandHash(run []string) string {
	sum := sha256.Sum256([]byte("trove-test\x00" + strings.Join(run, "\x00")))
	return hex.EncodeToString(sum[:])[:16]
}
