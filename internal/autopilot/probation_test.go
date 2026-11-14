package autopilot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/store"
)

var probNow = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

// readyEvidence is the evidence for an optimizer whose shadow run looks good and whose trial can run.
func readyEvidence() Evidence {
	return Evidence{BaselineDone: true, ShadowSessions: 30, NetTokens: 50000, NeedsProbation: true}
}

func TestPolicyV2ListsTheRecoverableTechniquesAndLeavesV1Alone(t *testing.T) {
	v1, v2 := DefaultPolicy(), PolicyV2()
	if len(v1.Probation) != 0 || v1.Name != config.AutopilotPolicy {
		t.Errorf("v1 must keep exactly its lossless list: %+v", v1)
	}
	if v2.Name != config.AutopilotPolicyV2 || strings.Join(v2.Eligible, ",") != strings.Join(v1.Eligible, ",") {
		t.Errorf("v2 must contain the v1 list and name itself: %+v", v2)
	}
	for _, n := range v2.Probation {
		if v2.IsEligible(n) {
			t.Errorf("%s is recoverable; it must not also be on the no-trial list", n)
		}
	}
	if PolicyFor("lossless-v9").NeedsProbation("hook_repeat_read") || PolicyFor(config.AutopilotPolicyV2).Name != config.AutopilotPolicyV2 {
		t.Error("an unknown policy must fall back to v1, which only shrinks what Autopilot may do")
	}
}

func TestDecideProbation(t *testing.T) {
	p := PolicyV2()
	day := 24 * time.Hour
	inTrial := func(since time.Time, attempts int) config.AutopilotEntry {
		return config.AutopilotEntry{State: config.AutopilotProbation, SinceMS: since.UnixMilli(), Attempts: attempts}
	}
	cand := config.AutopilotEntry{State: config.AutopilotCandidate}
	cases := []struct {
		name      string
		cur       config.AutopilotEntry
		mod       func(*Evidence)
		want      string
		attempts  int
		reasonHas string
	}{
		{"a candidate that needs a trial starts one", cand, nil, config.AutopilotProbation, 1, "random"},
		{"a candidate that needs none goes on", cand, func(e *Evidence) { e.NeedsProbation = false }, config.AutopilotOn, 1, "shadow evidence"},
		{"no trial when A/B is off", cand, func(e *Evidence) { e.CanProbation = "A/B comparison is switched off" }, config.AutopilotShadow, 0, "switched off"},
		{"one trial at a time", cand, func(e *Evidence) { e.OtherInProbation = "hook_repeat_read" }, config.AutopilotCandidate, 0, "waiting for hook_repeat_read"},
		{"tries are limited", config.AutopilotEntry{State: config.AutopilotCandidate, Attempts: 2}, nil, config.AutopilotShadow, 2, "maximum"},
		{"a shadow entry still needs the evidence", config.AutopilotEntry{State: config.AutopilotShadow}, func(e *Evidence) { e.ShadowSessions = 3 }, config.AutopilotShadow, 0, "3 of"},
		{"a clean result keeps it", inTrial(probNow.Add(-10*day), 1), func(e *Evidence) { e.Probation = ProbationEvidence{"saves", 20, 22} }, config.AutopilotOn, 1, "clean trial"},
		{"a clean result with too few sessions does not", inTrial(probNow.Add(-10*day), 1), func(e *Evidence) { e.Probation = ProbationEvidence{"saves", 20, 19} }, config.AutopilotProbation, 1, "trial running"},
		{"costs more", inTrial(probNow.Add(-5*day), 1), func(e *Evidence) { e.Probation = ProbationEvidence{"costs_more", 25, 25} }, config.AutopilotRolledBack, 1, "worse"},
		{"worse outcomes", inTrial(probNow.Add(-5*day), 1), func(e *Evidence) { e.Probation = ProbationEvidence{"worse_outcomes", 25, 25} }, config.AutopilotRolledBack, 1, "worse"},
		{"a guardrail trip", inTrial(probNow.Add(-5*day), 1), func(e *Evidence) { e.RolledBack = true }, config.AutopilotRolledBack, 1, "guardrail"},
		{"no result yet", inTrial(probNow.Add(-5*day), 1), func(e *Evidence) { e.Probation = ProbationEvidence{"not_enough_data", 4, 5} }, config.AutopilotProbation, 1, "4 sessions with it on"},
		{"no clear effect by the deadline", inTrial(probNow.Add(-29*day), 1), func(e *Evidence) { e.Probation = ProbationEvidence{"no_clear_effect", 30, 30} }, config.AutopilotShadow, 1, "no measurable benefit"},
		{"no clear effect before the deadline waits", inTrial(probNow.Add(-27*day), 1), func(e *Evidence) { e.Probation = ProbationEvidence{"no_clear_effect", 30, 30} }, config.AutopilotProbation, 1, ""},
		{"A/B switched off mid-trial", inTrial(probNow.Add(-2*day), 1), func(e *Evidence) { e.CanProbation = "A/B comparison is switched off" }, config.AutopilotShadow, 1, "cannot continue"},
		{"the hook probe stops covering it", inTrial(probNow.Add(-2*day), 1), func(e *Evidence) { e.Unavailable = "it needs Bash input rewriting" }, config.AutopilotShadow, 1, "no longer available"},
	}
	for _, c := range cases {
		ev := readyEvidence()
		if c.mod != nil {
			c.mod(&ev)
		}
		d := p.Decide(c.cur, ev, probNow)
		if d.Entry.State != c.want || d.Entry.Attempts != c.attempts || !strings.Contains(d.Entry.Reason, c.reasonHas) {
			t.Errorf("%s: state %q attempts %d reason %q; want %q, %d, containing %q",
				c.name, d.Entry.State, d.Entry.Attempts, d.Entry.Reason, c.want, c.attempts, c.reasonHas)
		}
	}
}

// A technique with no shadow saving estimate is judged by the trial alone, so the net-token bar is skipped for
// it, but it still needs the sessions.
func TestATechniqueWithNoEstimateNeedsSessionsButNotTokens(t *testing.T) {
	p := PolicyV2()
	ev := readyEvidence()
	ev.NetTokens, ev.NoSavingEstimate = 0, true
	if d := p.Decide(config.AutopilotEntry{State: config.AutopilotShadow}, ev, probNow); d.Entry.State != config.AutopilotCandidate {
		t.Errorf("no estimate, enough sessions: %q (%s)", d.Entry.State, d.Entry.Reason)
	}
	ev.ShadowSessions = 2
	if d := p.Decide(config.AutopilotEntry{State: config.AutopilotShadow}, ev, probNow); d.Entry.State != config.AutopilotShadow {
		t.Errorf("no estimate, too few sessions: %q", d.Entry.State)
	}
	if p.CannotJudge("hook_quiet_commands") != "" || DefaultPolicy().CannotJudge("hook_quiet_commands") == "" {
		t.Error("only a policy that has a trial for it may judge a technique with no estimate")
	}
}

func v2File(t *testing.T, dir string) {
	t.Helper()
	f, err := Enable(dir, PolicyV2(), probNow.AddDate(0, 0, -40))
	if err != nil {
		t.Fatal(err)
	}
	f.Policy = config.AutopilotPolicyV2
	for _, n := range PolicyV2().Probation {
		f.Entries[n] = config.AutopilotEntry{State: config.AutopilotShadow, SinceMS: probNow.AddDate(0, 0, -40).UnixMilli()}
	}
	if err := config.SaveAutopilot(dir, f); err != nil {
		t.Fatal(err)
	}
}

func advanceV2(t *testing.T, db *store.DB, dir string, cfg config.Config, n int) config.AutopilotFile {
	t.Helper()
	var f config.AutopilotFile
	for i := 0; i < n; i++ {
		res, err := Advance(context.Background(), db, dir, cfg, PolicyV2(), hookNow.Add(time.Duration(i)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		f = res.File
	}
	return f
}

func seedTrialEvidence(t *testing.T, db *store.DB, dir string) {
	t.Helper()
	setInstall(t, db, 30)
	hookSessions(t, db, "hook_repeat_read", 25, 500)
	hookSessions(t, db, "hook_quiet_commands", 25, 0)
	gatewaySessions(t, db, "noise_normalizer", 3, 10) // gateway traffic exists: arms can be assigned
	probed(t, dir)
	v2File(t, dir)
}

func TestAdvanceStartsOneTrialAtATimeAndKeepsNothingWithoutAComparison(t *testing.T) {
	db, dir := open(t)
	seedTrialEvidence(t, db, dir)
	f := advanceV2(t, db, dir, config.Default(), 4)
	a, b := f.Entries["hook_repeat_read"], f.Entries["hook_quiet_commands"]
	if a.State != config.AutopilotProbation {
		t.Fatalf("first in list order should be on trial: %+v", a)
	}
	if b.State == config.AutopilotProbation || !strings.Contains(b.Reason, "waiting for hook_repeat_read") {
		t.Fatalf("second must wait its turn: %+v", b)
	}
	// No sessions have been assigned, so the comparison has nothing to show and no trial can end in "on".
	later, err := Advance(context.Background(), db, dir, config.Default(), PolicyV2(), hookNow.AddDate(0, 0, 40))
	if err != nil {
		t.Fatal(err)
	}
	if got := later.File.Entries["hook_repeat_read"].State; got == config.AutopilotOn {
		t.Errorf("a trial was kept with no comparison read: %q", got)
	}
}

func TestNoTrialWhenOptedOutAndTheArmsComeFromTheRightChannel(t *testing.T) {
	db, dir := open(t)
	seedTrialEvidence(t, db, dir)
	cfg := config.Default()
	cfg.Experiment.Enabled = false
	f := advanceV2(t, db, dir, cfg, 4)
	for _, n := range PolicyV2().Probation {
		e := f.Entries[n]
		if e.State == config.AutopilotProbation || e.State == config.AutopilotOn || !strings.Contains(e.Reason, "switched off") {
			t.Errorf("%s with A/B off: %+v", n, e)
		}
	}

	// The hooks assign the arms for a hook technique, so a user with hooks and no gateway can have a trial.
	db2, dir2 := open(t)
	setInstall(t, db2, 30)
	hookSessions(t, db2, "hook_repeat_read", 25, 500)
	probed(t, dir2)
	v2File(t, dir2)
	f = advanceV2(t, db2, dir2, config.Default(), 4)
	if e := f.Entries["hook_repeat_read"]; e.State != config.AutopilotProbation {
		t.Errorf("a hooks-only user should be on trial: %+v", e)
	}

	// Gateway traffic alone cannot split sessions for a hook technique: the gateway never runs it.
	if got := probationBlocker(config.Default(), measure.Traffic{Gateway: true}, "hook_repeat_read"); !strings.Contains(got, "hooks") {
		t.Errorf("gateway-only traffic for a hook technique: %q", got)
	}
	if got := probationBlocker(config.Default(), measure.Traffic{Hooks: true}, "volatile_prefix_mover"); !strings.Contains(got, "gateway") {
		t.Errorf("hooks-only traffic for a request-level technique: %q", got)
	}
	noHistory := config.Default()
	noHistory.History.Enabled = false
	if got := probationBlocker(noHistory, measure.Traffic{Hooks: true}, "hook_repeat_read"); !strings.Contains(got, "history") {
		t.Errorf("hooks-only with history reading off cannot be measured: %q", got)
	}
	if got := probationBlocker(noHistory, measure.Traffic{Hooks: true, Gateway: true}, "hook_repeat_read"); got != "" {
		t.Errorf("with a gateway the sessions are measured there, history or not: %q", got)
	}
	if got := probationBlocker(config.Default(), measure.Traffic{}, "hook_repeat_read"); got == "" {
		t.Error("no sessions seen yet must not start a trial")
	}
}

func TestV1PolicyNeverTouchesTheProbationList(t *testing.T) {
	db, dir := open(t)
	seedTrialEvidence(t, db, dir)
	for i := 0; i < 4; i++ {
		if _, err := Advance(context.Background(), db, dir, config.Default(), DefaultPolicy(), hookNow.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	f, _ := config.LoadAutopilot(dir)
	for _, n := range PolicyV2().Probation {
		if e := f.Entries[n]; e.State == config.AutopilotProbation || e.State == config.AutopilotOn || e.State == config.AutopilotCandidate {
			t.Errorf("%s moved under the v1 policy: %+v", n, e)
		}
	}
}
