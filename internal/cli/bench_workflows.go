package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/stress"
)

func newWorkflows(a *App) *cobra.Command {
	var (
		seed   uint64
		model  string
		outDir string
	)
	c := &cobra.Command{
		Use:   "workflows",
		Short: "Replay scripted everyday Claude Code sessions through the engine and report three numbers",
		Long: "Runs hand-scripted sessions (fix a failing test, rename across a codebase, debug with logs, upgrade a\n" +
			"dependency, explore a repo, a long feature session, a quick question) through the real optimizer engine, offline:\n" +
			"no provider call and no spend. Reports an estimated saving, a measured cache-and-correctness check, and a measured\n" +
			"engine timing. The sessions are scripted, so the saving says what the optimizers do on these patterns, not what\n" +
			"any user will save.",
		Example: "  trove bench workflows\n  trove bench workflows --out ./workflows --json",
		Args:    cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			rep, err := stress.RunWorkflows(stress.WorkflowOptions{Seed: seed, Model: model})
			if err != nil {
				return err
			}
			md := stress.WorkflowMarkdown(rep)
			if outDir != "" {
				if err := os.MkdirAll(outDir, 0o750); err != nil {
					return fmt.Errorf("could not create %s: %w", outDir, err)
				}
				if err := os.WriteFile(filepath.Join(outDir, "workflows.md"), []byte(md), 0o600); err != nil {
					return fmt.Errorf("could not write workflows.md: %w", err)
				}
				b, err := json.MarshalIndent(rep, "", "  ")
				if err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(outDir, "workflows.json"), append(b, '\n'), 0o600); err != nil {
					return fmt.Errorf("could not write workflows.json: %w", err)
				}
			}
			if err := a.emit(rep, md); err != nil {
				return err
			}
			if rep.Headline.Violations > 0 || !rep.Headline.AllDeterministic {
				return fmt.Errorf("workflow replay found %d broken invariant(s), deterministic=%v. Do not quote the numbers; fix this first",
					rep.Headline.Violations, rep.Headline.AllDeterministic)
			}
			return nil
		},
	}
	f := c.Flags()
	f.Uint64Var(&seed, "seed", defaultJourneySeed, "random seed for the small variations (timings, ids); the same seed gives the same sessions")
	f.StringVar(&model, "model", "claude-sonnet-5-5", "pinned model id used to price the sessions")
	f.StringVar(&outDir, "out", "", "also write workflows.md and workflows.json into this folder")
	return c
}
