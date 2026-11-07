package cli

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/hooks"
	hookprobe "github.com/usetrove/trove/internal/hooks/probe"
)

type hooksVerifyFlags struct {
	finish    bool
	tools     []string
	sawOutput []string
	cancel    bool
}

func newHooksVerify(a *App) *cobra.Command {
	var fl hooksVerifyFlags
	c := &cobra.Command{
		Use:   "verify",
		Short: "Test hook rewriting inside a Claude Code session you start yourself (works on any plan)",
		Long: "Like `trove hooks probe --live`, but Trove starts no model request and runs no agent. It prepares a settings file and a\n" +
			"short prompt; you start Claude Code yourself with `claude --settings <file>`, paste the prompt, and approve the tool\n" +
			"call. Test hooks in that file rewrite one tool call and record what the agent did. Then `trove hooks verify --finish`\n" +
			"reads the record. It works on a Claude.ai plan or an API key, and uses a few tool calls of your own session.\n\n" +
			"The input result is observed (the PostToolUse hook is told what the tool really ran with). A hook cannot see what the\n" +
			"model was shown, so for the output result you say whether the model's reply contained the replacement text, with\n" +
			"--saw-output. That result is stored as reported by you. The test hooks stop on their own after an hour.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return runHooksVerify(a, fl) },
	}
	c.Flags().BoolVar(&fl.finish, "finish", false, "read the record of the session you ran and store the result")
	c.Flags().BoolVar(&fl.cancel, "cancel", false, "remove a prepared session and its settings file")
	c.Flags().StringSliceVar(&fl.tools, "tools", hookprobe.DefaultTools, "tools to test")
	c.Flags().StringSliceVar(&fl.sawOutput, "saw-output", nil, "with --finish: tools whose reply contained the replacement text, as Tool or Tool=no (for example Bash,Read=no)")
	return c
}

type verifyReport struct {
	Armed        *hookprobe.Armed    `json:"armed,omitempty"`
	AgentVersion string              `json:"agent_version,omitempty"`
	Entries      []hooks.Capability  `json:"entries,omitempty"`
	Steps        []string            `json:"steps,omitempty"`
	Notes        []string            `json:"notes,omitempty"`
	Capabilities *hooks.Capabilities `json:"capabilities,omitempty"`
}

func runHooksVerify(a *App, fl hooksVerifyFlags) error {
	dir, _, err := loadConfig(a)
	if err != nil {
		return err
	}
	state := filepath.Join(dir, "hooks", "verify")
	switch {
	case fl.cancel:
		if err := hookprobe.Disarm(state); err != nil {
			return fmt.Errorf("could not remove %s: %v. Delete that folder by hand", state, err)
		}
		return a.emit(verifyReport{Notes: []string{"Removed the prepared session."}}, "Removed the prepared session.")
	case fl.finish:
		return finishVerify(a, dir, state, fl)
	}
	bin, err := troveBinary(a)
	if err != nil {
		return err
	}
	armed, err := hookprobe.Arm(state, hookprobe.ArmOptions{
		Tools:   fl.tools,
		Version: installedClaudeVersion(a),
		HookCommand: func(mode, st string) (string, []string) {
			return bin, []string{"hook", "probe_" + mode, "--state", st}
		},
	})
	if err != nil {
		return err
	}
	rep := verifyReport{Armed: &armed, AgentVersion: installedClaudeVersion(a)}
	var b strings.Builder
	fmt.Fprintf(&b, "Trove has prepared a test. It started nothing and made no request.\n\n")
	fmt.Fprintf(&b, "1. In a new terminal start Claude Code yourself, with the test hooks added to that one session:\n\n     claude --settings %s\n\n", shellQuote(armed.SettingsPath))
	fmt.Fprintf(&b, "2. Paste these prompts one at a time and approve each tool call. Each one makes the model call one tool once;\n   the test hooks change that one call. Your own project is not touched.\n\n")
	for _, t := range armed.Tools {
		fmt.Fprintf(&b, "   %s:\n     %s\n\n", t, armed.Prompts[t])
	}
	fmt.Fprintf(&b, "3. For each tool the model's reply should be a text starting TROVE_PROBE_OUT_ if the agent honoured the output rewrite.\n")
	fmt.Fprintf(&b, "   Note which tools did that. Then exit Claude Code and run:\n\n     trove hooks verify --finish --saw-output <tools whose reply started TROVE_PROBE_OUT_, comma separated>\n\n")
	fmt.Fprintf(&b, "   Use Tool=no for a tool where the reply did not. A tool you leave out keeps its output result untested.\n")
	fmt.Fprintf(&b, "\nThe test hooks switch themselves off at %s. `trove hooks verify --cancel` removes the test now.\n", armed.Expires)
	rep.Steps = []string{"claude --settings " + armed.SettingsPath}
	return a.emit(rep, b.String())
}

func finishVerify(a *App, dir, state string, fl hooksVerifyFlags) error {
	saw := map[string]bool{}
	for _, s := range fl.sawOutput {
		s = strings.TrimSpace(s)
		switch {
		case s == "":
		case strings.HasSuffix(s, "=no"):
			saw[strings.TrimSuffix(s, "=no")] = false
		case strings.HasSuffix(s, "=yes"):
			saw[strings.TrimSuffix(s, "=yes")] = true
		default:
			saw[s] = true
		}
	}
	ver := installedClaudeVersion(a)
	entries, err := hookprobe.Finish(state, hookprobe.FinishOptions{SawOutput: saw, Version: ver})
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return errors.New("the prepared session had no tools. Run `trove hooks verify` again")
	}
	merged := mergeTools(hooks.LoadCapabilities(dir), entries)
	if err := hooks.SaveCapabilities(dir, merged); err != nil {
		return fmt.Errorf("could not save the result: %v. Check the permissions of %s", err, dir)
	}
	_ = hookprobe.Disarm(state)
	rep := verifyReport{AgentVersion: ver, Entries: entries, Capabilities: &merged}
	var b strings.Builder
	fmt.Fprintf(&b, "Hook capabilities, Claude Code %s, tested in your own session\n\n", orDash(ver))
	for _, e := range entries {
		fmt.Fprintf(&b, "%s %s: rewrite input %s, rewrite output %s [%s]\n    %s\n", e.Agent, e.Tool, e.InputRewrite, e.OutputRewrite, e.Method, e.Evidence)
	}
	b.WriteString("\nTrove rewrites a tool's input or output only where this shows \"yes\". The test hooks and settings file were removed. Run it again after updating the agent.\n")
	return a.emit(rep, b.String())
}

// mergeTools replaces the stored entry of each (agent, tool) in fresh and keeps the rest.
func mergeTools(old hooks.Capabilities, fresh []hooks.Capability) hooks.Capabilities {
	type key struct{ agent, tool string }
	replaced := map[key]bool{}
	for _, e := range fresh {
		replaced[key{e.Agent, e.Tool}] = true
	}
	var out hooks.Capabilities
	for _, e := range old.Entries {
		if !replaced[key{e.Agent, e.Tool}] {
			out.Entries = append(out.Entries, e)
		}
	}
	out.Entries = append(out.Entries, fresh...)
	return out
}
