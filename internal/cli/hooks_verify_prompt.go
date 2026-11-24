package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/usetrove/trove/internal/hooks"
	hookprobe "github.com/usetrove/trove/internal/hooks/probe"
)

type promptVerifyReport struct {
	Armed        *hookprobe.ArmedPrompt  `json:"armed,omitempty"`
	Result       *hookprobe.PromptResult `json:"result,omitempty"`
	AgentVersion string                  `json:"agent_version,omitempty"`
	Notes        []string                `json:"notes,omitempty"`
}

// runPromptVerify is `trove hooks verify --prompt-block`: the check hook_cold_resume's ask mode needs before it may
// stop a prompt (docs/hooks.md). Like the tool check, Trove starts nothing.
func runPromptVerify(a *App, dir string, fl hooksVerifyFlags) error {
	state := filepath.Join(dir, "hooks", "verify-prompt")
	switch {
	case fl.cancel:
		if err := hookprobe.Disarm(state); err != nil {
			return fmt.Errorf("could not remove %s: %v. Delete that folder by hand", state, err)
		}
		return a.emit(promptVerifyReport{Notes: []string{"Removed the prepared check."}}, "Removed the prepared check.")
	case fl.finish:
		return finishPromptVerify(a, dir, state, fl)
	}
	bin, err := troveBinary(a)
	if err != nil {
		return err
	}
	ver := installedClaudeVersion(a)
	armed, err := hookprobe.ArmPrompt(state, hookprobe.ArmPromptOptions{Version: ver,
		HookCommand: func(st string) (string, []string) { return bin, []string{"hook", "probe_prompt", "--state", st} }})
	if err != nil {
		return err
	}
	settings := shellQuote(armed.SettingsPath)
	var b strings.Builder
	b.WriteString("Trove has prepared a check of prompt blocking. It started nothing and made no request.\n\n")
	fmt.Fprintf(&b, "1. In a new terminal, in any folder, start Claude Code yourself with the test hook added:\n\n     claude --settings %s\n\n", settings)
	fmt.Fprintf(&b, "2. Type any short prompt, for example: hello\n   It should be stopped with a message starting %s. Note whether it was.\n\n", armed.BlockMarker)
	fmt.Fprintf(&b, "3. Then send this prompt:\n\n     %s\n\n   Note whether the reply contains %s. Then exit Claude Code.\n\n", armed.SystemPrompt, armed.SystemMarker)
	fmt.Fprintf(&b, "4. Run Claude Code once in print mode with the same file:\n\n     claude -p --settings %s \"reply with OK\"\n\n", settings)
	b.WriteString("5. Then run, with yes or no for what you saw in steps 2 and 3:\n\n     trove hooks verify --prompt-block --finish --saw-block yes --saw-system no\n\n")
	fmt.Fprintf(&b, "The test hook records only a call number and the value of CLAUDE_CODE_ENTRYPOINT, never your prompt. It switches itself off at %s.\n", armed.Expires)
	return a.emit(promptVerifyReport{Armed: &armed, AgentVersion: ver}, b.String())
}

func finishPromptVerify(a *App, dir, state string, fl hooksVerifyFlags) error {
	sawBlock, err := yesNoFlag("--saw-block", fl.sawBlock)
	if err != nil {
		return err
	}
	sawSystem, err := yesNoFlag("--saw-system", fl.sawSystem)
	if err != nil {
		return err
	}
	ver := installedClaudeVersion(a)
	res, err := hookprobe.FinishPrompt(state, hookprobe.FinishPromptOptions{SawBlock: sawBlock, SawSystem: sawSystem, Version: ver})
	if err != nil {
		return err
	}
	merged := mergeTools(hooks.LoadCapabilities(dir), []hooks.Capability{res.Capability})
	if err := hooks.SaveCapabilities(dir, merged); err != nil {
		return fmt.Errorf("could not save the result: %v. Check the permissions of %s", err, dir)
	}
	_ = hookprobe.Disarm(state)
	c := res.Capability
	var b strings.Builder
	fmt.Fprintf(&b, "Prompt blocking, Claude Code %s, tested in your own session: %s\n    %s\n\n", orDash(c.AgentVersion), c.Block, c.Evidence)
	switch c.Block {
	case hooks.CapYes:
		b.WriteString("hook_cold_resume may now stop the first prompt after a cold resume when optimize.cold_resume_action is \"ask\". Run this again after updating Claude Code.\n")
	default:
		b.WriteString("hook_cold_resume keeps warning instead of stopping prompts on this version.\n")
	}
	if res.SystemToModel == hooks.CapYes {
		b.WriteString("The warning text reached the model, so each warning also costs its length in tokens; docs/hooks.md says how this is charged.\n")
	}
	return a.emit(promptVerifyReport{Result: &res, AgentVersion: ver}, b.String())
}

// yesNoFlag reads a yes/no answer; "" is no answer.
func yesNoFlag(name, v string) (*bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "":
		return nil, nil
	case "yes", "y", "true":
		t := true
		return &t, nil
	case "no", "n", "false":
		f := false
		return &f, nil
	}
	return nil, fmt.Errorf("%s is %q; use yes or no", name, v)
}
