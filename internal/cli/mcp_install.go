package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/mcp/install"
	"github.com/usetrove/trove/internal/setup"
)

type mcpInstallFlags struct {
	scope, agent string
	yes, dryRun  bool
	projectDir   string
}

// mcpPlan is one registration Trove proposes.
type mcpPlan struct {
	Agent       string `json:"agent"`
	Scope       string `json:"scope"`
	Where       string `json:"where,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Adds        string `json:"adds,omitempty"`
	Note        string `json:"note,omitempty"`
	AlreadyDone bool   `json:"already_done,omitempty"`
	Problem     string `json:"problem,omitempty"`

	edit *setup.Edit
	add  []string // an agent command to run instead of a file edit
	rm   []string
}

type mcpInstallReport struct {
	Plans   []mcpPlan      `json:"plans"`
	Applied bool           `json:"applied"`
	Changes []setup.Change `json:"changes,omitempty"`
	Next    []string       `json:"next,omitempty"`
}

func newMCPInstall(a *App) *cobra.Command {
	var fl mcpInstallFlags
	c := &cobra.Command{
		Use:   "install",
		Short: "Register the Trove MCP server with Claude Code and Codex CLI (shows the change first; asks before applying)",
		Long: "Detects Claude Code and Codex CLI and shows exactly how each would be told to start `trove mcp`.\n" +
			"Nothing changes until you agree (or pass --yes). Edited files are backed up and `trove mcp uninstall` reverses everything.\n" +
			"Claude Code, user scope (the default): Trove runs Claude Code's own `claude mcp add`; it never edits ~/.claude.json.\n" +
			"Claude Code, project scope (--scope project): adds the server to .mcp.json in the project, which Claude Code asks you to approve.\n" +
			"Codex CLI: adds a [mcp_servers.trove] table to ~/.codex/config.toml (user level only).",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return runMCPInstall(a, fl) },
	}
	c.Flags().StringVar(&fl.scope, "scope", "user", "where to register: user (all projects) or project (.mcp.json, Claude Code only)")
	c.Flags().StringVar(&fl.agent, "agent", "all", "which agent: claude, codex or all")
	c.Flags().BoolVar(&fl.yes, "yes", false, "apply without asking")
	c.Flags().BoolVar(&fl.dryRun, "dry-run", false, "show what would change and stop")
	c.Flags().StringVar(&fl.projectDir, "project", "", "project folder for --scope project (default: the current folder)")
	return c
}

// serverCommand is the program agents run to start the server: plain `trove`
// when that name already finds this binary, otherwise its absolute path.
func serverCommand(a *App) string {
	exe := ""
	if a.Executable != nil {
		exe, _ = a.Executable()
	}
	if p, err := a.LookPath("trove"); err == nil && exe != "" {
		if same(p, exe) {
			return "trove"
		}
	}
	if exe == "" {
		return "trove"
	}
	return exe
}

func same(a, b string) bool {
	ra, e1 := filepath.EvalSymlinks(a)
	rb, e2 := filepath.EvalSymlinks(b)
	if e1 != nil || e2 != nil {
		return false
	}
	ai, e1 := os.Stat(ra)
	bi, e2 := os.Stat(rb)
	return e1 == nil && e2 == nil && os.SameFile(ai, bi)
}

func planMCP(a *App, dir, home string, fl mcpInstallFlags) ([]mcpPlan, error) {
	if fl.scope != "user" && fl.scope != "project" {
		return nil, fmt.Errorf("--scope %q is not valid; use user or project", fl.scope)
	}
	switch fl.agent {
	case "all", "claude", "codex":
	default:
		return nil, fmt.Errorf("--agent %q is not valid; use claude, codex or all", fl.agent)
	}
	cmd := serverCommand(a)
	var plans []mcpPlan
	for _, spec := range supportedAgents {
		isCodex := spec.binary == "codex"
		if (fl.agent == "claude" && isCodex) || (fl.agent == "codex" && !isCodex) {
			continue
		}
		cfgDir := spec.configDir(a, home)
		_, onPath := a.LookPath(spec.binary)
		_, statErr := os.Stat(cfgDir)
		if onPath != nil && statErr != nil {
			continue // not installed
		}
		p := mcpPlan{Agent: spec.name, Scope: fl.scope}
		switch {
		case isCodex && fl.scope == "project":
			p.Problem = "Codex CLI only reads project config for trusted projects, so Trove does not edit it. Run `trove mcp install --agent codex` to register at user level."
		case isCodex:
			e, err := setup.PlanCodexMCP(cfgDir, cmd)
			planFromEdit(&p, e, err)
		case fl.scope == "project":
			proj := fl.projectDir
			if proj == "" {
				var err error
				if proj, err = os.Getwd(); err != nil {
					p.Problem = "cannot tell which folder you are in. Pass --project <folder>."
					break
				}
			}
			e, err := setup.PlanProjectMCP(proj, cmd)
			planFromEdit(&p, e, err)
		default:
			planClaudeUser(a, dir, cmd, &p, onPath == nil)
		}
		plans = append(plans, p)
	}
	return plans, nil
}

func planFromEdit(p *mcpPlan, e *setup.Edit, err error) {
	if err != nil {
		p.Problem = err.Error()
		return
	}
	p.Where, p.Summary, p.Adds, p.Note, p.AlreadyDone, p.edit = e.Path, e.Summary, e.Show, e.Note, e.NoOp, e
}

// planClaudeUser plans the user-scope registration through `claude mcp add`.
func planClaudeUser(a *App, dir, cmd string, p *mcpPlan, haveCLI bool) {
	recs, err := install.LoadCLI(dir)
	if err != nil {
		p.Problem = err.Error()
		return
	}
	for _, r := range recs {
		if r.Agent == p.Agent && r.Scope == "user" {
			p.Summary, p.AlreadyDone = "Already registered with Claude Code (user scope).", true
			return
		}
	}
	if !haveCLI {
		p.Problem = "the `claude` command was not found on your PATH, and Trove does not edit ~/.claude.json itself. " +
			"Put `claude` on your PATH and run this again, or use `trove mcp install --scope project` to add the server to this project's .mcp.json."
		return
	}
	if a.Run != nil && a.Run("claude", "mcp", "get", setup.MCPServerName) == nil {
		p.Problem = "Claude Code already has an MCP server named \"trove\" that Trove did not add. Remove or rename it, then run this again."
		return
	}
	p.add = []string{"claude", "mcp", "add", "--transport", "stdio", "--scope", "user", setup.MCPServerName, "--", cmd, "mcp"}
	p.rm = []string{"claude", "mcp", "remove", setup.MCPServerName, "--scope", "user"}
	p.Where = "Claude Code user settings (managed by `claude mcp`)"
	p.Summary = "Register the Trove MCP server with Claude Code for all your projects."
	p.Adds = strings.Join(p.add, " ")
	p.Note = "Trove runs this command for you; it does not read or edit ~/.claude.json."
}

func runMCPInstall(a *App, fl mcpInstallFlags) error {
	dir, _, err := loadConfig(a)
	if err != nil {
		return err
	}
	home, err := a.HomeDir()
	if err != nil {
		return errors.New("cannot find your home directory; set HOME and try again")
	}
	plans, err := planMCP(a, dir, home, fl)
	if err != nil {
		return err
	}
	rep := mcpInstallReport{Plans: plans}
	if len(plans) == 0 {
		return a.emit(rep, "No supported agents found (looked for Claude Code and Codex CLI). Install one, then run this again.")
	}
	pending := 0
	var text strings.Builder
	for _, p := range plans {
		fmt.Fprintf(&text, "%s (%s scope)\n", p.Agent, p.Scope)
		switch {
		case p.Problem != "":
			fmt.Fprintf(&text, "  Cannot register: %s\n", p.Problem)
		case p.AlreadyDone:
			text.WriteString("  Already registered; nothing to change.\n")
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
		consent = a.confirm(fmt.Sprintf("Register Trove's MCP server with %d agent(s)? Edited files are backed up and `trove mcp uninstall` reverses it.", pending))
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
		return fmt.Errorf("%v. Run `trove mcp uninstall` to reverse anything already applied", err)
	}
	for _, p := range plans {
		if p.add == nil || p.AlreadyDone || p.Problem != "" {
			continue
		}
		if err := a.Run(p.add[0], p.add[1:]...); err != nil {
			_ = a.emit(rep, "")
			return fmt.Errorf("Claude Code did not accept the registration: %v. Nothing was recorded for Claude Code; run `trove mcp uninstall` to reverse anything else", err)
		}
		recs, lerr := install.LoadCLI(dir)
		if lerr != nil {
			return lerr
		}
		recs = append(recs, install.CLIInstall{Agent: p.Agent, Scope: p.Scope, Remove: p.rm, AppliedAt: time.Now().UTC()})
		if err := install.SaveCLI(dir, recs); err != nil {
			return fmt.Errorf("registered with Claude Code but could not record it (%v). Undo it yourself with: %s", err, strings.Join(p.rm, " "))
		}
		rep.Applied = true
	}
	rep.Next = []string{
		"Restart your agent so it picks up the new tools.",
		"Add .trove/commands.toml to a project to let trove_run run its tests, build and lint (see docs/mcp.md).",
		"Optional: `trove mcp snippet` prints a few lines for CLAUDE.md or AGENTS.md that tell the agent when to prefer these tools.",
		"Reverse everything with `trove mcp uninstall`.",
	}
	return a.emit(rep, "Done. "+strings.Join(rep.Next, "\n"))
}

type mcpUninstallReport struct {
	Undone  []setup.UndoResult `json:"undone"`
	Removed []string           `json:"removed"`
	Failed  []string           `json:"failed,omitempty"`
}

func newMCPUninstall(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the Trove MCP server from every agent it was registered with",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			dir, err := a.ResolveDataDir()
			if err != nil {
				return err
			}
			rep := mcpUninstallReport{Undone: []setup.UndoResult{}, Removed: []string{}}
			cs, err := install.MCPChanges(dir)
			if err != nil {
				return err
			}
			if len(cs) > 0 {
				ids := make([]string, len(cs))
				for i, c := range cs {
					ids[i] = c.ID
				}
				if rep.Undone, err = setup.UndoSelected(dir, ids); err != nil {
					return err
				}
			}
			recs, err := install.LoadCLI(dir)
			if err != nil {
				return err
			}
			var keep []install.CLIInstall
			for _, r := range recs {
				if len(r.Remove) == 0 || a.Run == nil {
					keep = append(keep, r)
					continue
				}
				if err := a.Run(r.Remove[0], r.Remove[1:]...); err != nil {
					rep.Failed = append(rep.Failed, fmt.Sprintf("%s: %v. Run it yourself: %s", r.Agent, err, strings.Join(r.Remove, " ")))
					keep = append(keep, r)
					continue
				}
				rep.Removed = append(rep.Removed, r.Agent+" ("+r.Scope+" scope)")
			}
			if err := install.SaveCLI(dir, keep); err != nil {
				return err
			}
			if len(rep.Undone) == 0 && len(rep.Removed) == 0 && len(rep.Failed) == 0 {
				return a.emit(rep, "Nothing to remove: Trove has not registered its MCP server with any agent.")
			}
			var b strings.Builder
			for _, r := range rep.Undone {
				fmt.Fprintf(&b, "%s: %s (%s)\n", r.Agent, r.Detail, r.Path)
			}
			for _, r := range rep.Removed {
				fmt.Fprintf(&b, "Removed from %s.\n", r)
			}
			for _, f := range rep.Failed {
				fmt.Fprintf(&b, "Could not remove: %s\n", f)
			}
			b.WriteString("Restart your agent to drop the tools.")
			text := b.String()
			if len(rep.Failed) > 0 {
				_ = a.emit(rep, text)
				return errors.New("some registrations could not be removed; see above")
			}
			return a.emit(rep, text)
		},
	}
}

type mcpStatusReport struct {
	Registered bool     `json:"registered"`
	Where      []string `json:"where"`
	Recovery   struct {
		Entries int   `json:"entries"`
		Bytes   int64 `json:"bytes"`
	} `json:"recovery"`
}

func newMCPStatus(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show where the Trove MCP server is registered and what the recovery store holds",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			dir, _, err := loadConfig(a)
			if err != nil {
				return err
			}
			rep := mcpStatusReport{Where: []string{}}
			cs, err := install.MCPChanges(dir)
			if err != nil {
				return err
			}
			for _, c := range cs {
				rep.Where = append(rep.Where, c.Path)
			}
			recs, err := install.LoadCLI(dir)
			if err != nil {
				return err
			}
			for _, r := range recs {
				rep.Where = append(rep.Where, "Claude Code ("+r.Scope+" scope)")
			}
			rep.Registered = len(rep.Where) > 0
			if st, _, err := openRecovery(a); err == nil {
				if s, err := st.Stats(); err == nil {
					rep.Recovery.Entries, rep.Recovery.Bytes = s.Entries, s.Bytes
				}
			}
			text := "Not registered with any agent. Run `trove mcp install`."
			if rep.Registered {
				text = "Registered: " + strings.Join(rep.Where, ", ")
			}
			text += fmt.Sprintf("\nRecovery store: %d outputs, %d bytes", rep.Recovery.Entries, rep.Recovery.Bytes)
			return a.emit(rep, text)
		},
	}
}
