package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/usetrove/trove/internal/autopilot"
	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/hookcaps"
	"github.com/usetrove/trove/internal/hooks"
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
		if config.IsNativeRuleTrial(tool) {
			changed = fmt.Sprintf("The trial of %s is stopped. New sessions are no longer steered; sessions already running keep what they started with.", tool)
		}
	} else {
		db, err := store.Open(dir)
		if err != nil {
			return fmt.Errorf("cannot open Trove's database: %v. Run `trove doctor` for details", err)
		}
		native := config.IsNativeRuleTrial(tool)
		var blocker string
		if native {
			blocker, err = steerTrialBlocker(context.Background(), db, dir, cfg, tool)
		} else {
			blocker, err = ruleTrialBlocker(context.Background(), db, cfg, tool)
		}
		_ = db.Close()
		if err != nil {
			return fmt.Errorf("cannot read the installed tools: %v. Run `trove doctor` for details", err)
		}
		if !cfg.Experiment.Enabled {
			blocker = "A/B comparison is switched off ([experiment] enabled = false), so sessions cannot be split"
		}
		if blocker == "" && (autopilot.IsRuleSet(tool) || native) && !yes {
			if a.JSON {
				return fmt.Errorf("A trial needs your agreement. Run `trove autopilot trial %s --yes` to agree without a prompt.", tool)
			}
			consent := ruleTrialConsent(tool)
			if native {
				consent = steerTrialConsent()
			}
			_, _ = fmt.Fprint(a.Out, consent+"\n\n")
			if !a.confirm("Start the trial?") {
				return a.emit(autopilotStatusJSON{Changed: "Trial not started."}, "Trial not started. Nothing was changed.")
			}
		}
		if _, err := autopilot.StartRuleTrial(dir, tool, blocker, now); err != nil {
			return err
		}
		changed = fmt.Sprintf("The trial of %s is running. About half of new Claude Code sessions get its rules at the start; `trove autopilot` shows the comparison as it builds.", tool)
		if native {
			changed = fmt.Sprintf("The trial of %s is running. In about half of new Claude Code sessions on Opus 5.5 routine prompts are steered; `trove autopilot` shows the comparison as it builds.", tool)
		}
	}
	return runAutopilotStatus(a, dir, changed)
}

// steerShadowMin is how many routine prompts on an allowed model shadow mode must have counted before the steer's
// trial may start: shadow first, so the user sees there is something to compare.
const steerShadowMin = 20

// steerTrialConsent is what the user agrees to before the steer's trial starts (T2: explicit opt-in).
func steerTrialConsent() string {
	return `A trial of hook_routine_steer compares Claude Code sessions in which routine prompts are steered against
sessions in which they are only counted.

- A routine prompt is a short confirmation or instruction such as "yes", "continue", "run the tests" or "commit",
  and nothing else. Trove decides this on your machine, from the prompt text, which it never stores.
- In about half of new sessions on Claude Opus 5.5, chosen at random, such a prompt gets the added line
  "Answer directly without deliberating." Other models are never steered. Sessions already running are not changed.
- Each steered prompt costs about 30 tokens (estimated), and later requests carry the line as cached input; that
  cost is in what is measured.
- Trove compares thinking tokens per routine turn (measured, from your session logs) and shows the difference as a
  share of your plan limits (estimated). It stops the trial if tests fail more often, if sessions take more turns
  or tokens, or if the prompt after a steered turn is a correction ("no", "try again") more often.
- Autopilot never turns the steer on for every session. When the result is in, it reports the verdict in
  ` + "`trove autopilot`" + ` and you decide. Stop at any time with ` + "`trove autopilot trial hook_routine_steer --stop`" + `.`
}

// steerTrialBlocker says why the steer's trial cannot start now, or "": it must have run in shadow (so there is
// evidence it has routine prompts to act on), it must not be on for every session, and the live check that lets it
// send its text must have passed.
func steerTrialBlocker(ctx context.Context, db *store.DB, dir string, cfg config.Config, name string) (string, error) {
	switch cfg.OptimizerState(name) {
	case config.StateOn:
		return name + ` is on for every session, so nothing could be compared. Set it to "shadow" under [optimizers] first`, nil
	case config.StateShadow:
	default:
		return "run it in shadow first: set " + name + ` = "shadow" under [optimizers]. Shadow counts routine prompts and changes nothing`, nil
	}
	if !hooks.LoadCapabilities(dir).Supports("claude", hookcaps.ToolPrompt, hookcaps.PromptContext) {
		return "run `trove hooks verify --prompt-context` first: the steer sends nothing until that check sees the text land after the cached prefix", nil
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM hook_events WHERE hook = ? AND kind = 'would_steer'`, name).Scan(&n); err != nil {
		return "", err
	}
	if n < steerShadowMin {
		return fmt.Sprintf("shadow mode has counted %d routine prompts on Opus 5.5 so far; the trial needs at least %d, so there is something to compare. Run `trove hooks sync` if you have used it recently", n, steerShadowMin), nil
	}
	return "", nil
}
