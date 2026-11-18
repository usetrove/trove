package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/usetrove/trove/internal/autopilot"
	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/store"
)

// ruleTrialConsent is what the user agrees to before a rule-set trial starts (T2: explicit opt-in).
func ruleTrialConsent(tool string) string {
	return fmt.Sprintf(`A trial of %[1]s compares Claude Code sessions with its rules against sessions without them.

- At the start of about half of new sessions, chosen at random, the plugin's SessionStart hook adds %[1]s's
  rules to the session's context. The other half get nothing. Sessions already running are not changed.
- The rules cost input tokens on every request of the sessions that get them; that cost is in what is measured.
- Trove compares output tokens per turn, cost per turn, the share of sessions ending with failing tests, and
  turns. It stops the trial if tests fail more often with the rules, or if sessions come out worse.
- Autopilot never turns the rules on for every session. When the result is in, it reports the verdict in
  `+"`trove autopilot`"+` and you decide. Stop at any time with `+"`trove autopilot trial %[1]s --stop`"+`.`, tool)
}

// ruleTrialBlocker says why a trial of tool cannot start now, or "": it must be installed (so its license was
// shown), switched off (so half of sessions can run without it), and no other brevity rule set or verbosity_steer
// may be on.
func ruleTrialBlocker(ctx context.Context, db *store.DB, cfg config.Config, tool string) (string, error) {
	t, ok, err := db.ExternalTools().Get(ctx, tool)
	if err != nil {
		return "", err
	}
	if !ok {
		return fmt.Sprintf("%s is not installed. Run `trove external install %s` first (it shows the license); leave it off, the trial adds the rules itself", tool, tool), nil
	}
	if t.State == "on" {
		return fmt.Sprintf("%s is switched on for every session, so nothing could be compared. Run `trove external disable %s` first", tool, tool), nil
	}
	all, err := db.ExternalTools().List(ctx)
	if err != nil {
		return "", err
	}
	for _, o := range all {
		if o.State == "on" && autopilot.IsRuleSet(o.Name) {
			return fmt.Sprintf("%s is on, and only one output-style rule set may run at a time. Run `trove external disable %s` first", o.Name, o.Name), nil
		}
	}
	if s := cfg.OptimizerState(optimize.OptimizerVerbositySteer); s == config.StateOn {
		return "verbosity_steer is on, and only one output-style rule set may run at a time. Set it to \"off\" in your Trove config first", nil
	}
	return "", nil
}

func runRuleTrial(a *App, tool string, stop, yes bool) error {
	dir, cfg, err := loadConfig(a)
	if err != nil {
		return err
	}
	now := time.Now()
	changed := ""
	if stop {
		if _, err := autopilot.StopRuleTrial(dir, tool, now); err != nil {
			return err
		}
		changed = fmt.Sprintf("The trial of %s is stopped. New sessions no longer get its rules; sessions already running keep what they started with.", tool)
	} else {
		db, err := store.Open(dir)
		if err != nil {
			return fmt.Errorf("cannot open Trove's database: %v. Run `trove doctor` for details", err)
		}
		blocker, err := ruleTrialBlocker(context.Background(), db, cfg, tool)
		_ = db.Close()
		if err != nil {
			return fmt.Errorf("cannot read the installed tools: %v. Run `trove doctor` for details", err)
		}
		if !cfg.Experiment.Enabled {
			blocker = "A/B comparison is switched off ([experiment] enabled = false), so sessions cannot be split"
		}
		if blocker == "" && autopilot.IsRuleSet(tool) && !yes {
			if a.JSON {
				return fmt.Errorf("A trial needs your agreement. Run `trove autopilot trial %s --yes` to agree without a prompt.", tool)
			}
			_, _ = fmt.Fprint(a.Out, ruleTrialConsent(tool)+"\n\n")
			if !a.confirm("Start the trial?") {
				return a.emit(autopilotStatusJSON{Changed: "Trial not started."}, "Trial not started. Nothing was changed.")
			}
		}
		if _, err := autopilot.StartRuleTrial(dir, tool, blocker, now); err != nil {
			return err
		}
		changed = fmt.Sprintf("The trial of %s is running. About half of new Claude Code sessions get its rules at the start; `trove autopilot` shows the comparison as it builds.", tool)
	}
	return runAutopilotStatus(a, dir, changed)
}
