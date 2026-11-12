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
	f.BoolVar(&a.NoHistory, "no-history", false, "do not read your agent's local session history for this run")

	root.AddCommand(
		newSetup(a),
		newGateway(a),
		newReport(a),
		newLogs(a),
		newBudget(a),
		newBench(a),
		newReceipts(a),
		newStatusLine(a),
		newFix(a),
		newSettings(a),
		newTasks(a),
		newUndo(a),
		newShare(a),
		newDoctor(a),
		newPricing(a),
		newOptimize(a),
		newMCP(a),
		newEngine(a),
		newExternal(a),
		newExperiment(a),
		newTune(a),
		newRecommend(a),
		newHooks(a),
		newHookCmd(a),
		newPlugin(a),
		newVersion(a),
		newReconcile(a),
		newFeedback(a),
		newContribute(a),
		newData(a),
		newUninstall(a),
	)
	return root
}
