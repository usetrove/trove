package autopilot

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/experiment"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/measure/limitfit"
	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/store"
	"github.com/usetrove/trove/internal/technique"
)

// Enable records the user's consent and starts every eligible optimizer in
// shadow. It changes no request: shadow forwards the original and only estimates.
// An optimizer the user set in config.toml keeps the user's value.
func Enable(dir string, p Policy, now time.Time) (config.AutopilotFile, error) {
	f, err := config.LoadAutopilot(dir)
	if err != nil {
		f = config.AutopilotFile{Entries: map[string]config.AutopilotEntry{}}
	}
	f.Enabled, f.Paused, f.Policy, f.ConsentedAt = true, false, config.AutopilotPolicy, now.UnixMilli()
	for _, name := range p.Eligible {
		if _, ok := f.Entries[name]; !ok {
			f.Entries[name] = config.AutopilotEntry{State: config.AutopilotShadow, SinceMS: now.UnixMilli(), Reason: "collecting shadow estimates"}
			f.Record(config.AutopilotEvent{AtMS: now.UnixMilli(), Optimizer: name, From: config.AutopilotOff, To: config.AutopilotShadow, Reason: "Autopilot turned on"})
		}
	}
	return f, config.SaveAutopilot(dir, f)
}

// CurrentPolicy is the policy the user consented to, read from autopilot.json. A missing, damaged or unknown
// record gives the v1 policy, which only ever shrinks what Autopilot may do.
func CurrentPolicy(dir string) Policy {
	f, err := config.LoadAutopilot(dir)
	if err != nil {
		return DefaultPolicy()
	}
	return PolicyFor(f.Policy)
}

// EnableTrials records the user's agreement to trials under policy p (lossless-v2 or v3) and lists p's trial
// candidates in shadow. A user moving from v2 to v3 keeps what v2 already has (its states and attempt counts) and
// gets the new candidates in shadow. It needs Autopilot to be on already, and changes no request: a candidate is only ever put on trial
// later, from shadow evidence, at a session boundary.
func EnableTrials(dir string, p Policy, now time.Time) (config.AutopilotFile, error) {
	if len(p.Probation) == 0 {
		return config.AutopilotFile{}, fmt.Errorf("policy %s has no trials", p.Name)
	}
	f, err := config.LoadAutopilot(dir)
	if err != nil {
		return f, err
	}
	if !f.Enabled {
		return f, fmt.Errorf("Autopilot is not on. Run `trove autopilot on` first")
	}
	f.Policy, f.TrialsConsentedAt = p.Name, now.UnixMilli()
	for _, name := range p.Probation {
		if _, ok := f.Entries[name]; !ok {
			f.Entries[name] = config.AutopilotEntry{State: config.AutopilotShadow, SinceMS: now.UnixMilli(), Reason: "collecting shadow estimates"}
			f.Record(config.AutopilotEvent{AtMS: now.UnixMilli(), Optimizer: name, From: config.AutopilotOff, To: config.AutopilotShadow, Reason: "trials turned on"})
		}
	}
	return f, config.SaveAutopilot(dir, f)
}

// DisableTrials withdraws the agreement to trials: back to the lossless-v1 policy, and every trial candidate of
// every policy version is forgotten, including one on trial. New sessions are not split any more; sessions already running keep
// their pins. What Autopilot turned on under v1 is untouched.
func DisableTrials(dir string, now time.Time) (config.AutopilotFile, error) {
	f, err := config.LoadAutopilot(dir)
	if err != nil {
		return f, err
	}
	f.Policy, f.TrialsConsentedAt = config.AutopilotPolicy, 0
	for _, name := range PolicyV3().Probation {
		if e, ok := f.Entries[name]; ok {
			if e.State == config.AutopilotProbation || e.State == config.AutopilotOn {
				f.Record(config.AutopilotEvent{AtMS: now.UnixMilli(), Optimizer: name, From: e.State, To: config.AutopilotOff, Reason: "trials turned off"})
			}
			delete(f.Entries, name)
		}
	}
	return f, config.SaveAutopilot(dir, f)
}

// Disable withdraws consent and forgets Autopilot's states and plan. New sessions
// go back to what config.toml says. Sessions already running keep their pins.
func Disable(dir string) error {
	off := config.AutopilotFile{Entries: map[string]config.AutopilotEntry{}}
	// A setting Autopilot changed in the agent's own files stays recorded, so it is taken back at the next
	// session end with no other session open (withdrawSubTTL), never forgotten in place.
	if cur, err := config.LoadAutopilot(dir); err == nil {
		for n, st := range cur.Settings {
			if st.State == config.AutopilotOn {
				if off.Settings == nil {
					off.Settings = map[string]config.AutopilotSetting{}
				}
				off.Settings[n] = st
			}
		}
	}
	return config.SaveAutopilot(dir, off)
}

// SettingChangeLeft returns the fix id of a settings change Autopilot made that is still recorded as on ("" if
// none), so `trove autopilot off` can say it is taken back at the next quiet session end.
func SettingChangeLeft(dir string) string {
	f, err := config.LoadAutopilot(dir)
	if err != nil {
		return ""
	}
	if st, ok := f.Settings[SubTTLName]; ok && st.State == config.AutopilotOn {
		return st.FixID
	}
	return ""
}

// SetPaused pauses or resumes Autopilot without losing its states.
func SetPaused(dir string, paused bool) (config.AutopilotFile, error) {
	f, err := config.LoadAutopilot(dir)
	if err != nil {
		return f, err
	}
	f.Paused = paused
	return f, config.SaveAutopilot(dir, f)
}

// Result is what one Advance did.
type Result struct {
	File config.AutopilotFile
	// Changed is true when autopilot.json was rewritten.
	Changed bool
	// Events are the state changes made in this call.
	Events []config.AutopilotEvent
}

// Advance evaluates every eligible optimizer once, refreshes the shadow planner's
// advice and saves any change. Call it at a session boundary (a session ending, or
// the gateway running its hourly check): the new states only reach sessions that
// start afterwards, because the engine and the hooks pin a session's states when
// it starts. A failure leaves the saved states as they were, so an error never
// turns anything on.
func Advance(ctx context.Context, db *store.DB, dir string, cfg config.Config, p Policy, now time.Time) (Result, error) {
	f, err := config.LoadAutopilot(dir)
	if err != nil {
		return Result{File: f}, err
	}
	if !f.Active() {
		return Result{File: f}, nil
	}
	base, rolled, err := measure.LoadStatus(ctx, db, cfg.Measure, now)
	if err != nil {
		return Result{File: f}, fmt.Errorf("cannot read the baseline status: %v", err)
	}
	w := measure.Window{From: now.AddDate(0, 0, -p.LookbackDays), To: now}
	rows, err := measure.ShadowEstimates(ctx, db, w, cfg)
	if err != nil {
		return Result{File: f}, fmt.Errorf("cannot read shadow estimates: %v", err)
	}
	byName := map[string]measure.ShadowRow{}
	for _, r := range rows {
		byName[r.Optimizer] = r
	}
	hookShadow, err := measure.HookShadowEstimates(ctx, db, w)
	if err != nil {
		return Result{File: f}, fmt.Errorf("cannot read the hooks' shadow estimates: %v", err)
	}
	hookSessions, err := measure.HookSessions(ctx, db)
	if err != nil {
		return Result{File: f}, fmt.Errorf("cannot count the hooks' sessions: %v", err)
	}
	traffic, err := measure.TrafficSeen(ctx, db, w)
	if err != nil {
		return Result{File: f}, fmt.Errorf("cannot tell which channels were used: %v", err)
	}
	setup := detectSetup(dir, cfg, traffic)
	// The baseline counts sessions from both channels, so a person with no gateway can leave it.
	seen := measure.PhaseFor(cfg.Measure, base.InstalledAt, now, sessionsSeen(base.SessionsSeen, hookSessions))
	baselineDone := seen.Phase == measure.PhaseActive
	on := onNow(ctx, db, cfg)
	// onForAll is what is on for every new session: a rule-set trial checks it, because the trial itself is
	// only on for half of them.
	onForAll := append([]string(nil), on...)
	for n, e := range f.Entries {
		if e.State == config.AutopilotProbation {
			on = append(on, RuleTrialTool(n)) // on for half of sessions, so it can still conflict with what else is on
		}
	}
	res := Result{File: f}
	changed := false
	for _, name := range p.Names() {
		if _, mine := cfg.Optimizers[name]; mine {
			// The user's own setting wins; Autopilot stays out of the way.
			if _, had := f.Entries[name]; had {
				delete(f.Entries, name)
				changed = true
			}
			continue
		}
		cur := f.Entries[name]
		_, back := rolled[name]
		sessions, events, tokens := combine(byName[name], hookShadow[name])
		ev := Evidence{
			BaselineDone:   baselineDone,
			ShadowSessions: sessions,
			NetTokens:      p.NetOf(name, sessions, events, tokens),
			RolledBack:     back,
			Blocked:        firstNonEmpty(blockedBy(on, name), p.CannotJudge(name)),

			NeedsProbation:   p.NeedsProbation(name),
			NoSavingEstimate: NoSavingEstimate(name),
			CanProbation:     probationBlocker(cfg, traffic, name),
			OtherInProbation: otherInProbation(f.Entries, name),
			Unavailable:      whyUnavailable(name, setup),
		}
		if cur.State == config.AutopilotProbation {
			pe, err := readTrial(ctx, db, cfg, name, cur, now)
			if err != nil {
				return Result{File: f}, fmt.Errorf("cannot read the trial of %s: %v", name, err)
			}
			ev.Probation = pe
			idle := time.Duration(cfg.Gateway.IdleMinutes) * time.Minute
			on, off, err := measure.TestFailByArm(ctx, db, name, cur.SinceMS, now.Add(-idle).UnixMilli())
			if err != nil {
				return Result{File: f}, fmt.Errorf("cannot read the test results of the trial of %s: %v", name, err)
			}
			ev.Tests = TestEvidence{On: on, Off: off}
			if t, ok := technique.Get(name); ok {
				ev.Tests.Tier = t.Tier
			}
		}
		d := p.Decide(cur, ev, now)
		if d.ClearRollback {
			if err := measure.ClearOverride(ctx, db, name, now); err != nil {
				return Result{File: f}, fmt.Errorf("cannot lift the rollback of %s: %v", name, err)
			}
		}
		if d.Entry != cur {
			f.Entries[name] = d.Entry
			changed = true
		}
		if d.Entry.State != cur.State {
			e := config.AutopilotEvent{AtMS: now.UnixMilli(), Optimizer: name, From: orOff(cur.State), To: d.Entry.State, Reason: d.Entry.Reason}
			f.Record(e)
			res.Events = append(res.Events, e)
		}
	}
	// Rule-set trials the user started: judged here, never promoted (see ruletrial.go).
	if rc, evs, err := advanceRuleTrials(ctx, db, cfg, &f, onForAll, traffic, now); err != nil {
		return Result{File: f}, err
	} else if rc {
		changed = true
		res.Events = append(res.Events, evs...)
	}
	// The shadow planner only writes advice. It never touches a state.
	blocked := map[string]string{}
	for _, n := range optimizerNames() {
		if r := blockedBy(on, n); r != "" {
			blocked[n] = r
		}
	}
	unavailable := map[string]string{}
	for _, n := range optimize.PipelineNames {
		if r := whyUnavailable(n, setup); r != "" {
			unavailable[n] = r
		}
	}
	plan := p.Plan(combinedRows(p.planNames(), byName, hookShadow), f.Entries, baselineDone, blocked, unavailable)
	annotateLimits(plan, limitfit.ReadCache(dir), now)
	if !samePlan(plan, f.Plan) {
		f.Plan, f.PlannedMS = plan, now.UnixMilli()
		changed = true
	}
	f.LastEvalMS = now.UnixMilli()
	if changed {
		if err := config.SaveAutopilot(dir, f); err != nil {
			return Result{File: f}, fmt.Errorf("cannot save Autopilot's state: %v", err)
		}
	}
	res.File, res.Changed = f, changed
	return res, nil
}

func orOff(s string) string {
	if s == "" {
		return config.AutopilotOff
	}
	return s
}

func samePlan(a, b []config.AutopilotPlanItem) bool {
	return reflect.DeepEqual(a, b)
}

// annotateLimits adds each item's estimated net saving per shadow session as percent of the plan
// limits, from the saved fit. Display only: nothing Autopilot decides reads it (PRINCIPLES.md rule 6).
func annotateLimits(plan []config.AutopilotPlanItem, fit *limitfit.Result, now time.Time) {
	if fit == nil {
		return
	}
	tbl, err := pricing.Default()
	if err != nil {
		tbl = nil // only the type fit can convert then
	}
	conv := func(s *limitfit.Share) *config.PercentRange {
		if s == nil {
			return nil
		}
		return &config.PercentRange{Point: s.Point, Lo: s.Lo, Hi: s.Hi, Basis: s.Basis}
	}
	for i := range plan {
		it := &plan[i]
		if it.NetTokens <= 0 || it.Sessions <= 0 {
			continue
		}
		d := fit.ContextDelta(it.NetTokens / int64(it.Sessions))
		limitfit.PriceDelta(tbl, &d, now)
		if e := fit.Percent(d); e != nil {
			it.Limit = &config.AutopilotLimitShare{FiveHour: conv(e.FiveHour), Week: conv(e.Week)}
		}
	}
}

// permissive is the setup the conflict check assumes: Autopilot asks only whether
// something already on gets in the way, not whether the user has the gateway or the
// MCP server, so it hides every "not available here" answer.
var permissive = technique.Context{Login: technique.LoginAPIKey, GatewayOn: true,
	Have: []string{technique.CapMCPServer, technique.CapRecoveryStore, technique.CapCustomAgent}}

// onNow lists, by name, every built-in optimizer set to on for new sessions
// (including the ones Autopilot already turned on) and every wrapped tool that is
// switched on.
func onNow(ctx context.Context, db *store.DB, cfg config.Config) []string {
	var on []string
	for _, n := range optimizerNames() {
		if cfg.OptimizerState(n) == config.StateOn {
			on = append(on, n)
		}
	}
	if tools, err := db.ExternalTools().List(ctx); err == nil {
		for _, t := range tools {
			if t.State == "on" {
				on = append(on, t.Name)
			}
		}
	}
	return on
}

// optimizerNames lists the built-in optimizers the registry knows.
func optimizerNames() []string {
	var out []string
	for _, t := range technique.All() {
		if t.Status == technique.StatusBuilt && (t.Route == technique.RouteNative) {
			out = append(out, t.Names...)
		}
	}
	sort.Strings(out)
	return out
}

// blockedBy returns the registry's reason that name cannot be turned on beside what
// is on already, or "" when nothing is in the way. Anything that is name itself is ignored.
func blockedBy(on []string, name string) string {
	var others []string
	for _, o := range on {
		if o != name {
			others = append(others, o)
		}
	}
	if r := technique.CanAdd(others, name, permissive); len(r.Conflicts) > 0 {
		return r.Conflicts[0].Reason
	}
	return ""
}

// viaHooks reports whether the hooks assign the named optimizer's trial arms. It is one list, in the config
// package, because the gateway's assigner has to skip the same names: one session must never get two arms for
// one optimizer. output_trimmer is on that list although it also runs in the gateway.
func viaHooks(name string) bool { return config.HookAssignsTrial(name) }

// probationBlocker says why a trial cannot run for this user now, or "" when it can. A trial needs A/B
// comparison switched on (with it off an optimizer that is "on" is on for everyone), and something to split
// the sessions: the hooks for a hook technique, the gateway for a request-level one. Nothing is checked before
// any traffic is seen, as with the other prerequisite checks.
func probationBlocker(cfg config.Config, tr measure.Traffic, name string) string {
	switch {
	case !cfg.Experiment.Enabled:
		return "A/B comparison is switched off ([experiment] enabled = false)"
	case !tr.Gateway && !tr.Hooks:
		return "Trove has not seen any of your sessions yet"
	case viaHooks(name) && !tr.Gateway && !cfg.History.Enabled:
		return "without the gateway a trial is judged on your local session history, and reading it is switched off ([history] enabled = false)"
	case viaHooks(name) && !tr.Hooks:
		return "the plugin's hooks assign which sessions run with it, and they have recorded nothing recently"
	case !viaHooks(name) && !tr.Gateway:
		return "it needs the gateway to pick which sessions run with it, and none of your recent traffic went through it"
	}
	return ""
}

// otherInProbation returns the name of another optimizer in probation, or "".
func otherInProbation(entries map[string]config.AutopilotEntry, name string) string {
	for n, e := range entries {
		if n != name && e.State == config.AutopilotProbation {
			return n
		}
	}
	return ""
}

// readTrial reads the session-level comparison for an optimizer on trial: sessions assigned since the trial
// began, on against shadow (all metadata). A request-level optimizer is judged on provider-reported cost by
// the gateway's comparison. A hook technique is judged on tokens per session, from the gateway's record of a
// session or else the agent's local history, because a user with no gateway has no priced sessions. Both use
// the experiment package's own status rule and the user's [experiment] thresholds. A read error is returned,
// never turned into a result, so a failure cannot keep an optimizer.
func readTrial(ctx context.Context, db *store.DB, cfg config.Config, name string, cur config.AutopilotEntry, now time.Time) (ProbationEvidence, error) {
	var r experiment.Result
	var err error
	if viaHooks(name) {
		idle := time.Duration(cfg.Gateway.IdleMinutes) * time.Minute
		r, err = experiment.AnalyzeHookTrial(ctx, db, name, cfg.Experiment, cur.SinceMS, now, idle)
	} else {
		r, err = experiment.AnalyzeSince(ctx, db, name, cfg.Experiment, experiment.DefaultPriors(), cur.SinceMS)
	}
	if err != nil {
		return ProbationEvidence{}, err
	}
	return ProbationEvidence{Status: string(r.Status), NOn: r.NOn, NOff: r.NOff}, nil
}
