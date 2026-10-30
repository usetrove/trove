package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/hooks"
	"github.com/usetrove/trove/internal/mcp/install"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/setup"
	"github.com/usetrove/trove/internal/store"
)

type hooksInstallFlags struct {
	agent, scope, projectDir string
	yes, dryRun, largeOutput bool
}

// hooksPlan is one settings file Trove proposes to add hooks to.
type hooksPlan struct {
	Agent       string   `json:"agent"`
	Where       string   `json:"where,omitempty"`
	Summary     string   `json:"summary,omitempty"`
	Adds        string   `json:"adds,omitempty"`
	Note        string   `json:"note,omitempty"`
	AlreadyDone bool     `json:"already_done,omitempty"`
	Problem     string   `json:"problem,omitempty"`
	Hooks       []string `json:"hooks,omitempty"`

	edit *setup.Edit
}

type hooksInstallReport struct {
	Plans   []hooksPlan    `json:"plans"`
	Applied bool           `json:"applied"`
	Changes []setup.Change `json:"changes,omitempty"`
	Next    []string       `json:"next,omitempty"`
}

func newHooks(a *App) *cobra.Command {
	c := &cobra.Command{
		Use:   "hooks",
		Short: "Install, remove and inspect Trove's agent hooks (repeat-read prevention and session receipts)",
		Long: "Hooks are small commands Claude Code and Codex CLI run around tool calls and when a session ends. They work\n" +
			"without the gateway, so they also help if you use a subscription. See docs/hooks.md.",
	}
	c.AddCommand(newHooksInstall(a), newHooksUninstall(a), newHooksStatus(a), newHooksSync(a))
	return c
}

func newHooksInstall(a *App) *cobra.Command {
	var fl hooksInstallFlags
	c := &cobra.Command{
		Use:   "install",
		Short: "Add Trove's hooks to Claude Code and Codex CLI (shows the change first; asks before applying)",
		Long: "Adds Trove's hooks to your agent's hook configuration. Nothing changes until you agree (or pass --yes).\n" +
			"Your own hooks are never touched, the file is backed up first, and `trove hooks uninstall` (or `trove undo`) removes only Trove's entries.\n" +
			"Installing the hooks does not turn any optimization on. They stay inert until you set hook_repeat_read to \"shadow\" (estimate only) or\n" +
			"\"on\" under [optimizers] in config.toml; a session keeps the setting it started with.\n" +
			"Claude Code: ~/.claude/settings.json (--scope user) or .claude/settings.local.json in a project (--scope project).\n" +
			"Codex CLI: ~/.codex/hooks.json. Codex asks you to review new hooks with /hooks before they run.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return runHooksInstall(a, fl) },
	}
	c.Flags().StringVar(&fl.agent, "agent", "all", "which agent: claude, codex or all")
	c.Flags().StringVar(&fl.scope, "scope", "user", "Claude Code only: user (all projects) or project (.claude/settings.local.json)")
	c.Flags().StringVar(&fl.projectDir, "project", "", "project folder for --scope project (default: the current folder)")
	c.Flags().BoolVar(&fl.largeOutput, "large-output", false, "also add the optional hint that suggests trove_run for noisy commands (test suites, verbose builds)")
	c.Flags().BoolVar(&fl.yes, "yes", false, "apply without asking")
	c.Flags().BoolVar(&fl.dryRun, "dry-run", false, "show what would change and stop")
	return c
}

// hookCommands returns the command prefix that identifies Trove's hooks and a
// function that builds the full command for an event.
func hookCommands(a *App, dir string, agentFlag string) (base string, build func(event string) string) {
	bin := "trove"
	if a.Executable != nil {
		if p, err := a.Executable(); err == nil && p != "" {
			bin = p
		}
	}
	quote := func(s string) string {
		if strings.ContainsAny(s, " \t") {
			return `"` + s + `"`
		}
		return s
	}
	base = quote(bin) + " hook "
	suffix := ""
	if a.DataDir != "" || a.Getenv("TROVE_DATA_DIR") != "" {
		suffix += " --data-dir " + quote(dir)
	}
	if agentFlag != "" {
		suffix += " --agent " + agentFlag
	}
	return base, func(event string) string { return base + event + suffix }
}

func claudeSpecs(build func(string) string, largeOutput bool) []setup.HookSpec {
	s := []setup.HookSpec{
		{Event: "PreToolUse", Matcher: "Read", Command: build(hooks.EventPreRead), Timeout: 5},
		{Event: "PostToolUse", Matcher: "Read", Command: build(hooks.EventPostRead), Timeout: 5},
		{Event: "SessionStart", Command: build(hooks.EventReset), Timeout: 5},
		{Event: "PreCompact", Command: build(hooks.EventReset), Timeout: 5},
		{Event: "SessionEnd", Command: build(hooks.EventSessionEnd), Timeout: 3},
	}
	if largeOutput {
		s = append(s, setup.HookSpec{Event: "PreToolUse", Matcher: "Bash", Command: build(hooks.EventLargeOutput), Timeout: 5})
	}
	return s
}

// codexSpecs leaves out repeat-read: Codex's hook page names no file-read tool to
// match (checked 2026-10-30), so there is nothing to hook.
func codexSpecs(build func(string) string, largeOutput bool) []setup.HookSpec {
	s := []setup.HookSpec{
		{Event: "SessionStart", Command: build(hooks.EventReset), Timeout: 3},
		{Event: "SessionEnd", Command: build(hooks.EventSessionEnd), Timeout: 3},
	}
	if largeOutput {
		s = append(s, setup.HookSpec{Event: "PreToolUse", Matcher: "^Bash$", Command: build(hooks.EventLargeOutput), Timeout: 3})
	}
	return s
}

func planHooksInstall(a *App, dir, home string, fl hooksInstallFlags) ([]hooksPlan, error) {
	if fl.scope != "user" && fl.scope != "project" {
		return nil, fmt.Errorf("--scope %q is not valid; use user or project", fl.scope)
	}
	switch fl.agent {
	case "all", "claude", "codex":
	default:
		return nil, fmt.Errorf("--agent %q is not valid; use claude, codex or all", fl.agent)
	}
	var plans []hooksPlan
	for _, spec := range supportedAgents {
		isCodex := spec.binary == "codex"
		if (fl.agent == "claude" && isCodex) || (fl.agent == "codex" && !isCodex) {
			continue
		}
		cfgDir := spec.configDir(a, home)
		_, onPath := a.LookPath(spec.binary)
		if _, statErr := os.Stat(cfgDir); onPath != nil && statErr != nil {
			continue // not installed
		}
		p := hooksPlan{Agent: spec.name}
		var e *setup.Edit
		var err error
		switch {
		case isCodex && fl.scope == "project":
			p.Problem = "Codex CLI only reads project hooks for trusted projects, so Trove does not edit them. Run `trove hooks install --agent codex` to install at user level."
		case isCodex:
			base, build := hookCommands(a, dir, "codex")
			specs := codexSpecs(build, fl.largeOutput)
			p.Hooks = hookNames(specs)
			e, err = setup.PlanCodexHooks(cfgDir, specs, base)
		default:
			base, build := hookCommands(a, dir, "")
			specs := claudeSpecs(build, fl.largeOutput)
			p.Hooks = hookNames(specs)
			path := filepath.Join(cfgDir, "settings.json")
			if fl.scope == "project" {
				proj := fl.projectDir
				if proj == "" {
					if proj, err = os.Getwd(); err != nil {
						p.Problem = "cannot tell which folder you are in. Pass --project <folder>."
						break
					}
				}
				// settings.local.json is not committed, so a path that only exists on this machine stays here.
				path = filepath.Join(proj, ".claude", "settings.local.json")
			}
			e, err = setup.PlanClaudeHooks(path, specs, base)
		}
		switch {
		case err != nil:
			p.Problem = err.Error()
		case e != nil:
			p.Where, p.Summary, p.Adds, p.Note, p.AlreadyDone, p.edit = e.Path, e.Summary, e.Show, e.Note, e.NoOp, e
		}
		plans = append(plans, p)
	}
	return plans, nil
}

func hookNames(specs []setup.HookSpec) []string {
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.Event+" -> "+strings.TrimSpace(s.Command[strings.Index(s.Command, " hook ")+len(" hook "):]))
	}
	return out
}

func runHooksInstall(a *App, fl hooksInstallFlags) error {
	dir, cfg, err := loadConfig(a)
	if err != nil {
		return err
	}
	home, err := a.HomeDir()
	if err != nil {
		return errors.New("cannot find your home directory; set HOME and try again")
	}
	plans, err := planHooksInstall(a, dir, home, fl)
	if err != nil {
		return err
	}
	rep := hooksInstallReport{Plans: plans}
	if len(plans) == 0 {
		return a.emit(rep, "No supported agents found (looked for Claude Code and Codex CLI). Install one, then run this again.")
	}
	pending := 0
	var text strings.Builder
	for _, p := range plans {
		fmt.Fprintf(&text, "%s\n", p.Agent)
		switch {
		case p.Problem != "":
			fmt.Fprintf(&text, "  Cannot add hooks: %s\n", p.Problem)
		case p.AlreadyDone:
			text.WriteString("  Already installed; nothing to change.\n")
		default:
			pending++
			fmt.Fprintf(&text, "  %s\n  Where: %s\n  Adds:\n    %s\n", p.Summary, p.Where, strings.ReplaceAll(strings.TrimRight(p.Adds, "\n"), "\n", "\n    "))
			if p.Note != "" {
				fmt.Fprintf(&text, "  Note: %s\n", p.Note)
			}
		}
	}
	if pending == 0 {
		return a.emit(rep, strings.TrimRight(text.String(), "\n"))
	}
	if fl.dryRun {
		return a.emit(rep, text.String()+"Dry run: nothing was changed.")
	}
	if !a.JSON {
		_, _ = fmt.Fprint(a.Out, text.String())
	}
	consent := fl.yes
	if !consent && !a.JSON {
		consent = a.confirm(fmt.Sprintf("Add Trove's hooks to %d agent file(s)? They are backed up first and `trove hooks uninstall` reverses it.", pending))
	}
	if !consent {
		return a.emit(rep, "No changes made. Re-run with --yes (or answer y) to apply.")
	}
	if err := ensureConfigFile(dir); err != nil {
		return err
	}
	var edits []*setup.Edit
	for _, p := range plans {
		if p.edit != nil && !p.AlreadyDone && p.Problem == "" {
			edits = append(edits, p.edit)
		}
	}
	applied, err := setup.Apply(dir, edits, time.Now())
	rep.Changes, rep.Applied = applied, len(applied) > 0
	if err != nil {
		_ = a.emit(rep, "")
		return fmt.Errorf("%v. Run `trove hooks uninstall` to reverse anything already applied", err)
	}
	// Give the hooks their snapshot now, so they know the optimizers' states from the first session.
	if err := syncHooks(a, dir, cfg); err != nil {
		rep.Next = append(rep.Next, "Could not write the hook snapshot ("+err.Error()+"); run `trove hooks sync`.")
	}
	rep.Next = append(rep.Next,
		"Restart your agent (new sessions pick the hooks up).",
		"The hooks do nothing yet. To start in shadow mode (counts what it would skip, changes nothing), add under [optimizers] in "+filepath.Join(dir, config.FileName)+":  hook_repeat_read = \"shadow\"",
		"Codex CLI asks you to review new hooks: open /hooks in Codex and trust Trove's entries.",
		"See what the hooks did with `trove hooks status`. Reverse everything with `trove hooks uninstall`.",
	)
	return a.emit(rep, "Done.\n"+strings.Join(rep.Next, "\n"))
}

// syncHooks rewrites the snapshot the hooks read, ingesting their counters and
// running the guardrail when a database exists.
func syncHooks(a *App, dir string, cfg config.Config) error {
	ctx := context.Background()
	now := time.Now()
	if _, err := os.Stat(filepath.Join(dir, store.DBFileName)); err == nil {
		db, err := store.Open(dir)
		if err != nil {
			return err
		}
		defer func() { _ = db.Close() }()
		return hooks.Maintain(ctx, db, dir, cfg, a.Getenv, now)
	}
	return hooks.WriteSnapshot(dir, hooks.BuildSnapshot(cfg, nil, mcpRegistered(dir), now))
}

type hooksUninstallReport struct {
	Undone []setup.UndoResult `json:"undone"`
}

func newHooksUninstall(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove Trove's hooks from every agent file they were added to",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			dir, err := a.ResolveDataDir()
			if err != nil {
				return err
			}
			cs, err := setup.HooksInstalled(dir)
			if err != nil {
				return err
			}
			rep := hooksUninstallReport{Undone: []setup.UndoResult{}}
			if len(cs) == 0 {
				return a.emit(rep, "Nothing to remove: Trove has not added hooks to any agent.")
			}
			ids := make([]string, len(cs))
			for i, c := range cs {
				ids[i] = c.ID
			}
			if rep.Undone, err = setup.UndoSelected(dir, ids); err != nil {
				return err
			}
			var b strings.Builder
			for _, r := range rep.Undone {
				fmt.Fprintf(&b, "%s: %s (%s)\n", r.Agent, r.Detail, r.Path)
			}
			b.WriteString("Restart your agent to stop running them.")
			return a.emit(rep, b.String())
		},
	}
}

type hooksStatusReport struct {
	Installed  []string            `json:"installed"`
	Optimizers map[string]string   `json:"optimizers"`
	RolledBack []string            `json:"rolled_back"`
	Stats      []measure.HookStats `json:"stats"`
	Pending    int                 `json:"pending_events"`
	Notes      []string            `json:"notes,omitempty"`
}

func newHooksStatus(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show where the hooks are installed and what they have done",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			dir, cfg, err := loadConfig(a)
			if err != nil {
				return err
			}
			rep := hooksStatusReport{Installed: []string{}, Optimizers: map[string]string{}, RolledBack: []string{}, Stats: []measure.HookStats{}}
			cs, err := setup.HooksInstalled(dir)
			if err != nil {
				return err
			}
			for _, c := range cs {
				rep.Installed = append(rep.Installed, c.Path)
			}
			for _, n := range hooks.OptimizerNames {
				rep.Optimizers[n] = cfg.OptimizerState(n)
			}
			if snap, ok := hooks.LoadSnapshot(dir); ok {
				rep.RolledBack = append(rep.RolledBack, snap.RolledBack...)
			}
			if err := syncHooks(a, dir, cfg); err != nil {
				rep.Notes = append(rep.Notes, "Could not refresh the hook counts: "+err.Error())
			}
			if db, _ := openExisting(dir); db != nil {
				defer func() { _ = db.Close() }()
				if rep.Stats, err = measure.HookTotals(context.Background(), db, time.Now().Add(-30*24*time.Hour)); err != nil {
					return fmt.Errorf("cannot read hook counts: %v. Run `trove doctor` for details", err)
				}
			}
			return a.emit(rep, renderHooksStatus(rep))
		},
	}
}

func renderHooksStatus(r hooksStatusReport) string {
	var b strings.Builder
	if len(r.Installed) == 0 {
		b.WriteString("Hooks are not installed. Run `trove hooks install`.\n")
	} else {
		b.WriteString("Installed in: " + strings.Join(r.Installed, ", ") + "\n")
	}
	for _, n := range hooks.OptimizerNames {
		fmt.Fprintf(&b, "  %s: %s\n", n, r.Optimizers[n])
	}
	if len(r.RolledBack) > 0 {
		b.WriteString("Rolled back to shadow by a guardrail: " + strings.Join(r.RolledBack, ", ") + "\n")
	}
	if len(r.Stats) == 0 {
		b.WriteString("No hook activity recorded in the last 30 days.")
		return b.String()
	}
	b.WriteString("Last 30 days (counts are measured; token savings are estimated):\n")
	for _, s := range r.Stats {
		fmt.Fprintf(&b, "  %s: %d blocked, %d would block (shadow), %d advised, %d would advise (shadow), %d re-read anyway",
			s.Hook, s.Blocked, s.WouldBlock, s.Advised, s.WouldAdvise, s.RereadAnyway)
		if s.RereadRate != nil {
			fmt.Fprintf(&b, " (%.0f%% of blocks, measured)", s.RereadRate.Value)
		}
		if s.Blocked > 0 {
			fmt.Fprintf(&b, "; about %.0f tokens kept out of context (estimated)", s.TokensSaved.Value)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func newHooksSync(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "sync",
		Short: "Ingest the hooks' counters, run the guardrail and refresh what the hooks read",
		Long:  "Run this after editing [optimizers] in config.toml to refresh the snapshot the hooks read. Session end does the same automatically.",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			dir, cfg, err := loadConfig(a)
			if err != nil {
				return err
			}
			if err := syncHooks(a, dir, cfg); err != nil {
				return fmt.Errorf("cannot sync the hooks: %v. Run `trove doctor` for details", err)
			}
			return a.emit(map[string]bool{"synced": true}, "Hook snapshot refreshed.")
		},
	}
}

func mcpRegistered(dir string) bool { return install.Registered(dir) }
