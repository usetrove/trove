package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/usetrove/trove/internal/hookcaps"
	"github.com/usetrove/trove/internal/hooks"
	hookprobe "github.com/usetrove/trove/internal/hooks/probe"
)

type contextVerifyReport struct {
	Armed        *hookprobe.ArmedContext  `json:"armed,omitempty"`
	Result       *hookprobe.ContextResult `json:"result,omitempty"`
	AgentVersion string                   `json:"agent_version,omitempty"`
	Notes        []string                 `json:"notes,omitempty"`
}

// runContextVerify is `trove hooks verify --prompt-context`: the check hook_routine_steer needs before it sends
// anything (docs/hooks.md, "Routine steer"). Like the other checks, Trove starts nothing.
func runContextVerify(a *App, dir string, fl hooksVerifyFlags) error {
	state := filepath.Join(dir, "hooks", "verify-context")
	switch {
	case fl.cancel:
		if err := hookprobe.Disarm(state); err != nil {
			return fmt.Errorf("could not remove %s: %v. Delete that folder by hand", state, err)
		}
		return a.emit(contextVerifyReport{Notes: []string{"Removed the prepared check."}}, "Removed the prepared check.")
	case fl.finish:
		return finishContextVerify(a, dir, state)
	}
	bin, err := troveBinary(a)
	if err != nil {
		return err
	}
	ver := installedClaudeVersion(a)
	armed, err := hookprobe.ArmContext(state, hookprobe.ArmContextOptions{Version: ver,
		HookCommand: func(st string) (string, []string) { return bin, []string{"hook", "probe_context", "--state", st} }})
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("Trove has prepared a check of where text added by a UserPromptSubmit hook lands. It started nothing and made no request.\n\n")
	fmt.Fprintf(&b, "1. In a new terminal, in any folder, start Claude Code yourself with the test hook added:\n\n     claude --model claude-opus-5-5 --settings %s\n\n", shellQuote(armed.SettingsPath))
	b.WriteString("2. Send these four prompts, one at a time, waiting for each reply. Send them within a few minutes, so the\n   prompt cache is still warm:\n\n")
	for i, p := range armed.Prompts {
		fmt.Fprintf(&b, "     %d. %s\n", i+1, p)
	}
	b.WriteString("\n   The test hook adds a short marker to the second prompt. Then exit Claude Code.\n\n")
	b.WriteString("3. Then run:\n\n     trove hooks verify --prompt-context --finish\n\n")
	fmt.Fprintf(&b, "On the fourth prompt the test hook reads the session's transcript in memory and records only line positions and\ntoken counts, never a prompt or a reply. It switches itself off at %s. The check uses four short turns of your\nown session.\n", armed.Expires)
	return a.emit(contextVerifyReport{Armed: &armed, AgentVersion: ver}, b.String())
}

func finishContextVerify(a *App, dir, state string) error {
	ver := installedClaudeVersion(a)
	caps := hooks.LoadCapabilities(dir)
	var prior *hooks.Capability
	if e, ok := caps.Find("claude", hookcaps.ToolPrompt); ok {
		prior = &e
	}
	res, err := hookprobe.FinishContext(state, hookprobe.FinishContextOptions{Version: ver, Prior: prior})
	if err != nil {
		return err
	}
	if err := hooks.SaveCapabilities(dir, mergeTools(caps, []hooks.Capability{res.Capability})); err != nil {
		return fmt.Errorf("could not save the result: %v. Check the permissions of %s", err, dir)
	}
	_ = hookprobe.Disarm(state)
	c := res.Capability
	var b strings.Builder
	fmt.Fprintf(&b, "Hook context placement, Claude Code %s, tested in your own session: %s\n    %s\n\n", orDash(c.AgentVersion), c.Context, c.ContextEvidence)
	switch c.Context {
	case hooks.CapYes:
		b.WriteString("hook_routine_steer may now send its text in sessions where it is on, or in the \"on\" arm of its trial.\n" +
			"Run this again after updating Claude Code.\n")
	case hooks.CapNo:
		b.WriteString("hook_routine_steer keeps recording only, on this version: sending its text would cost the prompt cache.\n")
	default:
		b.WriteString("Nothing was decided. Run `trove hooks verify --prompt-context` again and send all four prompts in one session.\n")
	}
	if !res.Findings.ModelAllowed {
		b.WriteString("The session's model was not one hook_routine_steer acts on (only claude-opus-5-5), or Claude Code did not say which it was.\n")
	}
	return a.emit(contextVerifyReport{Result: &res, AgentVersion: ver}, b.String())
}
