package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/autopilot"
	"github.com/usetrove/trove/internal/config"
)

// autopilotConsent is what the user agrees to. It is shown by `trove setup` and
// `trove autopilot on`, and says what Autopilot will not do.
const autopilotConsent = `Autopilot lets Trove watch your usage, test savings in the background without
changing anything, and turn on only the ones that prove safe for your sessions.

  - It starts in shadow mode: nothing is changed until there is enough evidence.
  - Only lossless optimizers that leave your cached prompt alone are eligible.
  - Changes reach only sessions that start afterwards, never one in progress.
  - Anything you set yourself in config.toml is left exactly as you set it.
  - A guardrail turns an optimizer back to shadow if it stops paying off.
  - It never touches your login or your traffic beyond what you already set up.
  - Nothing leaves your machine.

Undo it any time with ` + "`trove autopilot off`" + ` (or ` + "`trove undo`" + ` for everything).`

// trialsConsent is what the user agrees to for trials (policy lossless-v2). It is a second, separate yes: the
// first agreement covers only lossless changes, and a trial changes what the model sees for half of the sessions.
const trialsConsent = `Trials let Autopilot try a few changes that are not lossless: they hide or shorten
something the model could otherwise see, which the agent can get back, at a cost, if it needs it.
Today that is two: refusing a repeat read of a file that has not changed, and adding a quiet flag to
a few noisy commands such as ` + "`npm install`" + `.

  - Nothing starts on a hunch. Each is first watched in shadow mode, as everything else is.
  - Then one at a time it runs for about half of your new sessions, chosen at random, and is off for the
    rest, so Trove can compare real sessions with and without it.
  - It is kept only if the comparison is clean: fewer tokens, and no more turns or tool errors.
    Otherwise it goes back to shadow and Trove tells you why.
  - It needs at least 20 sessions on each side, so a trial takes weeks, not days. If there is no clear
    difference after 28 days it is dropped. These numbers are placeholders, not yet validated.
  - Sessions already running are never changed, and a session never switches sides.
  - For this Trove reads your local Claude Code history (counts only, never text) when a session ends.
  - It does not run if you have turned off A/B comparison ([experiment] enabled = false).
  - Anything you set yourself in config.toml is left exactly as you set it.

Withdraw it any time with ` + "`trove autopilot trials off`" + `. That also removes anything a trial kept.`

type autopilotOptimizerJSON struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

type autopilotStatusJSON struct {
	Enabled bool   `json:"enabled"`
	Paused  bool   `json:"paused"`
	Policy  string `json:"policy,omitempty"`
	// Trials is true when the user has agreed to trials (policy lossless-v2).
	Trials     bool                     `json:"trials"`
	Optimizers []autopilotOptimizerJSON `json:"optimizers,omitempty"`
	// Thresholds are placeholders until a benchmark sets them.
	Thresholds string `json:"thresholds,omitempty"`
	// TrialRule says what a trial needs, when trials are on.
	TrialRule string `json:"trial_rule,omitempty"`
	Changed   string `json:"changed,omitempty"`
	// Summary is the last week: changes, rollbacks and the shadow planner's advice.
	Summary *autopilot.Summary `json:"summary,omitempty"`
}

func newAutopilot(a *App) *cobra.Command {
	var yes bool
	c := &cobra.Command{
		Use:       "autopilot [on|off|pause|resume|trials [on|off]]",
		Short:     "Let Trove turn on proven savings by itself (with no argument: show what it is doing)",
		Args:      cobra.MaximumNArgs(2),
		ValidArgs: []string{"on", "off", "pause", "resume", "trials"},
		RunE: func(_ *cobra.Command, args []string) error {
			verb := ""
			if len(args) >= 1 {
				verb = args[0]
			}
			if verb == "trials" {
				sub := "on"
				if len(args) == 2 {
					sub = args[1]
				}
				if sub != "on" && sub != "off" {
					return fmt.Errorf("unknown action %q. Use `trove autopilot trials on` or `trove autopilot trials off`.", sub)
				}
				verb = "trials " + sub
			} else if len(args) == 2 {
				return fmt.Errorf("unknown action %q. Use on, off, pause, resume or trials, or no argument to see the status.", strings.Join(args, " "))
			}
			return runAutopilot(a, verb, yes)
		},
	}
	c.Flags().BoolVar(&yes, "yes", false, "agree to Autopilot without asking (for `on`)")
	return c
}

func runAutopilot(a *App, verb string, yes bool) error {
	dir, err := a.ResolveDataDir()
	if err != nil {
		return err
	}
	changed := ""
	switch verb {
	case "":
	case "on":
		cur, _ := config.LoadAutopilot(dir)
		if cur.Active() {
			changed = "Autopilot is already on."
			break
		}
		if !yes {
			if a.JSON {
				return fmt.Errorf("Autopilot needs your agreement. Run `trove autopilot on --yes` to agree without a prompt.")
			}
			_, _ = fmt.Fprint(a.Out, autopilotConsent+"\n\n")
			if !a.confirm("Turn on Autopilot?") {
				return a.emit(autopilotStatusJSON{Changed: "Autopilot not turned on."}, "Autopilot not turned on. Nothing was changed.")
			}
		}
		if err := ensureConfigFile(dir); err != nil {
			return err
		}
		if _, err := autopilot.Enable(dir, autopilot.DefaultPolicy(), time.Now()); err != nil {
			return fmt.Errorf("cannot turn on Autopilot: %v. Check that %s is writable.", err, dir)
		}
		changed = "Autopilot is on. It starts in shadow mode and changes nothing yet."
	case "trials on":
		cur, _ := config.LoadAutopilot(dir)
		if !cur.Enabled {
			return fmt.Errorf("Autopilot is not on. Run `trove autopilot on` first, then `trove autopilot trials on`.")
		}
		if cur.Policy == config.AutopilotPolicyV2 {
			changed = "Trials are already on."
			break
		}
		if !yes {
			if a.JSON {
				return fmt.Errorf("Trials need your agreement. Run `trove autopilot trials on --yes` to agree without a prompt.")
			}
			_, _ = fmt.Fprint(a.Out, trialsConsent+"\n\n")
			if !a.confirm("Turn on trials?") {
				return a.emit(autopilotStatusJSON{Changed: "Trials not turned on."}, "Trials not turned on. Nothing was changed.")
			}
		}
		if _, err := autopilot.EnableTrials(dir, time.Now()); err != nil {
			return fmt.Errorf("cannot turn on trials: %v. Check that %s is writable.", err, dir)
		}
		changed = "Trials are on. The candidates start in shadow mode and change nothing yet."
	case "trials off":
		cur, err := config.LoadAutopilot(dir)
		if err != nil {
			return err
		}
		if cur.Policy != config.AutopilotPolicyV2 {
			changed = "Trials are already off."
			break
		}
		if _, err := autopilot.DisableTrials(dir, time.Now()); err != nil {
			return fmt.Errorf("cannot turn off trials: %v. Check that %s is writable.", err, dir)
		}
		changed = "Trials are off. New sessions are no longer split; sessions already running keep their settings. Anything a trial had kept is off again."
	case "off":
		if err := autopilot.Disable(dir); err != nil {
			return fmt.Errorf("cannot turn off Autopilot: %v. Check that %s is writable.", err, dir)
		}
		changed = "Autopilot is off. New sessions use only what config.toml says; sessions already running keep their settings."
	case "pause", "resume":
		cur, err := config.LoadAutopilot(dir)
		if err != nil {
			return err
		}
		if !cur.Enabled {
			return fmt.Errorf("Autopilot is not on. Run `trove autopilot on` first.")
		}
		if _, err := autopilot.SetPaused(dir, verb == "pause"); err != nil {
			return fmt.Errorf("cannot %s Autopilot: %v. Check that %s is writable.", verb, err, dir)
		}
		changed = map[string]string{"pause": "Autopilot is paused. New sessions use only what config.toml says.", "resume": "Autopilot is running again."}[verb]
	default:
		return fmt.Errorf("unknown action %q. Use on, off, pause, resume or trials, or no argument to see the status.", verb)
	}

	f, err := config.LoadAutopilot(dir)
	if err != nil {
		return err
	}
	p := autopilot.PolicyFor(f.Policy)
	st := autopilotStatusJSON{Enabled: f.Enabled, Paused: f.Paused, Policy: f.Policy, Trials: f.Policy == config.AutopilotPolicyV2, Changed: changed}
	st.Summary = autopilot.Summarize(f, time.Now(), 7)
	for _, n := range f.Names() {
		e := f.Entries[n]
		st.Optimizers = append(st.Optimizers, autopilotOptimizerJSON{Name: n, State: e.State, Reason: e.Reason})
	}
	if f.Enabled {
		st.Thresholds = fmt.Sprintf("at least %d shadow sessions and %d estimated net tokens in %d days (placeholder values, not yet validated by a benchmark)",
			p.MinSessions, p.MinNetTokens, p.LookbackDays)
		if st.Trials {
			st.TrialRule = fmt.Sprintf("at least %d sessions on each side and no more than %d days (placeholder values, not yet validated by a benchmark)",
				p.ProbationMinSessionsPerArm, p.ProbationMaxDays)
		}
	}
	return a.emit(st, renderAutopilot(st))
}

func renderAutopilot(s autopilotStatusJSON) string {
	var b strings.Builder
	if s.Changed != "" {
		b.WriteString(s.Changed + "\n\n")
	}
	switch {
	case !s.Enabled:
		b.WriteString("Autopilot is off. Turn it on with `trove autopilot on`.")
		return b.String()
	case s.Paused:
		b.WriteString("Autopilot is paused.\n")
	default:
		b.WriteString("Autopilot is on.\n")
	}
	for _, o := range s.Optimizers {
		state := o.State
		if state == config.AutopilotProbation {
			state = "on trial"
		}
		line := fmt.Sprintf("  %-28s %s", o.Name, state)
		if o.Reason != "" {
			line += "  (" + o.Reason + ")"
		}
		b.WriteString(line + "\n")
	}
	if s.Thresholds != "" {
		b.WriteString("\nIt turns an optimizer on after " + s.Thresholds + ". Savings shown are estimated until measured.\n")
	}
	switch {
	case s.Trials && s.TrialRule != "":
		b.WriteString("\nTrials are on: a change that is not lossless is tried on about half of new sessions, chosen at random, and kept only if the comparison is clean. A trial needs " + s.TrialRule + ". Withdraw with `trove autopilot trials off`.\n")
	case s.Trials:
		b.WriteString("\nTrials are on.\n")
	default:
		b.WriteString("\nTrials (changes that are not lossless, tried on half of your sessions to compare) are off. See them with `trove autopilot trials on`.\n")
	}
	if d := s.Summary.Details(); d != "" {
		b.WriteString("\n" + d + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// autopilotStep offers Autopilot at the end of `trove setup`. It applies only with
// its own consent: the --autopilot flag, or a yes at the prompt. --yes alone does
// not agree to it, as with the status line.
func autopilotStep(a *App, dir string, flag bool) (string, error) {
	cur, _ := config.LoadAutopilot(dir)
	if cur.Active() {
		return "Autopilot is on (see `trove autopilot`).\n\n", nil
	}
	if !a.JSON && !flag {
		_, _ = fmt.Fprint(a.Out, autopilotConsent+"\n\n")
	}
	consent := flag
	if !consent && !a.JSON {
		consent = a.confirm("Turn on Autopilot?")
	}
	if !consent {
		return "Autopilot not turned on. Turn it on any time with `trove autopilot on`.\n\n", nil
	}
	if err := ensureConfigFile(dir); err != nil {
		return "", err
	}
	if _, err := autopilot.Enable(dir, autopilot.DefaultPolicy(), time.Now()); err != nil {
		return "", fmt.Errorf("cannot turn on Autopilot: %v. Check that %s is writable.", err, dir)
	}
	return "Autopilot is on. It starts in shadow mode and changes nothing yet.\n\n", nil
}

// autopilotStepFor runs autopilotStep and records the outcome in the setup report.
func autopilotStepFor(a *App, rep *setupReport, dir string, flag bool) (string, error) {
	text, err := autopilotStep(a, dir, flag)
	if err != nil {
		return "", err
	}
	if f, lerr := config.LoadAutopilot(dir); lerr == nil {
		rep.AutopilotOn = f.Active()
	}
	return text, nil
}

// autopilotSuffix is the short note the status line adds while Autopilot is on:
// "learning" until something is turned on, then how many optimizers it has on.
// It adds nothing when Autopilot is off, paused or unreadable, and it reads one
// small file, so the status line stays fast. It states no saving figure.
func autopilotSuffix(dir string) string {
	f, err := config.LoadAutopilot(dir)
	if err != nil || !f.Active() {
		return ""
	}
	on := 0
	for _, e := range f.Entries {
		if e.State == config.AutopilotOn {
			on++
		}
	}
	if on == 0 {
		return " · autopilot learning"
	}
	return fmt.Sprintf(" · autopilot %d on", on)
}

// autopilotSummary is the last week of Autopilot for `trove report`. Nil unless the
// user agreed to it, and nil on any read problem: the report never fails for this.
func autopilotSummary(a *App) *autopilot.Summary {
	dir, err := a.ResolveDataDir()
	if err != nil {
		return nil
	}
	f, err := config.LoadAutopilot(dir)
	if err != nil {
		return nil
	}
	return autopilot.Summarize(f, time.Now(), 7)
}
