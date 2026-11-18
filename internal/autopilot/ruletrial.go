package autopilot

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/experiment"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/store"
	"github.com/usetrove/trove/internal/technique"
)

// Rule-set trials (plan 6.6). A rule set that asks for shorter replies (terse-rules, Ponytail, Caveman) is T2: it
// changes what the model is asked to do, so whether it pays off is judged per user, by a session-level A/B. The
// hooks add the rules to the context of about half of new sessions at SessionStart, chosen by a hash of the session
// key, and leave the rest alone. Autopilot reads the comparison at each session boundary and stops the trial when
// a guardrail trips or the result is in. It never turns the rule set on: T2 needs a benchmark verdict before
// Autopilot may do that, so a clean result is reported and the user decides.

// RuleTrialName is the autopilot.json entry of a rule set's trial: the tool's optimizer name.
func RuleTrialName(tool string) string { return "ext_" + tool }

// RuleTrialTool is the tool a rule-set trial entry is about.
func RuleTrialTool(name string) string { return strings.TrimPrefix(name, "ext_") }

// IsRuleSet reports whether the named tool is a wrapped third-party rule set in the behavior-rules slot.
func IsRuleSet(tool string) bool {
	t, ok := technique.Get(tool)
	if !ok || t.Route != technique.RouteWrap || t.Status != technique.StatusWrapped {
		return false
	}
	for _, c := range t.Claims {
		if c.Resource == technique.ResourceBehaviorRules {
			return true
		}
	}
	return false
}

// RuleSets lists the tools IsRuleSet accepts, sorted.
func RuleSets() []string {
	var out []string
	for _, t := range technique.All() {
		for _, n := range t.Names {
			if IsRuleSet(n) {
				out = append(out, n)
			}
		}
	}
	sort.Strings(out)
	return out
}

// StartRuleTrial records the user's explicit agreement to trial one rule set and starts it. blocker is the
// caller's reason the trial cannot start (the tool is not installed, it or another rule set is on, or
// verbosity_steer is on), or "". Nothing changes for sessions already running.
func StartRuleTrial(dir, tool, blocker string, now time.Time) (config.AutopilotFile, error) {
	f, err := config.LoadAutopilot(dir)
	if err != nil {
		return f, err
	}
	switch {
	case !f.Active():
		return f, fmt.Errorf("Autopilot is not on (or is paused). Run `trove autopilot on` first")
	case !IsRuleSet(tool):
		return f, fmt.Errorf("%s is not a rule set Trove can trial. Rule sets: %s", tool, strings.Join(RuleSets(), ", "))
	case blocker != "":
		return f, fmt.Errorf("%s", blocker)
	}
	name := RuleTrialName(tool)
	if other := otherInProbation(f.Entries, name); other != "" {
		return f, fmt.Errorf("%s is on trial already, and one trial runs at a time. Wait for it to finish, or stop it with `trove autopilot trial %s --stop`", other, RuleTrialTool(other))
	}
	cur := f.Entries[name]
	next := config.AutopilotEntry{State: config.AutopilotProbation, SinceMS: now.UnixMilli(), Attempts: cur.Attempts + 1,
		Reason: "trial running: the rules are added at the start of about half of new sessions, chosen at random, to compare with the rest"}
	f.Entries[name] = next
	f.Record(config.AutopilotEvent{AtMS: now.UnixMilli(), Optimizer: name, From: orOff(cur.State), To: next.State, Reason: "you started a trial"})
	return f, config.SaveAutopilot(dir, f)
}

// StopRuleTrial ends a rule set's trial at once: new sessions no longer get the rules. Sessions already running
// keep what they started with.
func StopRuleTrial(dir, tool string, now time.Time) (config.AutopilotFile, error) {
	f, err := config.LoadAutopilot(dir)
	if err != nil {
		return f, err
	}
	name := RuleTrialName(tool)
	cur, ok := f.Entries[name]
	if !ok {
		return f, fmt.Errorf("%s has no trial. Start one with `trove autopilot trial %s`", tool, tool)
	}
	delete(f.Entries, name)
	f.Record(config.AutopilotEvent{AtMS: now.UnixMilli(), Optimizer: name, From: orOff(cur.State), To: config.AutopilotOff, Reason: "you stopped the trial"})
	return f, config.SaveAutopilot(dir, f)
}

// DecideRuleTrial is the state machine for a running rule-set trial, a pure function of its inputs. The numbers
// are the trial policy's (TrialPolicy), whatever policy the user agreed to, because the user agreed to this trial
// itself. v is the comparison so far; its verdict, with the four figures, goes into the reason.
func DecideRuleTrial(cur config.AutopilotEntry, ev Evidence, v experiment.Result, now time.Time) Decision {
	p := TrialPolicy()
	next := cur
	set := func(state, reason string) {
		next.State, next.Reason, next.SinceMS = state, reason, now.UnixMilli()
	}
	if cur.State != config.AutopilotProbation {
		return Decision{Entry: cur}
	}
	verdict := strings.ReplaceAll(v.TrialVerdict(), "\n  ", "; ")
	testsTrip, testsWhy := measure.TestFailTrips(ev.Tests.On, ev.Tests.Off, p.TestFailMinSessions, p.testFailMargin(ev.Tests.Tier))
	switch {
	case ev.Blocked != "":
		set(config.AutopilotOff, "trial stopped: "+ev.Blocked)
	case testsTrip:
		set(config.AutopilotRolledBack, "trial stopped by the test_fail_rate guardrail: "+testsWhy+". Verdict: "+verdict)
	case ev.CanProbation != "":
		set(config.AutopilotOff, "the trial cannot continue: "+ev.CanProbation)
	case v.Status == experiment.CostsMore || v.Status == experiment.WorseOutcomes:
		set(config.AutopilotRolledBack, "trial stopped: sessions with the rules came out worse (measured). Verdict: "+verdict)
	case v.Status == experiment.Saves && v.NOn >= p.probationMin(ev.Tests.Tier) && v.NOff >= p.probationMin(ev.Tests.Tier):
		set(config.AutopilotOff, "trial finished: sessions with the rules came out better (measured). Autopilot does not turn rule sets on; turn it on yourself if you want it. Verdict: "+verdict)
	case now.Sub(time.UnixMilli(cur.SinceMS)) >= time.Duration(p.ProbationMaxDays)*24*time.Hour:
		set(config.AutopilotOff, fmt.Sprintf("trial finished after %d days with no clear benefit. Verdict: %s", p.ProbationMaxDays, verdict))
	default:
		next.Reason = fmt.Sprintf("trial running: %d sessions with the rules and %d without so far, each side needs %d. So far: %s",
			v.NOn, v.NOff, p.probationMin(ev.Tests.Tier), verdict)
	}
	return Decision{Entry: next}
}

// advanceRuleTrials evaluates every running rule-set trial once. on lists what is on for new sessions. It returns
// whether any entry changed, and the state changes made.
func advanceRuleTrials(ctx context.Context, db *store.DB, cfg config.Config, f *config.AutopilotFile, on []string, traffic measure.Traffic, now time.Time) (bool, []config.AutopilotEvent, error) {
	changed := false
	var events []config.AutopilotEvent
	idle := time.Duration(cfg.Gateway.IdleMinutes) * time.Minute
	for _, name := range f.RuleTrials() {
		cur := f.Entries[name]
		tool := RuleTrialTool(name)
		v, err := experiment.AnalyzeHookTrial(ctx, db, name, cfg.Experiment, cur.SinceMS, now, idle)
		if err != nil {
			return changed, events, fmt.Errorf("cannot read the trial of %s: %v", tool, err)
		}
		tOn, tOff, err := measure.TestFailByArm(ctx, db, name, cur.SinceMS, now.Add(-idle).UnixMilli())
		if err != nil {
			return changed, events, fmt.Errorf("cannot read the test results of the trial of %s: %v", tool, err)
		}
		ev := Evidence{
			Blocked:      ruleTrialBlocked(on, tool),
			CanProbation: probationBlocker(cfg, traffic, name),
			Tests:        TestEvidence{Tier: technique.T2, On: tOn, Off: tOff},
		}
		d := DecideRuleTrial(cur, ev, v, now)
		if d.Entry != cur {
			f.Entries[name] = d.Entry
			changed = true
		}
		if d.Entry.State != cur.State {
			e := config.AutopilotEvent{AtMS: now.UnixMilli(), Optimizer: name, From: cur.State, To: d.Entry.State, Reason: d.Entry.Reason}
			f.Record(e)
			events = append(events, e)
		}
	}
	return changed, events, nil
}

// ruleTrialBlocked says why a rule-set trial cannot go on beside what is on for new sessions (the rule set itself
// switched on in the agent's config, another rule set, or verbosity_steer), or "".
func ruleTrialBlocked(on []string, tool string) string {
	for _, o := range on {
		if o == tool {
			return tool + " is now switched on in your agent's config for every session, so there is nothing to compare it with"
		}
	}
	return blockedBy(on, tool)
}
