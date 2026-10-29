package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	c.AddCommand(newMCPInstall(a), newMCPUninstall(a), newMCPStatus(a), newMCPSchemaSize(a), newMCPSnippet(a))
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
	return mcp.Options{Root: r, Store: st, Version: Version}, nil
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
