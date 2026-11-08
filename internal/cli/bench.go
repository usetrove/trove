package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/journey"
	"github.com/usetrove/trove/internal/mcp"
)

// defaultJourneySeed is the seed the README commands use. Changing it changes every number.
const defaultJourneySeed = 20261108

func newBench(a *App) *cobra.Command {
	c := &cobra.Command{
		Use:   "bench",
		Short: "Simulate user journeys and measure what Trove changes",
		Long: "Simulates realistic users over a simulated week and reports what Trove changes in tokens and\n" +
			"list-price cost. The offline tier is deterministic and spends nothing; its numbers are estimates.",
	}
	c.AddCommand(newJourneys(a))
	return c
}

// journeysOutput is the --json shape when --sensitivity is given.
type journeysOutput struct {
	Main        journey.Result `json:"main"`
	Sensitivity journey.Result `json:"sensitivity"`
}

func newJourneys(a *App) *cobra.Command {
	var (
		tier        string
		seed        uint64
		weeks       int
		profiles    []string
		list        bool
		budgetMS    int
		workers     int
		sensitivity bool
		calibrate   string
		outDir      string
	)
	c := &cobra.Command{
		Use:   "journeys",
		Short: "Run the user-journey simulation (offline tier) or calibrate it against your logs",
		Example: "  trove bench journeys --tier offline --seed 20261108\n" +
			"  trove bench journeys --tier offline --profile daily-pro --weeks 2 --json\n" +
			"  trove bench journeys --tier offline --sensitivity --out ./proof\n" +
			"  trove bench journeys --calibrate ~/.claude/projects --out ./proof",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if list {
				return journeyList(a)
			}
			if calibrate != "" {
				return journeyCalibrate(a, calibrate, outDir)
			}
			if tier != "offline" {
				return fmt.Errorf("the %q tier is not available from this command. The live tier runs real agent sessions on your API key and needs a budget you approve first; it runs from the bench/ directory", tier)
			}
			ver, err := mcp.MeasureSchema(context.Background(), mcp.Options{Version: "bench"})
			if err != nil {
				return fmt.Errorf("could not measure Trove's tool definitions: %w", err)
			}
			opts := journey.Options{
				Seed: seed, Weeks: weeks, Profiles: profiles, MCPSchemaBytes: ver.Bytes,
				BudgetMS: budgetMS, Workers: workers,
			}
			res, err := journey.Run(opts)
			if err != nil {
				return err
			}
			if !sensitivity {
				if outDir != "" {
					if err := writeJourneyFiles(outDir, res, nil); err != nil {
						return err
					}
					if !a.JSON {
						fmt.Fprintf(a.Out, "Wrote %s and %s\n",
							filepath.Join(outDir, "journeys.json"), filepath.Join(outDir, "journeys.md"))
					}
				}
				return a.emit(res, journey.Markdown(res))
			}
			prod, err := journey.Sensitivity(opts)
			if err != nil {
				return err
			}
			sens := journey.SensitivityMarkdown(res, prod)
			if outDir != "" {
				if err := writeJourneyFiles(outDir, res, &prod); err != nil {
					return err
				}
			}
			return a.emit(journeysOutput{Main: res, Sensitivity: prod}, journey.Markdown(res)+"\n"+sens)
		},
	}
	f := c.Flags()
	f.StringVar(&tier, "tier", "offline", "offline (deterministic, no spend) or live (not available here)")
	f.Uint64Var(&seed, "seed", defaultJourneySeed, "random seed; the same seed gives identical numbers")
	f.IntVar(&weeks, "weeks", 4, "simulated weeks per profile; each week is one batch of the profile's sessions")
	f.StringSliceVar(&profiles, "profile", nil, "run only these profile IDs (repeatable; see --list)")
	f.BoolVar(&list, "list", false, "list the profiles and exit")
	f.IntVar(&budgetMS, "budget-ms", 0, "optimizer time budget in ms (0 = the simulator's budget; production is 25)")
	f.IntVar(&workers, "workers", 0, "sessions run at once (0 = every CPU)")
	f.BoolVar(&sensitivity, "sensitivity", false, "also run with the production 25 ms budget, one session at a time, and compare")
	f.StringVar(&calibrate, "calibrate", "", "compare the generator with real Claude Code session logs in this folder (aggregates only)")
	f.StringVar(&outDir, "out", "", "write the results and tables into this directory")
	return c
}

func journeyList(a *App) error {
	type row struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Billing  string `json:"billing"`
		Model    string `json:"model"`
		Sessions int    `json:"sessions_per_week"`
		Reason   string `json:"reason,omitempty"`
	}
	var rows []row
	var b strings.Builder
	for _, p := range journey.Profiles() {
		r := row{ID: p.ID, Name: p.Name, Billing: p.Billing, Model: string(p.Model()), Sessions: p.Sessions, Reason: p.NotMeasurableReason()}
		rows = append(rows, r)
		fmt.Fprintf(&b, "%-20s %-30s %-12s %s\n", r.ID, r.Name, r.Billing, r.Model)
	}
	return a.emit(rows, strings.TrimRight(b.String(), "\n"))
}

// journeyCalibrate regenerates the calibration targets from a folder of session
// logs and runs the gate against them. Its --out folder gets
// calibration_targets.json, which is the file to commit to internal/journey.
func journeyCalibrate(a *App, dir, outDir string) error {
	t, err := journey.CalibrateLogs(dir)
	if err != nil {
		return err
	}
	st := journey.CheckCalibration(t, journey.DefaultTolerance)
	md := journey.CalibrationMarkdown(t, st)
	if outDir != "" {
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			return fmt.Errorf("could not create %s: %w", outDir, err)
		}
		b, err := json.MarshalIndent(t, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(outDir, "calibration_targets.json"), append(b, '\n'), 0o644); err != nil {
			return fmt.Errorf("could not write calibration_targets.json: %w", err)
		}
		if err := os.WriteFile(filepath.Join(outDir, "calibration.md"), []byte(md), 0o644); err != nil {
			return fmt.Errorf("could not write calibration.md: %w", err)
		}
	}
	return a.emit(struct {
		Targets journey.Targets           `json:"targets"`
		Status  journey.CalibrationStatus `json:"status"`
	}{t, st}, md)
}

// writeJourneyFiles writes the full result and the Markdown tables. Neither file
// holds a path, a key or a timestamp, so both are safe to share.
func writeJourneyFiles(dir string, res journey.Result, sens *journey.Result) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("could not create %s: %w", dir, err)
	}
	var payload any = res
	md := journey.Markdown(res)
	if sens != nil {
		payload = journeysOutput{Main: res, Sensitivity: *sens}
		md += "\n" + journey.SensitivityMarkdown(res, *sens)
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "journeys.json"), append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("could not write journeys.json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "journeys.md"), []byte(md), 0o644); err != nil {
		return fmt.Errorf("could not write journeys.md: %w", err)
	}
	return nil
}
