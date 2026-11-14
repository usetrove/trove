package autopilot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/measure"
)

func row(sessions int, tokens float64) measure.ShadowRow {
	return measure.ShadowRow{Sessions: sessions, Tokens: measure.Figure{Value: tokens, Label: measure.Estimated}}
}

func verdicts(plan []config.AutopilotPlanItem) map[string]string {
	m := map[string]string{}
	for _, it := range plan {
		m[it.Optimizer] = it.Verdict
	}
	return m
}

func TestPlannerVerdicts(t *testing.T) {
	p := DefaultPolicy()
	rows := map[string]measure.ShadowRow{
		"noise_normalizer":           row(20, 9000),  // eligible, enough evidence
		"duplicate_result_collapser": row(20, 100),   // enough sessions, saving below the bar
		"output_trimmer":             row(20, 50000), // great evidence, but never automatic
		"cache_breakpoints":          row(3, 10),     // too few sessions
		"volatile_prefix_mover":      row(20, -40),   // enough sessions, not positive
	}
	entries := map[string]config.AutopilotEntry{}
	got := verdicts(p.Plan(rows, entries, true, nil, nil))
	want := map[string]string{
		"noise_normalizer":           VerdictWouldTurnOn,
		"duplicate_result_collapser": VerdictCollecting,
		"output_trimmer":             VerdictNeedsYourChoice,
		"cache_breakpoints":          VerdictCollecting,
		"volatile_prefix_mover":      VerdictNotWorthIt,
	}
	for n, w := range want {
		if got[n] != w {
			t.Errorf("%s: %q, want %q", n, got[n], w)
		}
	}

	entries["noise_normalizer"] = config.AutopilotEntry{State: config.AutopilotOn}
	if v := verdicts(p.Plan(rows, entries, true, nil, nil))["noise_normalizer"]; v != VerdictOn {
		t.Errorf("an optimizer Autopilot already turned on is %q, want on", v)
	}
	if v := verdicts(p.Plan(rows, map[string]config.AutopilotEntry{}, false, nil, nil))["noise_normalizer"]; v != VerdictCollecting {
		t.Errorf("before the baseline is done it is %q, want collecting", v)
	}
}

// The cost of added text comes from the technique registry. hook_repeat_read adds a 68-token denial
// each time it blocks and does not subtract it itself, so Autopilot charges 68 per event.
func TestPlannerChargesTheRegistrysDeclaredOverhead(t *testing.T) {
	p := DefaultPolicy()
	r := row(20, 9000)
	r.Events = 200 // 200 x 68 = 13,600 of added text against 9,000 removed
	plan := p.Plan(map[string]measure.ShadowRow{"hook_repeat_read": r}, nil, true, nil, nil)
	for _, it := range plan {
		if it.Optimizer == "hook_repeat_read" && (it.Verdict != VerdictNotWorthIt || it.NetTokens != 9000-13600) {
			t.Errorf("got %q with %d net tokens, want not_worth_it and %d", it.Verdict, it.NetTokens, 9000-13600)
		}
	}
}

func TestNetOfChargesPerUseUnlessTheEstimateAlreadyDid(t *testing.T) {
	p := DefaultPolicy()
	cases := []struct {
		name         string
		sessions, ev int
		removed      float64
		want         int64
	}{
		{"noise_normalizer", 10, 100, 5000, 5000},        // adds no text
		{"hook_repeat_read", 10, 100, 5000, 5000 - 6800}, // 68 per denial, not netted
		{"hook_read_limit", 10, 100, 5000, 5000},         // 42 per note, already inside its estimate
		{"output_trimmer", 10, 100, 5000, 5000},          // adds text, declares no numbers: shown as is; ChargeableCost stops promotion
		{"not_a_technique", 10, 100, 5000, 5000},
	}
	for _, c := range cases {
		if got := p.NetOf(c.name, c.sessions, c.ev, c.removed); got != c.want {
			t.Errorf("%s: net %d, want %d", c.name, got, c.want)
		}
	}
}

func TestPlannerCoversEveryOptimizerAndTiersTheTrimmer(t *testing.T) {
	plan := DefaultPolicy().Plan(nil, nil, true, nil, nil)
	if len(plan) < 5 {
		t.Fatalf("plan lists %d optimizers", len(plan))
	}
	for _, it := range plan {
		if it.Reason == "" {
			t.Errorf("%s has no plain-English reason", it.Optimizer)
		}
		if it.Optimizer == "output_trimmer" && it.Tier != "recoverable" {
			t.Errorf("output_trimmer tier %q, want recoverable", it.Tier)
		}
	}
}

// The planner writes advice and never moves a state, and a user's setting is untouched.
func TestAdvanceRefreshesThePlanWithoutChangingStates(t *testing.T) {
	db, dir := open(t)
	if _, err := Enable(dir, DefaultPolicy(), t0); err != nil {
		t.Fatal(err)
	}
	before, _ := config.LoadAutopilot(dir)
	res, err := Advance(context.Background(), db, dir, config.Default(), DefaultPolicy(), t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.File.Plan) == 0 || res.File.PlannedMS == 0 {
		t.Fatal("Advance did not record the planner's advice")
	}
	for n, e := range before.Entries {
		if res.File.Entries[n].State != e.State {
			t.Errorf("%s moved from %q to %q with no evidence", n, e.State, res.File.Entries[n].State)
		}
	}
	for _, it := range res.File.Plan {
		if it.Verdict == VerdictWouldTurnOn || it.Verdict == VerdictOn {
			t.Errorf("%s: %q with no usage recorded", it.Optimizer, it.Verdict)
		}
	}
}

func TestEnableRecordsHistoryAndHistoryIsCapped(t *testing.T) {
	_, dir := open(t)
	f, err := Enable(dir, DefaultPolicy(), t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.History) != len(DefaultPolicy().Eligible) || f.History[0].To != config.AutopilotShadow {
		t.Errorf("history after Enable: %+v", f.History)
	}
	for i := 0; i < config.AutopilotHistoryMax+20; i++ {
		f.Record(config.AutopilotEvent{AtMS: int64(i)})
	}
	if len(f.History) != config.AutopilotHistoryMax || f.History[len(f.History)-1].AtMS != int64(config.AutopilotHistoryMax+19) {
		t.Errorf("history has %d entries, newest %d", len(f.History), f.History[len(f.History)-1].AtMS)
	}
}

func TestAnnounceSendsEachNoticeOnce(t *testing.T) {
	_, dir := open(t)
	f := config.AutopilotFile{Enabled: true, Policy: config.AutopilotPolicy, Entries: map[string]config.AutopilotEntry{}}
	f.Record(config.AutopilotEvent{AtMS: 1, Optimizer: "noise_normalizer", From: "off", To: config.AutopilotShadow})
	f.Record(config.AutopilotEvent{AtMS: 2, Optimizer: "noise_normalizer", From: "candidate", To: config.AutopilotOn, Reason: "r"})
	f.Record(config.AutopilotEvent{AtMS: 3, Optimizer: "duplicate_result_collapser", From: "on", To: config.AutopilotRolledBack, Reason: "a guardrail rolled it back to shadow"})
	if err := config.SaveAutopilot(dir, f); err != nil {
		t.Fatal(err)
	}
	var sent []string
	send := func(title, body string) { sent = append(sent, body) }
	if err := Announce(dir, true, send); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 || !strings.Contains(sent[0], "Turned on noise_normalizer") || !strings.Contains(sent[1], "Rolled duplicate_result_collapser back") {
		t.Fatalf("sent %q; want a turn-on and a rollback notice, and none for shadow", sent)
	}
	if err := Announce(dir, true, send); err != nil || len(sent) != 2 {
		t.Errorf("a second call sent more: %d, %v", len(sent), err)
	}
}

func TestAnnounceStaysQuietWhenNotificationsAreOffAndDoesNotReplayLater(t *testing.T) {
	_, dir := open(t)
	f := config.AutopilotFile{Enabled: true, Policy: config.AutopilotPolicy, Entries: map[string]config.AutopilotEntry{}}
	f.Record(config.AutopilotEvent{AtMS: 5, Optimizer: "noise_normalizer", To: config.AutopilotOn})
	_ = config.SaveAutopilot(dir, f)
	count := 0
	send := func(string, string) { count++ }
	if err := Announce(dir, false, send); err != nil || count != 0 {
		t.Fatalf("notifications off: sent %d, %v", count, err)
	}
	if err := Announce(dir, true, send); err != nil || count != 0 {
		t.Errorf("turning notifications on replayed an old notice: sent %d", count)
	}
}

func TestSummarize(t *testing.T) {
	if Summarize(config.AutopilotFile{}, t0, 7) != nil {
		t.Error("a user who never agreed gets a summary")
	}
	f := config.AutopilotFile{Enabled: true, Policy: config.AutopilotPolicy, Entries: map[string]config.AutopilotEntry{
		"noise_normalizer":           {State: config.AutopilotOn},
		"duplicate_result_collapser": {State: config.AutopilotRolledBack, SinceMS: t0.UnixMilli(), Reason: "a guardrail rolled it back to shadow"},
	}}
	f.Record(config.AutopilotEvent{AtMS: t0.AddDate(0, 0, -30).UnixMilli(), Optimizer: "noise_normalizer", To: config.AutopilotShadow})
	f.Record(config.AutopilotEvent{AtMS: t0.AddDate(0, 0, -2).UnixMilli(), Optimizer: "noise_normalizer", To: config.AutopilotOn, Reason: "turned on from shadow evidence (estimated)"})
	s := Summarize(f, t0, 7)
	if len(s.Changes) != 1 || s.Changes[0].Optimizer != "noise_normalizer" {
		t.Errorf("changes %+v; only the last 7 days belong", s.Changes)
	}
	txt := s.Text()
	for _, want := range []string{"On now: noise_normalizer", "Rolled back to shadow", "duplicate_result_collapser", "Changes in the last 7 days"} {
		if !strings.Contains(txt, want) {
			t.Errorf("summary lacks %q:\n%s", want, txt)
		}
	}
	if strings.Contains(txt, "$") {
		t.Errorf("the summary states a dollar figure it cannot back up:\n%s", txt)
	}
}

func TestAnOptimizerWithUndeclaredCostIsNeverChargeable(t *testing.T) {
	if ChargeableCost("output_trimmer") == "" {
		t.Error("output_trimmer adds text with no declared numbers; Autopilot must refuse it")
	}
	for _, n := range []string{"noise_normalizer", "duplicate_result_collapser", "hook_repeat_read", "hook_read_limit", "not_a_technique"} {
		if why := ChargeableCost(n); why != "" {
			t.Errorf("%s: %q", n, why)
		}
	}
}
