package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/analyze"
	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/store"
)

type verbosityOutput struct {
	Current config.VerbositySetting `json:"current"`
	Learned analyze.VerbosityChoice `json:"learned"`
	// SteerState is verbosity_steer's configured state; the level only matters while it is shadow or on.
	SteerState string `json:"verbosity_steer"`
	DryRun     bool   `json:"dry_run"`
	Applied    bool   `json:"applied"`
	Message    string `json:"message"`
}

func newOptimizeVerbosity(a *App) *cobra.Command {
	var days int
	var apply, force bool
	c := &cobra.Command{
		Use:   "verbosity",
		Short: "Learn how short verbosity_steer should ask replies to be, from your past sessions",
		Long: "Looks at recorded sessions for long replies you cut short: a reply whose stream was stopped, or\n" +
			"one answered by a new message within seconds of it ending. If that happens often, it picks \"terse\";\n" +
			"otherwise \"concise\". It shows the level and its evidence first and changes nothing (a dry run);\n" +
			"--apply writes optimize.verbosity, which applies to new sessions. A level you set by hand is only\n" +
			"replaced with --force. All counts are estimates from metadata; no prompt or reply text is read.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if days < 1 || days > 3650 {
				return fmt.Errorf("--days is %d; use a number from 1 to 3650", days)
			}
			return optimizeVerbosity(a, days, apply, force)
		},
	}
	c.Flags().IntVar(&days, "days", 30, "how many days of history to learn from")
	c.Flags().BoolVar(&apply, "apply", false, "write the learned level to config.toml (default: dry run)")
	c.Flags().BoolVar(&force, "force", false, "with --apply, replace a level you set by hand")
	return c
}

func optimizeVerbosity(a *App, days int, apply, force bool) error {
	dir, err := a.ResolveDataDir()
	if err != nil {
		return err
	}
	cur, err := config.ReadVerbositySetting(dir)
	if err != nil {
		return err
	}
	out := verbosityOutput{Current: cur, DryRun: !apply, SteerState: "off"}
	if cfg, err := config.Load(dir, a.Getenv); err == nil {
		out.SteerState = cfg.OptimizerState(optimize.OptimizerVerbositySteer)
	}
	to := time.Now()
	from := to.AddDate(0, 0, -days)
	th := analyze.DefaultVerbosityThresholds()
	out.Learned = analyze.VerbosityChoice{Thresholds: th, Evidence: analyze.VerbosityEvidence{Label: "estimated", From: from.UTC(), To: to.UTC()},
		Reason: "not enough evidence: no usage recorded yet. Run your agents through `trove gateway` first"}
	if _, err := os.Stat(filepath.Join(dir, store.DBFileName)); err == nil {
		db, err := store.Open(dir)
		if err != nil {
			return fmt.Errorf("cannot open Trove's database: %v. Run `trove doctor` for details", err)
		}
		defer func() { _ = db.Close() }()
		out.Learned, err = analyze.LearnVerbosity(context.Background(), db, from, to.Add(time.Minute), th)
		if err != nil {
			return fmt.Errorf("cannot read recorded usage: %v. Run `trove doctor` for details", err)
		}
	}

	lvl := out.Learned.Level
	switch {
	case lvl == "":
		out.Message = "Nothing changed: there is not enough evidence yet."
	case lvl == cur.Value:
		out.Message = fmt.Sprintf("Nothing to change: optimize.verbosity is already %q.", lvl)
	case !apply:
		out.Message = fmt.Sprintf("Dry run: nothing changed. Run `trove optimize verbosity --apply` to set optimize.verbosity to %q.", lvl)
	case cur.HandSet && !force:
		return fmt.Errorf("optimize.verbosity is %q, which you set yourself; Trove does not replace it with the learned %q. "+
			"Run `trove optimize verbosity --apply --force` to replace it, or keep yours", cur.Value, lvl)
	default:
		if err := config.SetVerbosity(dir, lvl); err != nil {
			return err
		}
		out.Applied = true
		out.Message = fmt.Sprintf("Set optimize.verbosity to %q. It applies to new sessions; running sessions keep their level.", lvl)
	}
	if out.SteerState == "off" && lvl != "" {
		out.Message += " verbosity_steer is off, so the level has no effect until you set it to \"shadow\" or \"on\" under [optimizers]."
	}
	return a.emit(out, renderVerbosity(out))
}

func renderVerbosity(o verbosityOutput) string {
	var b strings.Builder
	e, th := o.Learned.Evidence, o.Learned.Thresholds
	learned := o.Learned.Level
	if learned == "" {
		learned = "none (not enough evidence)"
	}
	if o.DryRun {
		b.WriteString("Dry run: nothing is changed without --apply.\n\n")
	}
	fmt.Fprintf(&b, "Current level: %s", o.Current.Value)
	switch {
	case o.Current.HandSet:
		b.WriteString(" (set by you)")
	case o.Current.Learned:
		b.WriteString(" (learned)")
	}
	fmt.Fprintf(&b, "\nLearned level: %s\n\n", learned)
	fmt.Fprintf(&b, "Evidence (estimated, from metadata only; %s to %s):\n", e.From.Format(time.DateOnly), e.To.Format(time.DateOnly))
	fmt.Fprintf(&b, "  %d sessions, %d long replies (%d+ output tokens, or cut off)\n", e.Sessions, e.Replies, th.LongReplyTokens)
	fmt.Fprintf(&b, "  %d interrupted, %d followed by a user message within %ds\n", e.Interrupted, e.FastFollowUps, th.FastFollowUpMS/1000)
	fmt.Fprintf(&b, "  %.0f%% cut short; terse from %.0f%%, with at least %d sessions and %d replies\n\n",
		100*e.Rate, 100*th.TerseRate, th.MinSessions, th.MinReplies)
	fmt.Fprintf(&b, "Why: %s.\n%s", o.Learned.Reason, o.Message)
	return b.String()
}
