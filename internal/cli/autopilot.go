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

type autopilotOptimizerJSON struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

type autopilotStatusJSON struct {
	Enabled    bool                     `json:"enabled"`
	Paused     bool                     `json:"paused"`
	Policy     string                   `json:"policy,omitempty"`
	Optimizers []autopilotOptimizerJSON `json:"optimizers,omitempty"`
	// Thresholds are placeholders until a benchmark sets them.
	Thresholds string `json:"thresholds,omitempty"`
	Changed    string `json:"changed,omitempty"`
}

func newAutopilot(a *App) *cobra.Command {
	var yes bool
	c := &cobra.Command{
		Use:       "autopilot [on|off|pause|resume]",
		Short:     "Let Trove turn on proven savings by itself (with no argument: show what it is doing)",
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: []string{"on", "off", "pause", "resume"},
		RunE: func(_ *cobra.Command, args []string) error {
			verb := ""
			if len(args) == 1 {
				verb = args[0]
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
	p := autopilot.DefaultPolicy()
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
		if _, err := autopilot.Enable(dir, p, time.Now()); err != nil {
			return fmt.Errorf("cannot turn on Autopilot: %v. Check that %s is writable.", err, dir)
		}
		changed = "Autopilot is on. It starts in shadow mode and changes nothing yet."
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
		return fmt.Errorf("unknown action %q. Use on, off, pause or resume, or no argument to see the status.", verb)
	}

	f, err := config.LoadAutopilot(dir)
	if err != nil {
		return err
	}
	st := autopilotStatusJSON{Enabled: f.Enabled, Paused: f.Paused, Policy: f.Policy, Changed: changed}
	for _, n := range f.Names() {
		e := f.Entries[n]
		st.Optimizers = append(st.Optimizers, autopilotOptimizerJSON{Name: n, State: e.State, Reason: e.Reason})
	}
	if f.Enabled {
		st.Thresholds = fmt.Sprintf("at least %d shadow sessions and %d estimated net tokens in %d days (placeholder values, not yet validated by a benchmark)",
			p.MinSessions, p.MinNetTokens, p.LookbackDays)
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
		line := fmt.Sprintf("  %-28s %s", o.Name, o.State)
		if o.Reason != "" {
			line += "  (" + o.Reason + ")"
		}
		b.WriteString(line + "\n")
	}
	if s.Thresholds != "" {
		b.WriteString("\nIt turns an optimizer on after " + s.Thresholds + ". Savings shown are estimated until measured.\n")
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
