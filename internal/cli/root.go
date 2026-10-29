package cli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/redact"
)

// Execute runs the CLI with args and returns the process exit code.
func Execute(a *App, args []string) int {
	root := NewRoot(a)
	root.SetArgs(args)
	root.SetOut(a.Out)
	root.SetErr(a.Err)
	ctx := a.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if err := root.ExecuteContext(ctx); err != nil {
		msg := redact.String(err.Error())
		if a.JSON {
			_ = json.NewEncoder(a.Err).Encode(map[string]string{"error": msg})
		} else {
			_, _ = fmt.Fprintln(a.Err, "Error: "+msg)
		}
		return 1
	}
	return 0
}

// NewRoot builds the command tree.
func NewRoot(a *App) *cobra.Command {
	// On Windows cobra checks whether the program was started from Explorer, which
	// walks the process list and costs about 30 ms on every command. That would
	// blow the status line's 50 ms budget, and Trove is a terminal tool.
	cobra.MousetrapHelpText = ""
	var rootPeriod periodFlags
	root := &cobra.Command{
		Use:           "trove",
		Short:         "The efficiency layer for AI coding agents",
		Long:          "Trove cuts the tokens your AI coding agents waste, locally, and shows that results did not suffer.\nRun with no command to see your report.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          func(*cobra.Command, []string) error { return runReport(a, rootPeriod) },
	}
	rootPeriod.add(root)
	f := root.PersistentFlags()
	f.BoolVar(&a.JSON, "json", false, "output machine-readable JSON")
	f.BoolVar(&a.NoColor, "no-color", false, "disable colored output (NO_COLOR is also respected)")
	f.StringVar(&a.DataDir, "data-dir", "", "Trove data directory (default ~/.trove, or TROVE_DATA_DIR)")
	f.BoolVar(&a.Verbose, "verbose", false, "print debug logs to stderr")

	cfg := &cobra.Command{Use: "config", Short: "Read or change Trove settings", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error { return c.Help() }}
	cfg.AddCommand(
		stubArgCmd(a, "get [key]", "Show a setting (or all settings)", "config get", cobra.MaximumNArgs(1)),
		stubArgCmd(a, "set <key> <value>", "Change a setting", "config set", cobra.ExactArgs(2)),
	)

	root.AddCommand(
		newSetup(a),
		newGateway(a),
		newReport(a),
		newBudget(a),
		newReceipts(a),
		newStatusLine(a),
		stubCmd(a, "fix", "Apply safe configuration fixes", "fix"),
		newUndo(a),
		newShare(a),
		cfg,
		newDoctor(a),
		newPricing(a),
		newOptimize(a),
		newVersion(a),
		stubCmd(a, "uninstall", "Remove Trove's changes and data", "uninstall"),
	)
	return root
}

func stub(a *App, name string) func(*cobra.Command, []string) error {
	return func(*cobra.Command, []string) error {
		return a.emit(map[string]string{"command": name, "status": "not_implemented"}, "trove "+name+": not implemented yet")
	}
}

func stubCmd(a *App, use, short, name string) *cobra.Command {
	return stubArgCmd(a, use, short, name, cobra.NoArgs)
}

func stubArgCmd(a *App, use, short, name string, args cobra.PositionalArgs) *cobra.Command {
	return &cobra.Command{Use: use, Short: short, Args: args, RunE: stub(a, name)}
}
