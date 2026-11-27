package cli

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/autopilot"
	"github.com/usetrove/trove/internal/config"
)

// optimizeItems says, in plain English, what each item of the optimize policy does. It is the text the user agrees
// to, so every name the policy names must have a line here (TestOptimizeConsentNamesEveryItem).
var optimizeItems = map[string]string{
	config.OptimizerHookColdResume:   "Cold-resume guard (ask mode): when you resume a large session whose cache has expired, it stops the first prompt once and says what continuing will cost; send it again to go ahead. It only warns until Trove has checked that a stop reaches you",
	config.OptimizerHookModelSwitch:  "Model-switch guard: adds what a switch will cost on a large, warm context to Claude Code's own confirmation",
	config.OptimizerHookColdCompact:  "Cold-compact guard: stops a manual /compact once when the cache has expired and the history is large; run it again to go ahead",
	"noise_normalizer":               "Remove colour codes, progress frames and repeated blank lines from tool output (lossless)",
	"duplicate_result_collapser":     "Replace a repeated identical tool result with a pointer (lossless, gateway only)",
	"effort_routine_prompts":         "Low effort for the turn after a routine prompt (API key through the gateway only)",
	"thinking_clear":                 "Clear old thinking in very long sessions (API key through the gateway only)",
	config.OptimizerHookRoutineSteer: "Ask for a direct answer on routine prompts",
	"task_routine_agents":            "Routine subagents for tests and lint on a cheaper model",
	"task_explore":                   "The Explore subagent on a cheaper model",
	"ext_ponytail":                   "Ponytail rules, which ask the agent to write less code (needs `trove external install ponytail`)",
	config.OptimizerHookWriteToEdit:  "Write-to-Edit nudge",
	config.OptimizerHookEditBatch:    "Edit-batch nudge",
}

// optimizeConsent is what `trove setup` and `trove autopilot on` ask, in plain words. The lists come from the policy,
// so the words cannot drift from what the code does.
func optimizeConsent(p autopilot.Policy) string {
	line := func(n string) string { return "    - " + optimizeItems[n] + "\n" }
	var direct, first, gated, shadow strings.Builder
	for _, n := range p.Direct {
		direct.WriteString(line(n))
	}
	for _, n := range append(append([]string{}, p.Eligible...), p.Probation...) {
		if autopilot.VerdictBlocker(n) != "" {
			gated.WriteString(line(n))
		} else {
			first.WriteString(line(n))
		}
	}
	first.WriteString("    - The subagent cache lifetime: one Claude Code setting (subagentPromptCacheTtl in ~/.claude/settings.json),\n" +
		"      set to one hour only when your history shows it pays. It is backed up and `trove undo` reverses it\n")
	for _, n := range p.Held {
		gated.WriteString(line(n))
	}
	for _, n := range p.ShadowOnly {
		shadow.WriteString(line(n))
	}
	return fmt.Sprintf(optimizeConsentFmt, direct.String(), first.String(), gated.String(), strings.TrimSuffix(shadow.String(), "\n"))
}

const optimizeConsentFmt = `Turn on the optimize policy? Your one yes covers the list below and nothing else.
Every item is measured, and is rolled back on its own if it hurts.

On from your next session. These are guards: they never change what is sent to the model.
%s
Watched in shadow first (estimates only, nothing changed), and turned on only when your own sessions
show it pays:
%s
Also watched in shadow, but each changes what the agent is told or which model runs, so it also needs
a benchmark verdict before it is tried on half of your sessions. None has a verdict yet:
%s
Shadow only. Trove estimates these and never turns them on:
%s

  - How it is judged: the share of your plan limit each session uses (subscription; tokens per session
    until Trove has fitted your plan-limit meter), or its cost at API prices (API key), plus guardrails
    for failing tests, extra turns and tool errors.
  - Changes reach only sessions that start afterwards, never one in progress.
  - Anything you set yourself in config.toml is left exactly as you set it.
  - It never touches your login, and nothing leaves your machine.
  - The thresholds are placeholders, not yet validated by a benchmark.

Undo it any time with ` + "`trove autopilot off`" + ` (or ` + "`trove undo`" + ` for everything).`

// trialCandidates says, in plain English, what each trial candidate changes. It is the text the user agrees to,
// so every name in a policy's Probation list must have a line here (TestTrialsConsentNamesEveryCandidate).
var trialCandidates = map[string]string{
	"hook_repeat_read":    "refusing a repeat read of a file that has not changed (the agent can read it again)",
	"hook_quiet_commands": "adding a quiet flag to a few noisy commands such as `npm install` (the agent can run it again without the flag)",
	"output_trimmer":      "shortening very long test and build output to its failures and the first and last lines, with the full text kept on your machine for the agent to fetch back (needs the Trove MCP server, which adds about 350 tokens of tool definitions to every session)",
	"hook_large_output":   "suggesting the Trove MCP tool trove_run for known-noisy commands (a hint of about 55 tokens each time; needs the Trove MCP server)",
	"hook_read_limit":     "limiting the first read of a very large file to its first 400 lines, with a note telling the agent how to read the rest",
}

// trialsConsent is what the user agrees to for trials under the given policy. It is a second, separate yes: the
// first agreement covers only lossless changes, and a trial changes what the model sees for half of the sessions.
// The numbers and the list come from the policy, so the words cannot drift from what the code does.
func trialsConsent(p autopilot.Policy) string {
	var list strings.Builder
	for _, n := range p.Probation {
		list.WriteString("    - " + trialCandidates[n] + "\n")
	}
	return fmt.Sprintf(trialsConsentFmt, len(p.Probation), list.String(), p.ProbationMinSessionsPerArm, p.ProbationMaxDays)
}

const trialsConsentFmt = `Trials let Autopilot try a few changes that are not lossless: they hide or shorten
something the model could otherwise see, which the agent can get back, at a cost, if it needs it.
Today that is %d:
%s

  - Nothing starts on a hunch. Each is first watched in shadow mode, as everything else is.
  - Then one at a time it runs for about half of your new sessions, chosen at random, and is off for the
    rest, so Trove can compare real sessions with and without it.
  - It is kept only if the comparison is clean: fewer tokens, and no more turns or tool errors.
    Otherwise it goes back to shadow and Trove tells you why.
  - It needs at least %d sessions on each side, so a trial takes weeks, not days. If there is no clear
    difference after %d days it is dropped. These numbers are placeholders, not yet validated.
  - Each also has its own guardrail that puts it back to shadow by itself if the agent keeps undoing it.
  - Sessions already running are never changed, and a session never switches sides.
  - For this Trove reads your local Claude Code history (counts only, never text) when a session ends.
  - It does not run if you have turned off A/B comparison ([experiment] enabled = false).
  - Anything you set yourself in config.toml is left exactly as you set it.

Withdraw it any time with ` + "`trove autopilot trials off`" + `. That also removes anything a trial kept.`

// settingsConsent is what the user agrees to for the settings policy (settings-v1). It is a separate yes because
// it edits a file outside ~/.trove: the agent's own settings.
const settingsConsent = `The settings policy lets Autopilot change one setting in your Claude Code user settings
(~/.claude/settings.json): subagentPromptCacheTtl, the prompt cache lifetime of subagents, compaction and
other requests outside your main conversation (Claude Code 2.1.242 or later).

  - It sets it to "1h" only when your own history shows the one-hour lifetime pays: enough subagent
    rebuilds after pauses of 5 to 60 minutes to outweigh the higher one-hour write price (2x the input
    price, against 1.25x for five minutes). The figure is estimated from your plan-limit meter when Trove
    has fitted it, otherwise from API list prices, and says which.
  - It never changes a value you set: not the setting in any settings file, not the
    CLAUDE_CODE_SUBAGENT_PROMPT_CACHE_TTL, FORCE_PROMPT_CACHING_5M or ENABLE_PROMPT_CACHING_1H variables,
    and not a cacheTtl in one of your subagents.
  - It writes only when a session ends and no other agent session is open, so no running session changes.
  - After 14 days it checks the last 14 days of history again, and keeps checking. If the one-hour
    lifetime no longer pays, it removes what it set. If you undo it yourself, it never sets it again.
  - Every change is backed up; ` + "`trove undo <id>`" + ` reverses it.
  - For this Trove reads your local Claude Code history (counts only, never text) when a session ends.
    The thresholds are placeholders, not yet validated by a benchmark.

Withdraw it any time with ` + "`trove autopilot settings off`" + `.`

type autopilotSettingJSON struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
	FixID  string `json:"fix_id,omitempty"`

	Estimate *config.AutopilotSettingEstimate `json:"estimate,omitempty"`
}

type autopilotOptimizerJSON struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

type autopilotStatusJSON struct {
	Enabled bool   `json:"enabled"`
	Paused  bool   `json:"paused"`
	Policy  string `json:"policy,omitempty"`
	// Trials is true when the user has agreed to trials (policy lossless-v2 or lossless-v3).
	Trials     bool                     `json:"trials"`
	Optimizers []autopilotOptimizerJSON `json:"optimizers,omitempty"`
	// SettingsPolicy is "settings-v1" when the user agreed to it; Settings are the settings it judges.
	SettingsPolicy string                 `json:"settings_policy,omitempty"`
	Settings       []autopilotSettingJSON `json:"settings,omitempty"`
	// Thresholds are placeholders until a benchmark sets them.
	Thresholds string `json:"thresholds,omitempty"`
	// TrialRule says what a trial needs, when trials are on.
	TrialRule string `json:"trial_rule,omitempty"`
	Changed   string `json:"changed,omitempty"`
	// Summary is the last week: changes, rollbacks and the shadow planner's advice.
	Summary *autopilot.Summary `json:"summary,omitempty"`
}

func newAutopilot(a *App) *cobra.Command {
	var yes, stop bool
	c := &cobra.Command{
		Use:       "autopilot [on|off|pause|resume|trials [on|off]|settings [on|off]|trial <rule set>]",
		Short:     "Let Trove turn on proven savings by itself (with no argument: show what it is doing)",
		Args:      cobra.MaximumNArgs(2),
		ValidArgs: []string{"on", "off", "pause", "resume", "trials", "settings", "trial"},
		RunE: func(_ *cobra.Command, args []string) error {
			verb := ""
			if len(args) >= 1 {
				verb = args[0]
			}
			if verb == "trial" {
				if len(args) != 2 {
					return fmt.Errorf("name the rule set to trial: `trove autopilot trial <%s>`", strings.Join(autopilot.RuleSets(), "|"))
				}
				return runRuleTrial(a, args[1], stop, yes)
			}
			if stop {
				return fmt.Errorf("--stop only goes with `trove autopilot trial <rule set>`")
			}
			if verb == "trials" || verb == "settings" {
				sub := "on"
				if len(args) == 2 {
					sub = args[1]
				}
				if sub != "on" && sub != "off" {
					return fmt.Errorf("unknown action %q. Use `trove autopilot %s on` or `trove autopilot %s off`.", sub, verb, verb)
				}
				verb = verb + " " + sub
			} else if len(args) == 2 {
				return fmt.Errorf("unknown action %q. Use on, off, pause, resume or trials, or no argument to see the status.", strings.Join(args, " "))
			}
			return runAutopilot(a, verb, yes)
		},
	}
	c.Flags().BoolVar(&yes, "yes", false, "agree without asking (for `on`, `trials on`, `settings on` and `trial`)")
	c.Flags().BoolVar(&stop, "stop", false, "stop a rule-set trial (with `trial <rule set>`)")
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
		if cur.Active() && cur.IsOptimize() {
			changed = "Autopilot is already on, with the optimize policy."
			break
		}
		// A user on a lossless policy agreed to less. The optimize policy is a new yes, never a quiet upgrade.
		if !yes {
			if a.JSON {
				return fmt.Errorf("The optimize policy needs your agreement. Run `trove autopilot on --yes` to agree without a prompt.")
			}
			if cur.Active() {
				_, _ = fmt.Fprintf(a.Out, "Autopilot is on with the %s policy. The optimize policy covers more, listed below, and needs a new yes.\n\n", cur.Policy)
			}
			_, _ = fmt.Fprint(a.Out, optimizeConsent(autopilot.PolicyOptimize())+"\n\n")
			if !a.confirm("Turn on the optimize policy?") {
				return a.emit(autopilotStatusJSON{Changed: "Optimize policy not turned on."}, "Optimize policy not turned on. Nothing was changed.")
			}
		}
		if err := enableOptimize(a, dir); err != nil {
			return err
		}
		changed = optimizeOnText
	case "trials on":
		cur, _ := config.LoadAutopilot(dir)
		if !cur.Enabled {
			return fmt.Errorf("Autopilot is not on. Run `trove autopilot on` first, then `trove autopilot trials on`.")
		}
		tp := autopilot.TrialPolicy()
		if cur.Policy == tp.Name || (cur.IsOptimize() && cur.HasTrials()) {
			changed = "Trials are already on."
			break
		}
		// A user on an older trial policy agreed to fewer changes. Widening it is a new yes, never a quiet upgrade.
		upgrade := cur.HasTrials()
		if !yes {
			if a.JSON {
				return fmt.Errorf("Trials need your agreement. Run `trove autopilot trials on --yes` to agree without a prompt.")
			}
			if upgrade {
				had := len(autopilot.PolicyFor(cur.Policy).Probation)
				_, _ = fmt.Fprintf(a.Out, "You agreed to trials of %d changes before. This adds %d more, listed below, and needs a new yes.\n\n", had, len(tp.Probation)-had)
			}
			_, _ = fmt.Fprint(a.Out, trialsConsent(tp)+"\n\n")
			if !a.confirm("Turn on trials?") {
				return a.emit(autopilotStatusJSON{Changed: "Trials not turned on."}, "Trials not turned on. Nothing was changed.")
			}
		}
		if _, err := autopilot.EnableTrials(dir, tp, time.Now()); err != nil {
			return fmt.Errorf("cannot turn on trials: %v. Check that %s is writable.", err, dir)
		}
		changed = "Trials are on. The candidates start in shadow mode and change nothing yet."
		if upgrade {
			changed = "Trials now include the new candidates. They start in shadow mode and change nothing yet; the ones you already had keep their state."
		}
	case "trials off":
		cur, err := config.LoadAutopilot(dir)
		if err != nil {
			return err
		}
		if !cur.HasTrials() {
			changed = "Trials are already off."
			break
		}
		if _, err := autopilot.DisableTrials(dir, time.Now()); err != nil {
			return fmt.Errorf("cannot turn off trials: %v. Check that %s is writable.", err, dir)
		}
		changed = "Trials are off. New sessions are no longer split; sessions already running keep their settings. Anything a trial had kept is off again."
	case "settings on":
		cur, _ := config.LoadAutopilot(dir)
		if !cur.Enabled {
			return fmt.Errorf("Autopilot is not on. Run `trove autopilot on` first, then `trove autopilot settings on`.")
		}
		if cur.SettingsPolicy == config.AutopilotSettingsPolicy {
			changed = "The settings policy is already on."
			break
		}
		if !yes {
			if a.JSON {
				return fmt.Errorf("The settings policy needs your agreement. Run `trove autopilot settings on --yes` to agree without a prompt.")
			}
			_, _ = fmt.Fprint(a.Out, settingsConsent+"\n\n")
			if !a.confirm("Let Autopilot change this setting?") {
				return a.emit(autopilotStatusJSON{Changed: "Settings policy not turned on."}, "Settings policy not turned on. Nothing was changed.")
			}
		}
		if _, err := autopilot.EnableSettings(dir, time.Now()); err != nil {
			return fmt.Errorf("cannot turn on the settings policy: %v. Check that %s is writable.", err, dir)
		}
		changed = "The settings policy is on. Nothing is changed yet: the setting is judged when a session ends."
	case "settings off":
		id, err := autopilot.DisableSettings(dir)
		if err != nil {
			return fmt.Errorf("cannot turn off the settings policy: %v. Check that %s is writable.", err, dir)
		}
		changed = "The settings policy is off. Autopilot will not change your Claude Code settings."
		if id != "" {
			changed += " What it set is removed when a session ends with no other session open, or now with `trove undo " + id + "`."
		}
	case "off":
		if err := autopilot.Disable(dir); err != nil {
			return fmt.Errorf("cannot turn off Autopilot: %v. Check that %s is writable.", err, dir)
		}
		changed = "Autopilot is off. New sessions use only what config.toml says; sessions already running keep their settings."
		if id := autopilot.SettingChangeLeft(dir); id != "" {
			changed += " The Claude Code setting it changed is set back when a session ends with no other session open, or now with `trove undo " + id + "`."
		}
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
	return runAutopilotStatus(a, dir, changed)
}

// runAutopilotStatus prints Autopilot's state, with changed as the line saying what this command did.
func runAutopilotStatus(a *App, dir, changed string) error {
	f, err := config.LoadAutopilot(dir)
	if err != nil {
		return err
	}
	p := autopilot.PolicyOf(f)
	st := autopilotStatusJSON{Enabled: f.Enabled, Paused: f.Paused, Policy: f.Policy, Trials: f.HasTrials(), Changed: changed}
	st.Summary = autopilot.Summarize(f, time.Now(), 7)
	for _, n := range f.Names() {
		e := f.Entries[n]
		st.Optimizers = append(st.Optimizers, autopilotOptimizerJSON{Name: n, State: e.State, Reason: e.Reason})
	}
	st.SettingsPolicy = f.SettingsPolicy
	names := make([]string, 0, len(f.Settings))
	for n := range f.Settings {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		e := f.Settings[n]
		st.Settings = append(st.Settings, autopilotSettingJSON{Name: n, State: e.State, Reason: e.Reason, FixID: e.FixID, Estimate: e.Estimate})
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
	if s.SettingsPolicy != "" || len(s.Settings) > 0 {
		b.WriteString("\nSettings policy: ")
		if s.SettingsPolicy != "" {
			b.WriteString("on (" + s.SettingsPolicy + ")\n")
		} else {
			b.WriteString("off. See it with `trove autopilot settings on`.\n")
		}
		for _, o := range s.Settings {
			line := fmt.Sprintf("  %-28s %s", o.Name, o.State)
			if o.Reason != "" {
				line += "  (" + o.Reason + ")"
			}
			b.WriteString(line + "\n")
		}
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
	if cur.Active() && cur.IsOptimize() {
		return "The optimize policy is on (see `trove status`).\n\n", nil
	}
	if cur.Active() && !flag {
		return "Autopilot is on with the " + cur.Policy + " policy. `trove autopilot on` offers the optimize policy, which covers more.\n\n", nil
	}
	if !a.JSON && !flag {
		_, _ = fmt.Fprint(a.Out, optimizeConsent(autopilot.PolicyOptimize())+"\n\n")
	}
	consent := flag
	if !consent && !a.JSON {
		consent = a.confirm("Turn on the optimize policy?")
	}
	if !consent {
		return "Optimize policy not turned on. Turn it on any time with `trove autopilot on`.\n\n", nil
	}
	if err := enableOptimize(a, dir); err != nil {
		return "", err
	}
	return optimizeOnText + "\n\n", nil
}

// optimizeOnText is what the user sees once they agreed.
const optimizeOnText = "The optimize policy is on. The three guards start with your next session; everything else starts in shadow " +
	"and changes nothing until your own sessions show it pays. See what it does with `trove status`."

// enableOptimize records the agreement to the optimize policy and rewrites the snapshot the hooks read, so the next
// session that starts has the guards. Sessions already running keep what they started with (rule 5).
func enableOptimize(a *App, dir string) error {
	if err := ensureConfigFile(dir); err != nil {
		return err
	}
	if _, err := autopilot.Enable(dir, autopilot.PolicyOptimize(), time.Now()); err != nil {
		return fmt.Errorf("cannot turn on the optimize policy: %v. Check that %s is writable.", err, dir)
	}
	// Best effort: without it the hooks pick the guards up after the next session ends.
	if cfg, err := config.Load(dir, a.Getenv); err == nil {
		_ = syncHooks(a, dir, cfg)
	}
	return nil
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
