package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/usetrove/trove/internal/mcp"
	"github.com/usetrove/trove/internal/mcp/root"
	"github.com/usetrove/trove/internal/recovery"
)

func newMCP(a *App) *cobra.Command {
	var rootDir string
	c := &cobra.Command{
		Use:   "mcp",
		Short: "Run the Trove MCP server (stdio) that agents use for cheaper reads, searches and test runs",
		Long: "Runs the MCP server over stdin/stdout. You normally do not run this yourself: `trove mcp install` registers it\n" +
			"with Claude Code and Codex CLI, and they start it when needed. It only reads inside the project folder\n" +
			"(the current folder, or --root) and only runs commands listed in .trove/commands.toml.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if term.IsTerminal(int(os.Stdin.Fd())) { //nolint:gosec // fd fits in int
				return errors.New("`trove mcp` is a server that talks to an agent over stdin/stdout, so it does nothing from a terminal. " +
					"Run `trove mcp install` to register it with your agent, or `trove mcp schema-size` to see what it adds to each request")
			}
			o, err := mcpOptions(a, rootDir)
			if err != nil {
				return err
			}
			return mcp.Serve(c.Context(), o)
		},
	}
	c.Flags().StringVar(&rootDir, "root", "", "project folder the tools may read (default: the current folder)")
	c.AddCommand(newMCPInstall(a), newMCPUninstall(a), newMCPStatus(a), newMCPSchemaSize(a), newMCPSnippet(a), newMCPTrust(a))
	return c
}

// mcpOptions opens the project root and the recovery store.
func mcpOptions(a *App, rootDir string) (mcp.Options, error) {
	dir, cfg, err := loadConfig(a)
	if err != nil {
		return mcp.Options{}, err
	}
	if rootDir == "" {
		if rootDir, err = os.Getwd(); err != nil {
			return mcp.Options{}, errors.New("cannot tell which folder you are in. Pass --root <folder>")
		}
	}
	r, err := root.New(rootDir)
	if err != nil {
		return mcp.Options{}, err
	}
	st, err := recovery.Open(filepath.Join(dir, "recovery"), recovery.Options{
		MaxBytes: int64(cfg.Recovery.MaxMB) << 20,
		TTL:      time.Duration(cfg.Recovery.TTLDays) * 24 * time.Hour,
	})
	if err != nil {
		return mcp.Options{}, err
	}
	return mcp.Options{Root: r, Store: st, Trust: mcp.OpenTrust(dir), Version: Version}, nil
}

// stdinIsTerminal is a variable so tests can stand in for a person at a keyboard.
func defaultStdinIsTerminal() bool { return term.IsTerminal(int(os.Stdin.Fd())) } //nolint:gosec // fd fits in int

var stdinIsTerminal = defaultStdinIsTerminal

func newMCPTrust(a *App) *cobra.Command {
	var rootDir string
	var revoke bool
	c := &cobra.Command{
		Use:   "trust",
		Short: "Review and approve the project's .trove/commands.toml so trove_run may run it (needs a terminal)",
		Long: "A repository can list any program in .trove/commands.toml, so `trove_run` refuses it until you have read it\n" +
			"and approved this exact version. Approval ends when the file changes. This command only works at a real\n" +
			"terminal, with no --yes, so an agent cannot approve a file on your behalf. Use --revoke to withdraw approval.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			dir, _, err := loadConfig(a)
			if err != nil {
				return err
			}
			if rootDir == "" {
				if rootDir, err = os.Getwd(); err != nil {
					return errors.New("cannot tell which folder you are in. Pass --root <folder>")
				}
			}
			r, err := root.New(rootDir)
			if err != nil {
				return err
			}
			ts := mcp.OpenTrust(dir)
			if revoke {
				if err := ts.Revoke(r.Dir()); err != nil {
					return err
				}
				return a.emit(map[string]any{"revoked": true}, "Approval withdrawn. trove_run will refuse this project's commands until you approve them again.")
			}
			cmds, hash, err := mcp.PendingCommands(r)
			if err != nil {
				return err
			}
			if hash == "" || len(cmds) == 0 {
				return errors.New("this project has no .trove/commands.toml with commands in it, so there is nothing to approve")
			}
			names := make([]string, 0, len(cmds))
			for n := range cmds {
				names = append(names, n)
			}
			sort.Strings(names)
			var b strings.Builder
			fmt.Fprintf(&b, "These commands would be run with your permissions when an agent calls trove_run:\n\n")
			for _, n := range names {
				fmt.Fprintf(&b, "  %-12s %s\n", n, strings.Join(cmds[n].Run, " "))
			}
			if !stdinIsTerminal() {
				_, _ = fmt.Fprint(a.Out, b.String())
				return errors.New("approving commands needs a person at a terminal, so nothing was approved. Run `trove mcp trust` yourself in this project folder")
			}
			_, _ = fmt.Fprintf(a.Out, "%s\n", b.String())
			if !a.confirm("Approve exactly these commands for this project?") {
				return a.emit(map[string]any{"approved": false}, "Not approved. Nothing changed.")
			}
			if err := ts.Approve(r.Dir(), hash); err != nil {
				return err
			}
			return a.emit(map[string]any{"approved": true, "commands": names}, "Approved. If .trove/commands.toml changes, you will need to approve it again.")
		},
	}
	c.Flags().StringVar(&rootDir, "root", "", "project folder (default: the current folder)")
	c.Flags().BoolVar(&revoke, "revoke", false, "withdraw approval for this project")
	return c
}

func newMCPSchemaSize(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "schema-size",
		Short: "Show how many tokens the Trove tool definitions add to every request (estimated)",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			r, err := root.New(".")
			if err != nil {
				return err
			}
			sz, err := mcp.MeasureSchema(c.Context(), mcp.Options{Root: r, Version: Version})
			if err != nil {
				return err
			}
			text := fmt.Sprintf("%d tools, %d bytes, about %d tokens per request (estimated)\n", sz.Tools, sz.Bytes, sz.EstimatedTokens)
			for _, n := range []string{"trove_read", "trove_search", "trove_run", "trove_fetch_full"} {
				text += fmt.Sprintf("  %-17s about %d tokens\n", n, sz.PerTool[n])
			}
			text += fmt.Sprintf("optional context-file snippet: about %d tokens (estimated); `trove mcp snippet` prints it", mcp.SnippetTokens())
			return a.emit(map[string]any{"schema": sz, "snippet_estimated_tokens": mcp.SnippetTokens()}, text)
		},
	}
}

func newMCPSnippet(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "snippet",
		Short: "Print the optional text to add to CLAUDE.md or AGENTS.md so the agent prefers Trove's tools",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return a.emit(map[string]any{"snippet": mcp.Snippet, "estimated_tokens": mcp.SnippetTokens(), "label": "estimated"}, mcp.Snippet)
		},
	}
}

// openRecovery opens the recovery store with the configured limits.
func openRecovery(a *App) (*recovery.Store, string, error) {
	dir, cfg, err := loadConfig(a)
	if err != nil {
		return nil, "", err
	}
	st, err := recovery.Open(filepath.Join(dir, "recovery"), recovery.Options{
		MaxBytes: int64(cfg.Recovery.MaxMB) << 20,
		TTL:      time.Duration(cfg.Recovery.TTLDays) * 24 * time.Hour,
	})
	return st, dir, err
}
