package autopilot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/experiment"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/measure/limitfit"
	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/tasks"
	"github.com/usetrove/trove/internal/technique"
)

var optT0 = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

// The policy names exactly the list the owner approved on 2026-11-24, in its three groups, and no name twice.
func TestOptimizePolicyNamesTheApprovedList(t *testing.T) {
	p := PolicyOptimize()
	want := map[string][]string{
		"direct":     {config.OptimizerHookColdResume, config.OptimizerHookModelSwitch, config.OptimizerHookColdCompact},
		"eligible":   {"noise_normalizer", "duplicate_result_collapser"},
		"probation":  {optimize.OptimizerEffortRoutinePrompts, optimize.OptimizerThinkingClear},
		"held":       {config.OptimizerHookRoutineSteer, tasks.FeatureRoutine, tasks.FeatureExplore, "ext_ponytail"},
		"shadowOnly": {config.OptimizerHookWriteToEdit, config.OptimizerHookEditBatch},
	}
	got := map[string][]string{"direct": p.Direct, "eligible": p.Eligible, "probation": p.Probation, "held": p.Held, "shadowOnly": p.ShadowOnly}
	for g, w := range want {
		if strings.Join(got[g], ",") != strings.Join(w, ",") {
			t.Errorf("%s: %v, want %v", g, got[g], w)
		}
	}
	seen := map[string]bool{}
	for _, n := range p.Names() {
		if seen[n] {
			t.Errorf("%s is named twice", n)
		}
		seen[n] = true
	}
	if p.Outcome != OutcomeBill {
		t.Errorf("outcome %q", p.Outcome)
	}
	if PolicyFor(config.AutopilotPolicyOptimize).Name != config.AutopilotPolicyOptimize {
		t.Error("PolicyFor does not know the optimize policy")
	}
}

// Every T2 and T3 technique the policy names has no benchmark verdict today, so each says it is waiting for one.
// The direct guards are T1, so the verdict rule never holds them back.
func TestOptimizePolicyHoldsEveryT2AndT3ForAVerdict(t *testing.T) {
	p := PolicyOptimize()
	for _, n := range p.Names() {
		x, ok := technique.Get(RuleTrialTool(n))
		if !ok {
			t.Errorf("%s: unknown to the registry", n)
			continue
		}
		if x.Tier == technique.T1 {
			if VerdictBlocker(n) != "" {
				t.Errorf("%s is T1 but held for a verdict", n)
			}
			continue
		}
		if x.BenchVerdict == "" && VerdictBlocker(n) == "" {
			t.Errorf("%s is %s with no verdict but is not held", n, x.Tier)
		}
	}
	for _, n := range p.Direct {
		if x, _ := technique.Get(n); x.Tier != technique.T1 {
			t.Errorf("direct guard %s is %s; only a T1 guard may skip shadow", n, x.Tier)
		}
	}
}

func TestEnableOptimizeTurnsTheGuardsOnAndTheRestToShadow(t *testing.T) {
	dir := t.TempDir()
	f, err := Enable(dir, PolicyOptimize(), optT0)
	if err != nil {
		t.Fatal(err)
	}
	if f.Policy != config.AutopilotPolicyOptimize || !f.ColdResumeAsk || f.SettingsPolicy != config.AutopilotSettingsPolicy {
		t.Fatalf("consent record %+v", f)
	}
	p := PolicyOptimize()
	for _, n := range p.Names() {
		e := f.Entries[n]
		want := config.AutopilotShadow
		if p.IsDirect(n) {
			want = config.AutopilotOn
		}
		if e.State != want {
			t.Errorf("%s: %s, want %s", n, e.State, want)
		}
	}
	if f.HasTrials() {
		t.Error("the optimize policy agreed to the lossless trials too")
	}
	cfg, err := config.Load(dir, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	for n, want := range map[string]string{
		config.OptimizerHookColdResume: config.StateOn, config.OptimizerHookModelSwitch: config.StateOn,
		config.OptimizerHookColdCompact: config.StateOn, "noise_normalizer": config.StateShadow,
		config.OptimizerHookWriteToEdit: config.StateShadow, config.OptimizerHookRoutineSteer: config.StateShadow,
		optimize.OptimizerThinkingClear: config.StateShadow,
	} {
		if got := cfg.OptimizerState(n); got != want {
			t.Errorf("%s: state %q, want %q", n, got, want)
		}
	}
	if got := cfg.ColdResumeAction(); got != config.ColdResumeAsk {
		t.Errorf("cold-resume action %q, want ask", got)
	}
}

// A user who sets the guard in config.toml takes it over, action included (rule 6: never over a user's setting).
func TestTheUsersOwnColdResumeSettingWins(t *testing.T) {
	dir := t.TempDir()
	if _, err := Enable(dir, PolicyOptimize(), optT0); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, config.FileName), []byte("[optimizers]\nhook_cold_resume = \"on\"\n[optimize]\ncold_resume_action = \"warn\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(dir, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.ColdResumeAction(); got != config.ColdResumeWarn {
		t.Errorf("action %q, want the user's warn", got)
	}
	if err := os.WriteFile(filepath.Join(dir, config.FileName), []byte("[optimizers]\nhook_cold_resume = \"off\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _ = config.Load(dir, func(string) string { return "" })
	if got := cfg.OptimizerState(config.OptimizerHookColdResume); got != config.StateOff {
		t.Errorf("state %q, want the user's off", got)
	}
}

func TestDecideDirect(t *testing.T) {
	p := PolicyOptimize()
	g := config.OptimizerHookColdResume
	day := 24 * time.Hour
	on := config.AutopilotEntry{State: config.AutopilotOn, SinceMS: optT0.UnixMilli(), Attempts: 1}
	back := config.AutopilotEntry{State: config.AutopilotRolledBack, SinceMS: optT0.UnixMilli(), Attempts: 1}
	cases := []struct {
		name  string
		cur   config.AutopilotEntry
		ev    Evidence
		at    time.Time
		want  string
		clear bool
	}{
		{"off goes on with no shadow evidence", config.AutopilotEntry{}, Evidence{}, optT0, config.AutopilotOn, false},
		{"unavailable stays in shadow", config.AutopilotEntry{}, Evidence{Unavailable: "no hooks"}, optT0, config.AutopilotShadow, false},
		{"on stays on", on, Evidence{}, optT0.Add(day), config.AutopilotOn, false},
		{"its guardrail rolls it back", on, Evidence{RolledBack: true}, optT0.Add(day), config.AutopilotRolledBack, false},
		{"waits out the cool-down", back, Evidence{RolledBack: true}, optT0.Add(2 * day), config.AutopilotRolledBack, false},
		{"on again after the cool-down", back, Evidence{RolledBack: true}, optT0.Add(8 * day), config.AutopilotOn, true},
		{"left to the user after the last try", config.AutopilotEntry{State: config.AutopilotRolledBack, SinceMS: optT0.UnixMilli(), Attempts: 2},
			Evidence{RolledBack: true}, optT0.Add(30 * day), config.AutopilotRolledBack, false},
		{"a rollback in effect is waited out", config.AutopilotEntry{}, Evidence{RolledBack: true}, optT0, config.AutopilotRolledBack, false},
	}
	for _, c := range cases {
		d := p.DecideFor(g, c.cur, c.ev, c.at)
		if d.Entry.State != c.want || d.ClearRollback != c.clear {
			t.Errorf("%s: got %s (clear %v), want %s (clear %v): %s", c.name, d.Entry.State, d.ClearRollback, c.want, c.clear, d.Entry.Reason)
		}
	}
}

// However good the evidence, held and shadow-only techniques never leave shadow, and a T3 trial candidate with no
// verdict is held back before it can become a candidate.
func TestNothingBeyondT1LeavesShadowWithoutAVerdict(t *testing.T) {
	p := PolicyOptimize()
	great := Evidence{BaselineDone: true, ShadowSessions: 1000, NetTokens: 1 << 30, NeedsProbation: true}
	for _, n := range append(append([]string{}, p.Held...), p.ShadowOnly...) {
		cur := config.AutopilotEntry{}
		for i := 0; i < 5; i++ {
			cur = p.DecideFor(n, cur, great, optT0.Add(time.Duration(i)*time.Hour)).Entry
		}
		if cur.State != config.AutopilotShadow || cur.Reason == "" {
			t.Errorf("%s: %+v", n, cur)
		}
	}
	for _, n := range p.Probation {
		ev := great
		ev.Blocked = VerdictBlocker(n)
		cur := config.AutopilotEntry{State: config.AutopilotShadow}
		for i := 0; i < 5; i++ {
			cur = p.DecideFor(n, cur, ev, optT0.Add(time.Duration(i)*time.Hour)).Entry
		}
		if cur.State != config.AutopilotShadow || !strings.Contains(cur.Reason, "benchmark verdict") {
			t.Errorf("%s: %+v", n, cur)
		}
	}
	// A trial the user started for a held technique is theirs: the policy leaves it alone.
	trial := config.AutopilotEntry{State: config.AutopilotProbation, SinceMS: 1, Reason: "trial running"}
	if d := p.DecideFor(config.OptimizerHookRoutineSteer, trial, great, optT0); d.Entry != trial {
		t.Errorf("the user's trial was changed: %+v", d.Entry)
	}
}

// The routine steer runs in shadow under the policy (so its counts build up); a request-level trial gets its arms
// from the gateway, a hook one does not.
func TestOptimizeStates(t *testing.T) {
	f := config.AutopilotFile{Enabled: true, Policy: config.AutopilotPolicyOptimize, Entries: map[string]config.AutopilotEntry{
		config.OptimizerHookRoutineSteer:       {State: config.AutopilotShadow},
		optimize.OptimizerThinkingClear:        {State: config.AutopilotProbation},
		config.OptimizerHookRepeatRead:         {State: config.AutopilotProbation},
		"ext_ponytail":                         {State: config.AutopilotShadow},
		config.OptimizerHookColdCompact:        {State: config.AutopilotOn},
		config.OptimizerHookEditBatch:          {State: config.AutopilotShadow},
		optimize.OptimizerEffortRoutinePrompts: {State: config.AutopilotShadow},
	}}
	s := f.StatesWithArms(true)
	for n, want := range map[string]string{
		config.OptimizerHookRoutineSteer: config.StateShadow, optimize.OptimizerThinkingClear: config.StateOn,
		config.OptimizerHookRepeatRead: config.StateShadow, config.OptimizerHookColdCompact: config.StateOn,
		config.OptimizerHookEditBatch: config.StateShadow,
	} {
		if s[n] != want {
			t.Errorf("%s: %q, want %q", n, s[n], want)
		}
	}
	if _, there := s["ext_ponytail"]; there {
		t.Error("a rule set got a state")
	}
	if got := f.States()[optimize.OptimizerThinkingClear]; got != config.StateShadow {
		t.Errorf("without arms a trial is shadow, got %q", got)
	}
	f.Entries[config.OptimizerHookRoutineSteer] = config.AutopilotEntry{State: config.AutopilotProbation}
	if _, there := f.States()[config.OptimizerHookRoutineSteer]; there {
		t.Error("the steer's own trial got a state; its arms come from the hooks")
	}
}

func TestBillWeigher(t *testing.T) {
	u := experiment.SessionUsage{USD: 2, HasUSD: true, FromGateway: true}
	w := BillWeigher(measure.Traffic{Gateway: true}, nil, optT0)
	if v, ok := w.Weigh(u); w.Metric != experiment.MetricAPICost || !ok || v != 2 {
		t.Errorf("API key: %s %v %v", w.Metric, v, ok)
	}
	if _, ok := w.Weigh(experiment.SessionUsage{}); ok {
		t.Error("an unpriced session was weighed")
	}
	if w := BillWeigher(measure.Traffic{Hooks: true}, nil, optT0); w.Weigh != nil {
		t.Error("with no fit the trial should keep tokens per session")
	}
	fit := &limitfit.Result{FiveHour: limitfit.MeterFit{PerUSD: &limitfit.Range{Point: 0.5, Lo: 0.4, Hi: 0.6}}}
	w = BillWeigher(measure.Traffic{Hooks: true}, fit, optT0)
	if v, ok := w.Weigh(u); w.Metric != experiment.MetricLimitPct || !ok || v != 1 {
		t.Errorf("subscriber: %s %v %v", w.Metric, v, ok)
	}
}

func TestPolicyViewLabelsEveryFigure(t *testing.T) {
	dir := t.TempDir()
	f, err := Enable(dir, PolicyOptimize(), optT0)
	if err != nil {
		t.Fatal(err)
	}
	f.Plan = []config.AutopilotPlanItem{{Optimizer: "noise_normalizer", Sessions: 12, NetTokens: 45000}}
	f.Entries[config.OptimizerHookColdCompact] = config.AutopilotEntry{State: config.AutopilotRolledBack, Reason: "its guardrail rolled it back to shadow"}
	f.Record(config.AutopilotEvent{AtMS: optT0.UnixMilli(), Optimizer: config.OptimizerHookColdCompact, From: "on", To: config.AutopilotRolledBack, Reason: "you ran /compact again"})
	v := BuildPolicyView(context.Background(), nil, f, false, nil, optT0.Add(time.Hour))
	if v == nil {
		t.Fatal("no view")
	}
	txt := v.Text()
	for _, want := range []string{"Optimize policy", "On from the next session", "Shadow first", "Shadow only", "12 shadow sessions, about 45,000 tokens net", "(estimated)",
		"Rolled back in the last 30 days", "you ran /compact again", "tokens per session (measured)", "cache_ttl_subagents"} {
		if !strings.Contains(txt, want) {
			t.Errorf("view lacks %q:\n%s", want, txt)
		}
	}
	for _, it := range v.Items {
		for _, fg := range it.Figures {
			if fg.Label != string(measure.Measured) && fg.Label != string(measure.Estimated) {
				t.Errorf("%s: figure %q has label %q", it.Name, fg.Text, fg.Label)
			}
		}
	}
	if BuildPolicyView(context.Background(), nil, config.AutopilotFile{Enabled: true, Policy: config.AutopilotPolicy}, false, nil, optT0) != nil {
		t.Error("a lossless-v1 user got an optimize view")
	}
}

// Upgrading from lossless-v2 never widens its trials without a new yes.
func TestUpgradingFromV2WithdrawsItsTrials(t *testing.T) {
	dir := t.TempDir()
	if _, err := Enable(dir, DefaultPolicy(), optT0); err != nil {
		t.Fatal(err)
	}
	if _, err := EnableTrials(dir, PolicyV2(), optT0); err != nil {
		t.Fatal(err)
	}
	f, err := Enable(dir, PolicyOptimize(), optT0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if f.HasTrials() {
		t.Error("v2 trials were carried into the optimize policy without a yes")
	}
	if _, there := f.Entries[config.OptimizerHookRepeatRead]; there {
		t.Error("a v2 trial candidate survived the upgrade")
	}
	if len(PolicyOf(f).Probation) != len(PolicyOptimize().Probation) {
		t.Errorf("probation %v", PolicyOf(f).Probation)
	}
	f, err = EnableTrials(dir, TrialPolicy(), optT0.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !f.IsOptimize() || !f.HasTrials() || len(PolicyOf(f).Probation) != len(PolicyOptimize().Probation)+len(TrialPolicy().Probation) {
		t.Errorf("trials under the optimize policy: %s %v", f.Policy, PolicyOf(f).Probation)
	}
	f, err = DisableTrials(dir, optT0.Add(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !f.IsOptimize() || f.HasTrials() {
		t.Errorf("trials off left policy %s, trials %v", f.Policy, f.HasTrials())
	}
}

// A check at a session boundary keeps the guards on, though they have no saving estimate to judge, and keeps every
// item beyond T1 in shadow with its reason.
func TestAdvanceUnderTheOptimizePolicy(t *testing.T) {
	db, dir := open(t)
	if _, err := Enable(dir, PolicyOptimize(), optT0); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(dir, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	res, err := Advance(context.Background(), db, dir, cfg, CurrentPolicy(dir), optT0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	p := PolicyOptimize()
	for _, n := range p.Direct {
		if e := res.File.Entries[n]; e.State != config.AutopilotOn {
			t.Errorf("%s: %+v after a check, want on", n, e)
		}
	}
	for _, n := range append(append(append([]string{}, p.Held...), p.ShadowOnly...), p.Probation...) {
		if e := res.File.Entries[n]; e.State != config.AutopilotShadow || e.Reason == "" {
			t.Errorf("%s: %+v after a check, want shadow with a reason", n, e)
		}
	}
	for _, it := range res.File.Plan {
		if it.Verdict == VerdictWouldTurnOn && VerdictBlocker(it.Optimizer) != "" {
			t.Errorf("the planner would turn on %s with no verdict", it.Optimizer)
		}
	}
}
