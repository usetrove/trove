package cli

import (
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/hooks"
)

// RunHook runs `trove hook <event> ...` for cmd/trove, before the command tree is
// built, so a hook pays for as little start-up work as possible. args are the
// arguments after "hook". The exit code is always 0 (see hooks.Main).
func RunHook(args []string, stdin io.Reader, stdout io.Writer) int {
	if runProbeHook(args, stdin, stdout) {
		return 0
	}
	return hooks.Main(hooks.Env{Args: args, Stdin: stdin, Stdout: stdout, Getenv: os.Getenv, Notify: notifierSync(), NotifyText: textNotifierSync()})
}

// newHookCmd is the same entry point as a (hidden) cobra command, so
// `trove hook --help` and tests go through the normal path. Agents never use it.
func newHookCmd(a *App) *cobra.Command {
	return &cobra.Command{
		Use:    "hook <event>",
		Short:  "Run one agent hook (called by Claude Code and Codex CLI, not by hand)",
		Long:   "Events: " + strings.Join(hooks.Events, ", ") + ".\nReads the agent's hook input on stdin, writes the hook's answer on stdout and always exits 0, so a problem in Trove can never stop the agent. Problems are logged to hooks.log in the data directory. See docs/hooks.md.",
		Hidden: true,
		// The hook's own flags (--data-dir, --agent) are parsed by the hooks package.
		DisableFlagParsing: true,
		RunE: func(_ *cobra.Command, args []string) error {
			in := a.In
			if in == nil {
				in = strings.NewReader("")
			}
			if runProbeHook(args, in, a.Out) {
				return nil
			}
			hooks.Main(hooks.Env{Args: args, Stdin: in, Stdout: a.Out, Getenv: a.Getenv})
			return nil
		},
	}
}
