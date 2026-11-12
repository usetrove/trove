package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/stress"
)

func newStress(a *App) *cobra.Command {
	var (
		mode   string
		seed   uint64
		layers []string
		soak   time.Duration
		sample int
		replay string
		outDir string
		strict bool
	)
	c := &cobra.Command{
		Use:   "stress",
		Short: "Stress the engine and gateway: realism, fuzzing, invariants, load and failure injection",
		Long: "Runs five layers offline, with no provider call and no spend: realism (estimated savings across random\n" +
			"sessions), fuzzing (hostile inputs against every optimizer), invariants (must hold on every request),\n" +
			"load and soak (concurrency, latency, memory), and failure injection (every case must fail open).\n" +
			"Only the realism layer reports savings, and they are labeled estimated. The command exits non-zero on any failure.",
		Example: "  trove bench stress --mode smoke\n" +
			"  trove bench stress --mode full --soak 4h --out ./stress-nightly\n" +
			"  trove bench stress --layers fuzz,faults --seed 20261108 --json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rep, err := stress.Run(stress.Options{
				Mode: stress.Mode(mode), Seed: seed, Layers: layers, Soak: soak, Samples: sample, ReplayDir: replay,
			})
			if err != nil {
				return err
			}
			md := stress.Markdown(rep)
			if outDir != "" {
				if err := writeStressFiles(outDir, rep, md); err != nil {
					return err
				}
				if !a.JSON {
					_, _ = fmt.Fprintf(a.Out, "Wrote %s and %s\n", filepath.Join(outDir, "stress.json"), filepath.Join(outDir, "stress.md"))
				}
			}
			if err := a.emit(rep, md); err != nil {
				return err
			}
			if skipped := stress.SkippedLayers(rep); len(skipped) > 0 {
				return fmt.Errorf("stress run incomplete: layer(s) %v were skipped. A skipped layer is not a pass; "+
					"fix the missing prerequisite and rerun", skipped)
			}
			if rep.Status == stress.Fail {
				return fmt.Errorf("stress run failed: %d failure(s). See the failures above; each one lists a repro command", rep.Failures)
			}
			if strict && rep.Checks.Skipped > 0 {
				return fmt.Errorf("stress run has %d skipped check(s) and --strict is set. Skipped checks are not passes", rep.Checks.Skipped)
			}
			return nil
		},
	}
	f := c.Flags()
	f.StringVar(&mode, "mode", "smoke", "smoke (for CI, well under a minute) or full (for nightly)")
	f.Uint64Var(&seed, "seed", defaultJourneySeed, "random seed; the same seed gives the same samples and inputs")
	f.StringSliceVar(&layers, "layers", nil, "run only these layers: realism, fuzz, invariants, load, faults (default all)")
	f.DurationVar(&soak, "soak", 0, "soak duration for the load layer (0 uses the mode default: 5s smoke, 15m full)")
	f.IntVar(&sample, "samples", 0, "realism sessions to sample (0 uses the mode default)")
	f.StringVar(&replay, "replay-dir", "", "folder of Claude Code session logs to replay (not implemented in this build; reported as skipped)")
	f.StringVar(&outDir, "out", "", "write stress.json and stress.md into this folder")
	f.BoolVar(&strict, "strict", false, "also fail when any check was skipped (CI uses this)")
	return c
}

// writeStressFiles writes the report. It holds no request text, no key and no path inside the logs, only
// counts, sizes, hashes, measured numbers and the repro commands.
func writeStressFiles(dir string, rep stress.Report, md string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("could not create %s: %w", dir, err)
	}
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "stress.json"), append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("could not write stress.json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stress.md"), []byte(md), 0o600); err != nil {
		return fmt.Errorf("could not write stress.md: %w", err)
	}
	return nil
}
