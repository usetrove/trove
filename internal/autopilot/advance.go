package autopilot

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/optimize"
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

// Disable withdraws consent and forgets Autopilot's states and plan. New sessions
// go back to what config.toml says. Sessions already running keep their pins.
func Disable(dir string) error {
	return config.SaveAutopilot(dir, config.AutopilotFile{Entries: map[string]config.AutopilotEntry{}})
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
	res := Result{File: f}
	changed := false
	for _, name := range p.Eligible {
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
			Blocked:        firstNonEmpty(blockedBy(on, name), CannotJudge(name)),
			Unavailable:    whyUnavailable(name, setup),
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
	plan := p.Plan(combinedRows(optimize.PipelineNames, byName, hookShadow), f.Entries, baselineDone, blocked, unavailable)
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
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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
