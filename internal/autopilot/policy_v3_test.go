package autopilot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/hookcaps"
	"github.com/usetrove/trove/internal/mcp/install"
	"github.com/usetrove/trove/internal/technique"
)

var v3New = []string{"output_trimmer", "hook_large_output", "hook_read_limit"}

func TestPolicyV3IsV2PlusTheThreeNewCandidatesInOrder(t *testing.T) {
	v1, v2, v3 := DefaultPolicy(), PolicyV2(), PolicyV3()
	if v3.Name != config.AutopilotPolicyV3 || PolicyFor(config.AutopilotPolicyV3).Name != v3.Name {
		t.Fatalf("name: %+v", v3)
	}
	if strings.Join(v3.Eligible, ",") != strings.Join(v1.Eligible, ",") {
		t.Errorf("v3 must keep the lossless list exactly: %v", v3.Eligible)
	}
	want := append(append([]string{}, v2.Probation...), v3New...)
	if strings.Join(v3.Probation, ",") != strings.Join(want, ",") {
		t.Errorf("probation = %v, want %v (one trial at a time, in this order)", v3.Probation, want)
	}
	if v3.ProbationMinSessionsPerArm != v2.ProbationMinSessionsPerArm || v3.ProbationMaxDays != v2.ProbationMaxDays {
		t.Error("v3 uses the same placeholder numbers as v2 until a benchmark sets them")
	}
	// v2 is untouched: a user who agreed to it gets exactly its two.
	if strings.Join(v2.Probation, ",") != "hook_repeat_read,hook_quiet_commands" {
		t.Errorf("v2 changed: %v", v2.Probation)
	}
	if TrialPolicy().Name != v3.Name {
		t.Error("`trove autopilot trials on` must offer the newest policy")
	}
}

// Every candidate can be judged by Autopilot: either its cost is declared as numbers, or it is judged by its trial.
func TestEveryV3CandidateCanBeJudged(t *testing.T) {
	p := PolicyV3()
	for _, n := range p.Probation {
		if why := p.CannotJudge(n); why != "" {
			t.Errorf("%s: Autopilot cannot judge it: %s", n, why)
		}
		if !p.NeedsProbation(n) {
			t.Errorf("%s is recoverable; it must go through a trial", n)
		}
		if p.IsEligible(n) {
			t.Errorf("%s must not be in the no-trial list", n)
		}
		if Tier(n) != "recoverable" && n != "hook_large_output" {
			t.Errorf("%s: tier %q", n, Tier(n))
		}
	}
}

// The trimmer's tool definitions are charged to it every session (rule 15), so a saving the definitions ate does
// not promote it.
func TestTheTrimmerIsChargedForItsToolDefinitions(t *testing.T) {
	p := PolicyV3()
	// 30 sessions removing 300 tokens each is 9000 tokens, less 351 per session.
	if got := p.NetOf("output_trimmer", 30, 90, 9000); got != 9000-30*351 {
		t.Errorf("net = %d, want %d", got, 9000-30*351)
	}
	if got := p.NetOf("output_trimmer", 30, 90, 30*300); got > 0 {
		t.Errorf("a saving smaller than the tool definitions must net to nothing: %d", got)
	}
}

// One assigner per optimizer: the hooks assign every trial candidate, and the gateway never does.
func TestHookAssignedTrialsMatchTheV3List(t *testing.T) {
	for _, n := range PolicyV3().Probation {
		if !viaHooks(n) {
			t.Errorf("%s: the hooks must assign its trial", n)
		}
		f := config.AutopilotFile{Enabled: true, Policy: config.AutopilotPolicyV3, Entries: map[string]config.AutopilotEntry{n: {State: config.AutopilotProbation}}}
		if got := f.StatesWithArms(true)[n]; got != config.StateShadow {
			t.Errorf("%s: the gateway's assigner must see shadow, got %q", n, got)
		}
		if got := f.Trials(); len(got) != 1 || got[0] != n {
			t.Errorf("%s: the hooks must see it as a trial, got %v", n, got)
		}
	}
	// A request-level lossless optimizer kept by Autopilot is unaffected.
	if config.HookAssignsTrial("noise_normalizer") || config.HookAssignsTrial("duplicate_result_collapser") {
		t.Error("lossless optimizers are not trial candidates")
	}
}

func TestEnableTrialsV3KeepsWhatV2HadAndAddsTheNewCandidatesInShadow(t *testing.T) {
	_, dir := open(t)
	if _, err := Enable(dir, DefaultPolicy(), probNow); err != nil {
		t.Fatal(err)
	}
	if _, err := EnableTrials(dir, PolicyV2(), probNow); err != nil {
		t.Fatal(err)
	}
	f, _ := config.LoadAutopilot(dir)
	f.Entries["hook_repeat_read"] = config.AutopilotEntry{State: config.AutopilotOn, Attempts: 1, Reason: "kept after a clean trial"}
	f.ConsentedAt = 111
	if err := config.SaveAutopilot(dir, f); err != nil {
		t.Fatal(err)
	}
	later := probNow.Add(48 * time.Hour)
	f, err := EnableTrials(dir, PolicyV3(), later)
	if err != nil {
		t.Fatal(err)
	}
	if f.Policy != config.AutopilotPolicyV3 || f.TrialsConsentedAt != later.UnixMilli() || f.ConsentedAt != 111 {
		t.Errorf("consent: policy %q trials %d consent %d", f.Policy, f.TrialsConsentedAt, f.ConsentedAt)
	}
	if e := f.Entries["hook_repeat_read"]; e.State != config.AutopilotOn || e.Attempts != 1 {
		t.Errorf("what v2 had must be kept: %+v", e)
	}
	for _, n := range v3New {
		if e := f.Entries[n]; e.State != config.AutopilotShadow {
			t.Errorf("%s: %q, want shadow (a new candidate only estimates)", n, e.State)
		}
		if got := f.States()[n]; got != config.StateShadow {
			t.Errorf("%s: states say %q", n, got)
		}
	}
	if CurrentPolicy(dir).Name != config.AutopilotPolicyV3 {
		t.Error("CurrentPolicy must follow the new consent")
	}
	if _, err := EnableTrials(dir, DefaultPolicy(), later); err == nil {
		t.Error("a policy with no trials cannot be enabled as trials")
	}
}

func TestDisableTrialsRemovesV3CandidatesAndKeepsLosslessStates(t *testing.T) {
	_, dir := open(t)
	_, _ = Enable(dir, DefaultPolicy(), probNow)
	_, _ = EnableTrials(dir, PolicyV3(), probNow)
	f, _ := config.LoadAutopilot(dir)
	f.Entries["output_trimmer"] = config.AutopilotEntry{State: config.AutopilotProbation}
	f.Entries["hook_read_limit"] = config.AutopilotEntry{State: config.AutopilotOn}
	f.Entries["noise_normalizer"] = config.AutopilotEntry{State: config.AutopilotOn}
	if err := config.SaveAutopilot(dir, f); err != nil {
		t.Fatal(err)
	}
	f, err := DisableTrials(dir, probNow)
	if err != nil {
		t.Fatal(err)
	}
	if f.Policy != config.AutopilotPolicy || f.TrialsConsentedAt != 0 {
		t.Errorf("after: %+v", f)
	}
	for _, n := range PolicyV3().Probation {
		if _, there := f.Entries[n]; there {
			t.Errorf("%s must be forgotten when trials are withdrawn", n)
		}
	}
	if f.Entries["noise_normalizer"].State != config.AutopilotOn {
		t.Error("the lossless state must be untouched")
	}
}

// A v2 file is read by this program as v2: the new candidates are never evaluated for a user who did not agree.
func TestAV2UserIsNeverGivenTheNewCandidates(t *testing.T) {
	db, dir := open(t)
	setInstall(t, db, 45)
	hookSessions(t, db, "hook_read_limit", 30, 4000)
	probed(t, dir, hookcaps.Capability{Agent: "claude", AgentVersion: "2.1.319", Tool: "Read", InputRewrite: hookcaps.Yes, Method: "live", ProbedAt: "2026-11-14T00:00:00Z"})
	v2File(t, dir)
	res, err := Advance(context.Background(), db, dir, config.Default(), CurrentPolicy(dir), hookNow)
	if err != nil {
		t.Fatal(err)
	}
	if CurrentPolicy(dir).Name != config.AutopilotPolicyV2 {
		t.Fatal("the test needs a v2 file")
	}
	if _, there := res.File.Entries["hook_read_limit"]; there {
		t.Errorf("a v2 user has no entry for a v3 candidate: %+v", res.File.Entries)
	}
}

// For a v3 user with hook evidence, a candidate whose shadow run looks good starts a trial, and only one at a time.
func TestAV3CandidateWithGoodShadowEvidenceStartsATrialOneAtATime(t *testing.T) {
	db, dir := open(t)
	setInstall(t, db, 45)
	probed(t, dir,
		hookcaps.Capability{Agent: "claude", AgentVersion: "2.1.319", Tool: "Read", InputRewrite: hookcaps.Yes, Method: "live", ProbedAt: "2026-11-14T00:00:00Z"},
		hookcaps.Capability{Agent: "claude", AgentVersion: "2.1.319", Tool: "Bash", InputRewrite: hookcaps.Yes, OutputRewrite: hookcaps.Yes, Method: "live", ProbedAt: "2026-11-14T00:00:00Z"})
	if err := install.SaveCLI(dir, []install.CLIInstall{{Agent: "claude"}}); err != nil {
		t.Fatal(err)
	}
	if !install.Registered(dir) {
		t.Skip("could not simulate a registered MCP server in this build")
	}
	if _, err := Enable(dir, DefaultPolicy(), hookNow.AddDate(0, 0, -40)); err != nil {
		t.Fatal(err)
	}
	if _, err := EnableTrials(dir, PolicyV3(), hookNow.AddDate(0, 0, -40)); err != nil {
		t.Fatal(err)
	}
	// Two of the new candidates have plenty of shadow evidence.
	hookSessions(t, db, "output_trimmer", 25, 3000)
	hookSessions(t, db, "hook_read_limit", 25, 3000)
	var f config.AutopilotFile
	for i := 0; i < 6; i++ {
		res, err := Advance(context.Background(), db, dir, config.Default(), PolicyV3(), hookNow.Add(time.Duration(i)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		f = res.File
	}
	trials := f.Trials()
	if len(trials) != 1 {
		t.Fatalf("exactly one trial at a time, got %v (entries %+v)", trials, f.Entries)
	}
	if trials[0] != "output_trimmer" {
		t.Errorf("the list order decides who goes first: %v", trials)
	}
	other := f.Entries["hook_read_limit"]
	if other.State == config.AutopilotProbation || other.State == config.AutopilotOn {
		t.Errorf("the second candidate must wait: %+v", other)
	}
	if !strings.Contains(other.Reason, "waiting for output_trimmer") && other.State != config.AutopilotCandidate {
		t.Logf("second candidate: %+v", other)
	}
	_ = technique.CapMCPServer
}

// The cold-resume guard changes what the user sees, not what is sent: no lossless policy may name it (decided
// 2026-11-23, docs/hooks.md). Only the optimize policy does, as a direct guard (2026-11-24, TestDecideDirect).
func TestNoPolicyPromotesTheColdResumeGuard(t *testing.T) {
	for _, p := range []Policy{DefaultPolicy(), PolicyV2(), PolicyV3(), TrialPolicy()} {
		for _, n := range p.Names() {
			if n == config.OptimizerHookColdResume {
				t.Errorf("policy %s names %s", p.Name, n)
			}
		}
	}
}

// Neither rebuild guard may be named by a lossless policy either; the optimize policy names both as direct guards.
func TestNoPolicyPromotesTheRebuildGuards(t *testing.T) {
	for _, p := range []Policy{DefaultPolicy(), PolicyV2(), PolicyV3(), TrialPolicy()} {
		for _, n := range p.Names() {
			if n == config.OptimizerHookModelSwitch || n == config.OptimizerHookColdCompact {
				t.Errorf("policy %s names %s", p.Name, n)
			}
		}
	}
}

// The routine steer is T2 with no benchmark verdict: no lossless policy may name it; the optimize policy holds it in
// shadow (TestNothingBeyondT1LeavesShadowWithoutAVerdict), and only the user starts its trial.
func TestNoPolicyPromotesTheRoutineSteer(t *testing.T) {
	for _, p := range []Policy{DefaultPolicy(), PolicyV2(), PolicyV3(), TrialPolicy()} {
		for _, n := range p.Names() {
			if n == config.OptimizerHookRoutineSteer {
				t.Errorf("policy %s names %s", p.Name, n)
			}
		}
	}
}

// The code-write nudges are T2 with no benchmark verdict: no lossless policy may name them; the optimize policy keeps
// them in shadow only. Only the user turns them on.
func TestNoPolicyPromotesTheCodeWriteNudges(t *testing.T) {
	for _, p := range []Policy{DefaultPolicy(), PolicyV2(), PolicyV3(), TrialPolicy()} {
		for _, n := range p.Names() {
			if n == config.OptimizerHookWriteToEdit || n == config.OptimizerHookEditBatch {
				t.Errorf("policy %s names %s", p.Name, n)
			}
		}
	}
}
