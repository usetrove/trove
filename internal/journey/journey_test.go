package journey

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/optimize"
)

func TestMedianAndQuartiles(t *testing.T) {
	cases := []struct {
		name        string
		xs          []float64
		med, q1, q3 float64
	}{
		{"empty", nil, 0, 0, 0},
		{"single", []float64{4}, 4, 4, 4},
		{"odd", []float64{1, 2, 3, 4, 5}, 3, 2, 4},
		{"even", []float64{1, 2, 3, 4}, 2.5, 1.75, 3.25},
		{"unsorted", []float64{5, 1, 4, 2, 3}, 3, 2, 4},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := append([]float64(nil), c.xs...)
			if got := Median(in); math.Abs(got-c.med) > 1e-9 {
				t.Errorf("median = %v, want %v", got, c.med)
			}
			q1, q3 := Quartiles(in)
			if math.Abs(q1-c.q1) > 1e-9 || math.Abs(q3-c.q3) > 1e-9 {
				t.Errorf("quartiles = %v, %v; want %v, %v", q1, q3, c.q1, c.q3)
			}
			for i := range in {
				if in[i] != c.xs[i] {
					t.Fatalf("input was modified")
				}
			}
		})
	}
}

func TestMedianCIIsSeededAndBrackets(t *testing.T) {
	xs := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	lo1, hi1 := MedianCI(xs, rand.New(rand.NewPCG(1, 2)))
	lo2, hi2 := MedianCI(xs, rand.New(rand.NewPCG(1, 2)))
	if lo1 != lo2 || hi1 != hi2 {
		t.Fatalf("same seed gave different intervals: [%v,%v] vs [%v,%v]", lo1, hi1, lo2, hi2)
	}
	if lo1 > Median(xs) || hi1 < Median(xs) {
		t.Errorf("interval [%v,%v] does not bracket the median %v", lo1, hi1, Median(xs))
	}
	lo, hi := MedianCI([]float64{7, 7, 7, 7}, rand.New(rand.NewPCG(1, 2)))
	if lo != 7 || hi != 7 {
		t.Errorf("constant data interval = [%v,%v], want [7,7]", lo, hi)
	}
}

func TestWithinNoise(t *testing.T) {
	if !Within(-1, 2) || Within(1, 2) || Within(-3, -1) {
		t.Fatal("Within must be true only when the interval includes zero")
	}
}

func TestProfilesAreWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Profiles() {
		if seen[p.ID] {
			t.Errorf("duplicate profile id %q", p.ID)
		}
		seen[p.ID] = true
		if p.Sessions <= 0 || p.TurnsMin <= 0 || p.TurnsMax < p.TurnsMin {
			t.Errorf("%s: bad session or turn bounds", p.ID)
		}
		total := 0
		for _, k := range Kinds {
			total += p.Mix[k]
		}
		if total == 0 {
			t.Errorf("%s: empty output mix", p.ID)
		}
		ok, why := p.Measurable()
		if !ok && why == "" {
			t.Errorf("%s: not measurable without a reason", p.ID)
		}
	}
}

// Subscription and Codex users must never be measured through the gateway.
// Subscription users are measured through the hooks, in tokens only, and never
// through the gateway. Codex users (settings autopilot) are not measurable.
func TestSubscriptionUsesHooksAndCodexIsNotMeasured(t *testing.T) {
	for _, p := range Profiles() {
		switch {
		case p.Billing == "subscription":
			if p.Model() != ModelHooks {
				t.Errorf("%s: subscription must use the hooks model, got %s", p.ID, p.Model())
			}
		case p.Adapter == AdapterSettings:
			if p.Model() != ModelNone {
				t.Errorf("%s: codex must not be measured, got %s", p.ID, p.Model())
			}
		}
		if p.Billing == "subscription" && p.Model() == ModelGateway {
			t.Errorf("%s: subscription traffic must never use the gateway model", p.ID)
		}
	}
}

func TestBodiesAreValidJSONAndKeepStructure(t *testing.T) {
	p := Profiles()[1] // daily-pro
	drv := newDriver(p, nil, 9000)
	sess := genSession(p, 20261108, 0)
	for _, withTrove := range []bool{false, true} {
		bodies := drv.bodies(sess.Turns, simModels[0], withTrove)
		if len(bodies) != len(sess.Turns) {
			t.Fatalf("got %d bodies for %d turns", len(bodies), len(sess.Turns))
		}
		for k, b := range bodies {
			var v any
			if err := json.Unmarshal(b, &v); err != nil {
				t.Fatalf("trove=%v request %d is not valid JSON: %v", withTrove, k, err)
			}
			if err := checkStructure(b, ids(sess.Turns[:k])); err != nil {
				t.Fatalf("trove=%v request %d: %v", withTrove, k, err)
			}
		}
	}
}

// The engine must accept the synthetic bodies. A body it rejects is forwarded
// unchanged and silently produces no savings, which would hide a bug.
func TestEngineAcceptsSyntheticBodies(t *testing.T) {
	p := Profiles()[1]
	drv := newDriver(p, nil, 9000)
	sess := genSession(p, 20261108, 3)
	eng := optimize.New(optimize.Options{Config: drv.cfg, Getenv: func(string) string { return "" }, Recovery: newMemStore()})
	for k, b := range drv.bodies(sess.Turns, simModels[0], true) {
		res := eng.TransformRequest(context.Background(), optimize.RequestIn{
			Provider: "anthropic", Path: "/v1/messages", Header: http.Header{}, Body: b,
		}, optimize.Session{Key: "t", Agent: "claude_code", TroveRun: true})
		for _, f := range res.Failures {
			if f.Class == "malformed" {
				t.Fatalf("request %d rejected as malformed", k)
			}
		}
	}
}

func TestRepeatReadsAreDuplicates(t *testing.T) {
	p := Profiles()[0]
	p.RepeatRead = 100
	p.Mix = map[Kind]int{KindReadSmall: 1}
	turns := genTurns(p, newRand(7, "x"), 10, 10)
	repeats := 0
	for _, tu := range turns[1:] {
		if tu.Repeat {
			repeats++
		}
	}
	if repeats == 0 {
		t.Fatal("expected repeat reads with RepeatRead=100")
	}
}

func TestRunIsDeterministic(t *testing.T) {
	opts := Options{Seed: 20261108, Weeks: 1, Profiles: []string{"light"}, MCPSchemaBytes: 9000}
	a, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Fatal("same seed produced different output")
	}
}

func TestSeedChangesOutput(t *testing.T) {
	a, _ := Run(Options{Seed: 1, Weeks: 1, Profiles: []string{"light"}, MCPSchemaBytes: 9000})
	b, _ := Run(Options{Seed: 2, Weeks: 1, Profiles: []string{"light"}, MCPSchemaBytes: 9000})
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) == string(jb) {
		t.Fatal("different seeds produced identical output")
	}
}

// Every profile is listed, including the ones that cannot be measured. Nothing
// may be dropped after a run. The measurable profiles are covered by the
// determinism test; this one only checks the reporting shape, so it runs the
// two not-measurable profiles plus one short measurable profile.
func TestEveryProfileIsReported(t *testing.T) {
	res, err := Run(Options{Seed: 20261108, Weeks: 1, Profiles: []string{"codex", "subscription-pro", "light"}, MCPSchemaBytes: 9000})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Profiles) != 3 {
		t.Fatalf("reported %d profiles, want 3", len(res.Profiles))
	}
	for _, p := range res.Profiles {
		if !p.Measurable {
			if p.Reason == "" {
				t.Errorf("%s is missing its not-measurable reason", p.ID)
			}
			continue
		}
		if p.Model == string(ModelHooks) {
			if p.Hook == nil || len(p.Cost) != 0 {
				t.Errorf("%s (hooks) must report tokens only: hook=%v cost rows=%d", p.ID, p.Hook != nil, len(p.Cost))
			}
			continue
		}
		if len(p.Cost) != len(simModels) {
			t.Errorf("%s has %d cost rows, want %d", p.ID, len(p.Cost), len(simModels))
		}
	}
}

func TestUnknownProfileIsAnError(t *testing.T) {
	_, err := Run(Options{Seed: 1, Profiles: []string{"nope"}})
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("want an error naming the profile, got %v", err)
	}
}

func TestMarkdownLabelsEstimatesAndKeepsLiveSeparate(t *testing.T) {
	res, err := Run(Options{Seed: 20261108, Weeks: 1, Profiles: []string{"light", "codex"}, MCPSchemaBytes: 9000})
	if err != nil {
		t.Fatal(err)
	}
	md := Markdown(res)
	for _, want := range []string{
		"## Estimated savings (offline tier)",
		"**Label: estimated.**",
		"## Measured savings (live tier)",
		"**Label: measured.** Not run yet.",
		"Not measured by this tier",
		"2026-10-28.3",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown is missing %q", want)
		}
	}
	if strings.Contains(md, "Label: measured.** ") && !strings.Contains(md, "Not run yet") {
		t.Error("a measured label must not appear without the not-run notice")
	}
}

func TestOutputHasNoPathsOrKeys(t *testing.T) {
	res, _ := Run(Options{Seed: 20261108, Weeks: 1, Profiles: []string{"light"}, MCPSchemaBytes: 9000})
	b, _ := json.Marshal(res)
	md := Markdown(res)
	for _, bad := range []string{"C:\\", "/home/", "/Users/", "sk-ant", "ANTHROPIC_API_KEY"} {
		if strings.Contains(string(b), bad) || strings.Contains(md, bad) {
			t.Errorf("output contains %q", bad)
		}
	}
}

func ids(ts []Turn) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.ToolUseID)
	}
	return out
}

// The hooks model must deny real repeat reads against the fixture files, shrink the
// context, and never produce a dollar figure.
func TestHookArmDeniesRepeatReadsAndReportsTokensOnly(t *testing.T) {
	p := Profiles()[len(Profiles())-2] // subscription-pro
	if p.Model() != ModelHooks {
		t.Fatalf("test profile %s is not a hooks profile", p.ID)
	}
	p.Mix = map[Kind]int{KindReadSmall: 1}
	p.RepeatRead = 80
	drv := newDriver(p, nil, 0) // denials in isolation; the MCP overhead is tested separately
	sess := genSession(p, 20261108, 0)
	off := drv.runHookOff(sess.Turns)
	on, err := drv.runHookArm(sess.Turns, 0, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if on.Denied == 0 {
		t.Fatalf("expected repeat reads to be denied; repeats in the session: %d", countRepeats(sess.Turns))
	}
	if on.ContextTokens >= off.ContextTokens {
		t.Errorf("context should shrink when reads are denied: off=%d on=%d", off.ContextTokens, on.ContextTokens)
	}
	if len(on.CostUSD) != 0 || len(off.CostUSD) != 0 {
		t.Error("the hooks model must not produce dollar figures")
	}
	if !on.Checks.Structure {
		t.Error("structure check failed on the hooks arm")
	}
}

func countRepeats(ts []Turn) int {
	n := 0
	for _, t := range ts {
		if t.Repeat {
			n++
		}
	}
	return n
}

// The committed holdout targets must pass the gate. This is the check that the
// generator reproduces the session logs it was not fit to.
func TestCalibrationGatePassesOnCommittedHoldout(t *testing.T) {
	tg, err := CommittedTargets()
	if err != nil {
		t.Fatal(err)
	}
	st := CheckCalibration(tg, DefaultTolerance)
	if !st.Pass {
		t.Fatalf("calibration gate failed on the committed holdout: %+v", st.Checks)
	}
}

// The gate must be able to fail: a target far from the generator is rejected, and
// the failing tool is named.
func TestCalibrationGateFailsOutsideTolerance(t *testing.T) {
	tg, err := CommittedTargets()
	if err != nil {
		t.Fatal(err)
	}
	bash := tg.Holdout["Bash"]
	bash.MedianB *= 3
	tg.Holdout["Bash"] = bash
	st := CheckCalibration(tg, DefaultTolerance)
	if st.Pass {
		t.Fatal("a target three times the generator's median must fail the gate")
	}
	var named bool
	for _, c := range st.Checks {
		if c.Tool == "Bash" && !c.Pass && strings.Contains(c.Why, "median") {
			named = true
		}
	}
	if !named {
		t.Errorf("the failing tool and measure should be named: %+v", st.Checks)
	}
}

// With the gate failed, the report must show no savings table at all.
func TestMarkdownWithholdsSavingsWhenCalibrationFails(t *testing.T) {
	tg, err := CommittedTargets()
	if err != nil {
		t.Fatal(err)
	}
	bash := tg.Holdout["Bash"]
	bash.MedianB *= 3
	tg.Holdout["Bash"] = bash
	res, err := Run(Options{Seed: 20261108, Weeks: 1, Profiles: []string{"light"}, MCPSchemaBytes: 9000})
	if err != nil {
		t.Fatal(err)
	}
	res.Calibration = CheckCalibration(tg, DefaultTolerance)
	md := Markdown(res)
	if !strings.Contains(md, "**Calibration: FAIL.**") {
		t.Error("the calibration status must print at the top")
	}
	for _, bad := range []string{"## Estimated savings", "Saved, median per session", "Median cost off"} {
		if strings.Contains(md, bad) {
			t.Errorf("a savings table (%q) was rendered while calibration failed", bad)
		}
	}
	if !strings.Contains(md, "Savings tables withheld") {
		t.Error("the report must say that savings are withheld")
	}
}

// With the gate passed, the calibration status is the first thing in the report.
func TestMarkdownPrintsCalibrationFirst(t *testing.T) {
	res, err := Run(Options{Seed: 20261108, Weeks: 1, Profiles: []string{"light"}, MCPSchemaBytes: 9000})
	if err != nil {
		t.Fatal(err)
	}
	md := Markdown(res)
	if !strings.HasPrefix(md, "**Calibration: PASS.**") {
		t.Errorf("the report must open with the calibration status, got: %.80q", md)
	}
}

// The MCP tool list is charged on every request of the Trove arm, so with it
// registered a small-read session can cost more. That is the honest answer, and
// this test pins that the overhead is counted.
func TestHookArmChargesMCPToolListOverhead(t *testing.T) {
	p := Profiles()[len(Profiles())-2]
	sess := genSession(p, 20261108, 0)
	without, err := newDriver(p, nil, 0).runHookArm(sess.Turns, 0, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	with, err := newDriver(p, nil, 9000).runHookArm(sess.Turns, 0, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if with.ContextTokens <= without.ContextTokens {
		t.Errorf("the MCP tool list must add context: with=%d without=%d", with.ContextTokens, without.ContextTokens)
	}
}

// Cache safety: a request's transformed bytes must still be the same bytes at the
// start of the next request's transformed body, or the provider's cache would be
// rewritten on every turn. Measured with a fresh engine per request, so it checks
// the transform itself and not any memo.
func TestCachedPrefixIsNotRewritten(t *testing.T) {
	p := Profiles()[2]
	drv := newDriver(p, nil, 9000)
	sess := genSession(p, 20261108, 0)
	bodies := drv.bodies(sess.Turns, simModels[0], true)
	var prev []byte

	for k, b := range bodies {
		eng := optimize.New(optimize.Options{Config: drv.cfg, Getenv: func(string) string { return "" }, Recovery: newMemStore()})
		res := eng.TransformRequest(context.Background(), optimize.RequestIn{Provider: "anthropic", Path: "/v1/messages", Header: http.Header{}, Body: b},
			optimize.Session{Key: fmt.Sprintf("p%d", k), Agent: "claude_code", TroveRun: true})
		if prev != nil {
			// Every request ends in JSON framing ("]}") that the next request
			// continues with a comma, so the last framing bytes always differ. That is
			// not cache content: caches key on content blocks. Anything beyond the
			// framing would be a real rewrite.
			const framing = 2
			lost := len(prev) - commonPrefix(prev, res.Body)
			if lost > framing {
				t.Fatalf("request %d rewrote %d bytes of the cached prefix (more than the %d framing bytes)", k, lost, framing)
			}
		}
		prev = res.Body
	}
}

// The guard switches off an optimizer only on enough data and only for a trigger.
func TestGuardWindowDisablesOnlyTheTrippedOptimizer(t *testing.T) {
	var recs []SessionRecord
	for i := 0; i < 4; i++ {
		on := newArm()
		on.Requests = 10
		on.Applied = map[string]int64{"noise_normalizer": 500, "output_trimmer": 100}
		// Every output_trimmer request failed open; noise_normalizer never did.
		on.FailOpenBy = map[string]int{"output_trimmer": 10}
		recs = append(recs, SessionRecord{Index: i, On: on})
	}
	got := guardWindow(recs, len(recs))
	if len(got) != 1 || got[0].Optimizer != "output_trimmer" {
		t.Fatalf("only output_trimmer should be switched off, got %+v", got)
	}
	if got[0].Reason == "" || got[0].FailOpenRate < 0.9 {
		t.Errorf("the decision must carry its reason and rate: %+v", got[0])
	}
}

// Requests that fell back still carry Trove's tool list, so the overhead counts them.
func TestFallbackRequestsAreChargedTheToolList(t *testing.T) {
	res, err := Run(Options{Seed: 20261108, Weeks: 1, Profiles: []string{"light"}, MCPSchemaBytes: 9000})
	if err != nil {
		t.Fatal(err)
	}
	p := res.Profiles[0]
	if p.FallbackOverheadTokens > p.OverheadTokens {
		t.Fatalf("fallback overhead %d cannot exceed total overhead %d", p.FallbackOverheadTokens, p.OverheadTokens)
	}
	var fallbacks int
	for _, s := range p.Sessions {
		fallbacks += s.On.FailOpen
	}
	if want := int64(fallbacks) * tokens(9000); p.FallbackOverheadTokens != want {
		t.Errorf("fallback overhead = %d, want %d (%d fallback requests x tool list)", p.FallbackOverheadTokens, want, fallbacks)
	}
}

// The base prompt is a parameter: a larger one puts more tokens at the start of every request.
func TestBasePromptIsAParameter(t *testing.T) {
	p := Profiles()[1]
	small := newDriverWith(p, nil, 0, 6000)
	large := newDriverWith(p, nil, 0, DefaultSystemTokens)
	turns := genSession(p, 20261108, 0).Turns[:1]
	a := tokens(len(small.bodies(turns, simModels[0], false)[0]))
	b := tokens(len(large.bodies(turns, simModels[0], false)[0]))
	if b-a < 10000 {
		t.Fatalf("a %d-token base prompt should add about 13,000 tokens over 6,000; added %d", DefaultSystemTokens, b-a)
	}
}

// Every planned session is kept, with or without the guard. A dropped session
// would make N and the data disagree.
func TestEveryPlannedSessionIsRun(t *testing.T) {
	res, err := Run(Options{Seed: 20261108, Weeks: 2, Profiles: []string{"light"}, MCPSchemaBytes: 9000})
	if err != nil {
		t.Fatal(err)
	}
	p := res.Profiles[0]
	if len(p.Sessions) != p.N || p.N != Profiles()[0].Sessions*2 {
		t.Fatalf("ran %d sessions, planned %d", len(p.Sessions), p.N)
	}
}
