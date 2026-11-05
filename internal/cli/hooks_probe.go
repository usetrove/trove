package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/hooks"
	hookprobe "github.com/usetrove/trove/internal/hooks/probe"
)

// runProbeHook runs a probe hook (`trove hook probe_pre|probe_post`). It returns
// false when args are not a probe event.
func runProbeHook(args []string, stdin io.Reader, stdout io.Writer) bool {
	if len(args) == 0 || (args[0] != "probe_pre" && args[0] != "probe_post") {
		return false
	}
	hookprobe.HookMain(strings.TrimPrefix(args[0], "probe_"), stdin, stdout, os.Getenv)
	return true
}

type hooksProbeFlags struct {
	live      bool
	maxUSD    float64
	model     string
	claude    string
	tools     []string
	recordDir string
}

func newHooksProbe(a *App) *cobra.Command {
	var fl hooksProbeFlags
	c := &cobra.Command{
		Use:   "probe",
		Short: "Check which tool calls your agent version lets hooks rewrite",
		Long: "Hooks can change a tool's input before it runs and its output before the model sees it, but an agent version can\n" +
			"silently ignore either for some tools. The probe finds out. Without --live it only reads the version and reports what\n" +
			"the documentation says; nothing is run. With --live it runs one tiny Claude Code session per tool (a few cents on a\n" +
			"small model, capped by --max-usd) in a throwaway folder, with your own login, and records the result.\n" +
			"Until a tool has passed, Trove's rewriting optimizers stay in shadow mode for it. Run it again after each agent update.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runHooksProbe(cmd.Context(), a, fl) },
	}
	c.Flags().BoolVar(&fl.live, "live", false, "run real agent sessions (costs a few cents; needs --max-usd)")
	c.Flags().Float64Var(&fl.maxUSD, "max-usd", 0, "spending cap in USD for a live run, split across tools")
	c.Flags().StringVar(&fl.model, "model", "claude-haiku-5-5", "model for the probe sessions")
	c.Flags().StringVar(&fl.claude, "claude", "claude", "path of the Claude Code binary")
	c.Flags().StringSliceVar(&fl.tools, "tools", hookprobe.DefaultTools, "tools to probe")
	c.Flags().StringVar(&fl.recordDir, "record-dir", "", "save redacted copies of the hook inputs the agent sent (for test fixtures)")
	return c
}

func runHooksProbe(ctx context.Context, a *App, fl hooksProbeFlags) error {
	dir, _, err := loadConfig(a)
	if err != nil {
		return err
	}
	bin, err := troveBinary(a)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	rep, err := hookprobe.Run(ctx, hookprobe.Options{
		Claude: fl.claude, Live: fl.live, MaxUSD: fl.maxUSD, Model: fl.model, Tools: fl.tools, RecordDir: fl.recordDir,
		HookCommand: func(mode string) string { return shellQuote(bin) + " hook probe_" + mode },
	})
	if err != nil {
		return err
	}
	merged := mergeCapabilities(hooks.LoadCapabilities(dir), rep.Capabilities)
	if err := hooks.SaveCapabilities(dir, merged); err != nil {
		return fmt.Errorf("could not save the probe result: %v. Check the permissions of %s", err, dir)
	}
	return a.emit(rep, RenderProbe(rep))
}

// mergeCapabilities replaces the stored entries of every agent that was probed
// and keeps the rest.
func mergeCapabilities(old, fresh hooks.Capabilities) hooks.Capabilities {
	probed := map[string]bool{}
	for _, e := range fresh.Entries {
		probed[e.Agent] = true
	}
	var out hooks.Capabilities
	for _, e := range old.Entries {
		if !probed[e.Agent] {
			out.Entries = append(out.Entries, e)
		}
	}
	out.Entries = append(out.Entries, fresh.Entries...)
	return out
}

// RenderProbe is the plain-text result of a probe run.
func RenderProbe(rep hookprobe.Report) string {
	var b strings.Builder
	mode := "documentation only (nothing was run)"
	if rep.Live {
		mode = fmt.Sprintf("live (spent about $%.2f)", rep.SpentUSD)
	}
	fmt.Fprintf(&b, "Hook capabilities, Claude Code %s, %s\n\n", rep.AgentVersion, mode)
	for _, e := range rep.Capabilities.Entries {
		fmt.Fprintf(&b, "%s %s: rewrite input %s, rewrite output %s [%s]\n    %s\n", e.Agent, e.Tool, e.InputRewrite, e.OutputRewrite, e.Method, e.Evidence)
	}
	for _, n := range rep.Notes {
		fmt.Fprintf(&b, "\n%s\n", n)
	}
	b.WriteString("\nTrove rewrites a tool's input or output only where this shows \"yes\" from a live run. Run it again after updating the agent.\n")
	return b.String()
}

// hookRewritesCheck is the `trove doctor` line about hook rewriting.
func hookRewritesCheck(a *App, dir string, optimizerState func(string) string) check {
	const name = "hook rewrites"
	caps := hooks.LoadCapabilities(dir)
	type need struct{ opt, tool, kind string }
	needs := []need{
		{"hook_read_limit", "Read", hooks.RewriteInput},
		{"hook_quiet_commands", "Bash", hooks.RewriteInput},
	}
	if rewriteHooksInstalled(a) {
		needs = append(needs, need{"noise_normalizer", "Bash", hooks.RewriteOutput}, need{"output_trimmer", "Bash", hooks.RewriteOutput})
	}
	var blocked []string
	active := 0
	for _, n := range needs {
		if optimizerState(n.opt) == "off" {
			continue
		}
		active++
		if caps.Supports("claude", n.tool, n.kind) {
			continue
		}
		blocked = append(blocked, fmt.Sprintf("%s (%s %s: %s)", n.opt, n.tool, n.kind, capReason(caps, n.tool)))
	}
	switch {
	case active == 0:
		return check{name, statusInfo, "no hook rewriting optimizer is turned on", ""}
	case len(blocked) > 0:
		return check{name, statusWarn, "these optimizers stay in shadow mode for hooks because the agent has not been seen to honour the rewrite: " + strings.Join(blocked, "; "),
			"run `trove hooks probe --live --max-usd 0.25` (runs a few tiny sessions on your own login)"}
	}
	d := "every enabled rewrite has been seen to work"
	if v := installedClaudeVersion(a); v != "" {
		for _, e := range caps.Entries {
			if e.Agent == "claude" && e.Method == "live" && e.AgentVersion != v {
				return check{name, statusWarn, fmt.Sprintf("probed on Claude Code %s, but %s is installed now; an update can change what hooks may rewrite", e.AgentVersion, v),
					"run `trove hooks probe --live --max-usd 0.25` again"}
			}
		}
	}
	return check{name, statusOK, d, ""}
}

func capReason(c hooks.Capabilities, tool string) string {
	e, ok := c.Find("claude", tool)
	switch {
	case !ok:
		return "never probed on this machine"
	case e.Method != "live":
		return "only the documentation was read; no live probe yet"
	}
	return e.Evidence
}

func installedClaudeVersion(a *App) string {
	bin, err := a.LookPath("claude")
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	v, err := hookprobe.Version(ctx, hookprobe.Options{Claude: bin})
	if err != nil {
		return ""
	}
	return v
}

// rewriteHooksInstalled reports whether the PostToolUse output hook is in the
// user's Claude Code settings, without parsing the file.
func rewriteHooksInstalled(a *App) bool {
	home, err := a.HomeDir()
	if err != nil {
		return false
	}
	b, err := os.ReadFile(home + string(os.PathSeparator) + ".claude" + string(os.PathSeparator) + "settings.json") //nolint:gosec // the user's own agent settings
	return err == nil && strings.Contains(string(b), hooks.EventPostOutput)
}
