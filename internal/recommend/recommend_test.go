package recommend

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/experiment"
	"github.com/usetrove/trove/internal/features"
	"github.com/usetrove/trove/internal/hooks"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/settings"
)

func TestBundledDefaultsAreValidAndReferenceKnownNames(t *testing.T) {
	d, err := LoadDefaults(embeddedDefaults)
	if err != nil {
		t.Fatalf("the bundled defaults file does not load: %v", err)
	}
	if d.Version != SupportedVersion || d.Release == "" || len(d.Rules) == 0 {
		t.Fatalf("defaults = version %d release %q, %d rules", d.Version, d.Release, len(d.Rules))
	}
	cat, err := settings.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	ps, err := settings.BuiltinProfiles(cat)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range d.Rules {
		switch r.Kind {
		case KindOptimizer:
			if !contains(hooks.OptimizerNames, r.Name) {
				t.Errorf("rule %s names optimizer %q, which no adapter implements", r.ID, r.Name)
			}
		case KindProfile:
			if _, ok := ps.Get(r.Name); !ok {
				t.Errorf("rule %s names settings profile %q, which does not exist", r.ID, r.Name)
			}
		}
		if r.Evidence == EvidenceNone && r.Source != "" {
			t.Errorf("rule %s cites a source but evidence is none", r.ID)
		}
	}
}

func TestLoadDefaultsRejectsBadRules(t *testing.T) {
	base := "version = 1\n[[rule]]\nid = \"a\"\nkind = \"optimizer\"\nname = \"output_trimmer\"\naction = \"shadow\"\nevidence = \"none\"\nreason = \"r\"\n"
	cases := map[string]string{
		"enable from a table":       strings.Replace(base, `"shadow"`, `"enable"`, 1),
		"benchmark without source":  strings.Replace(base, `"none"`, `"benchmark"`, 1),
		"unknown evidence":          strings.Replace(base, `"none"`, `"folklore"`, 1),
		"no reason":                 strings.Replace(base, `reason = "r"`, `reason = ""`, 1),
		"duplicate id":              base + "[[rule]]\nid = \"a\"\nkind = \"optimizer\"\nname = \"x\"\naction = \"shadow\"\nevidence = \"none\"\nreason = \"r\"\n",
		"newer schema":              strings.Replace(base, "version = 1", "version = 99", 1),
		"unknown kind":              strings.Replace(base, `"optimizer"`, `"magic"`, 1),
		"profile with wrong action": strings.Replace(strings.Replace(base, `"optimizer"`, `"profile"`, 1), `"shadow"`, `"shadow"`, 1),
	}
	for name, in := range cases {
		if _, err := LoadDefaults([]byte(in)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if d, err := LoadDefaults(nil); err != nil || len(d.Rules) != 0 || d.MinSessions != 3 {
		t.Errorf("empty input should mean no rules: %+v %v", d, err)
	}
	if _, err := LoadDefaults([]byte(base)); err != nil {
		t.Errorf("the base case should load: %v", err)
	}
}

func repoOf(size features.SizeBucket, fw features.TestFramework) *features.Repo {
	return &features.Repo{Size: size, TestFramework: fw, LangMix: []features.LangShare{{Lang: features.FileGo, Pct: 100}}}
}

func names(rs []Recommendation) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Name+":"+string(r.Action)+":"+string(r.Confidence))
	}
	return out
}

func TestLocalRules(t *testing.T) {
	busy := Aggregate{Sessions: 6, FilesRead: 30, RepeatReads: 3, LargeResults: 4, DupRatio: 0.4, ToolResults: 20}
	tests := []struct {
		name string
		f    Facts
		want []string
	}{
		{"nothing known gives nothing", Facts{}, nil},
		{"too few sessions", Facts{Sessions: Aggregate{Sessions: 2, LargeResults: 9, RepeatReads: 9}}, nil},
		{"busy repo gets shadow suggestions, low confidence without evidence", Facts{Repo: repoOf("L", "go_test"), Sessions: busy},
			[]string{"hook_large_output:shadow:low", "hook_quiet_commands:shadow:low", "hook_read_limit:shadow:low",
				"hook_repeat_read:shadow:low", "noise_normalizer:shadow:low", "output_trimmer:shadow:low"}},
		{"small repo without a test framework skips test rules", Facts{Repo: repoOf("S", "unknown"), Sessions: Aggregate{Sessions: 6, LargeResults: 4}},
			[]string{"output_trimmer:shadow:low"}},
		{"already in shadow or on: no rule of thumb", Facts{Repo: repoOf("L", "go_test"), Sessions: busy,
			States: map[string]string{"output_trimmer": "on", "hook_repeat_read": "shadow", "hook_large_output": "shadow",
				"hook_quiet_commands": "on", "hook_read_limit": "on", "noise_normalizer": "on"}}, nil},
		{"rolled back is never suggested", Facts{Repo: repoOf("L", "go_test"), Sessions: Aggregate{Sessions: 6, LargeResults: 4}, RolledBack: []string{"output_trimmer", "hook_large_output", "hook_quiet_commands"}}, nil},
		{"task gives the profile", Facts{Task: "quick-fix"}, []string{"quick-fix:use_profile:low"}},
		{"unknown task gives none", Facts{Task: "poetry"}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NewLocal().Recommend(context.Background(), tc.f)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(names(got), tc.want) {
				t.Errorf("got  %v\nwant %v", names(got), tc.want)
			}
			for _, r := range got {
				if r.Reason == "" || strings.Contains(r.Reason, "{") || r.Label != "estimated" || r.Source != "local" {
					t.Errorf("recommendation incomplete: %+v", r)
				}
			}
		})
	}
}

func saves(opt string, conf string) experiment.Result {
	return experiment.Result{Optimizer: opt, Status: experiment.Saves, NOn: 20, NOff: 20,
		Cost: &experiment.MetricResult{Metric: experiment.MetricCost, Kind: "relative", Value: -0.12, Low: -0.2, High: -0.05,
			Label: measure.Measured, Confidence: conf}}
}

func TestLocalUsesTheUsersOwnExperiments(t *testing.T) {
	busy := Aggregate{Sessions: 9, LargeResults: 5}
	f := Facts{Repo: repoOf("M", "unknown"), Sessions: busy, Experiments: []experiment.Result{saves("output_trimmer", "high")},
		States: map[string]string{"output_trimmer": "shadow"}}
	got, _ := NewLocal().Recommend(context.Background(), f)
	if len(got) != 1 || got[0].Action != ActionEnable || got[0].Confidence != High || got[0].Evidence != EvidenceLocal || got[0].Label != "measured" {
		t.Fatalf("got %+v", got)
	}
	if !strings.Contains(got[0].Reason, "12.0% less") || !strings.Contains(got[0].Reason, "20 sessions") {
		t.Errorf("reason should carry the measured figures: %s", got[0].Reason)
	}

	f.States["output_trimmer"] = "on"
	if got, _ := NewLocal().Recommend(context.Background(), f); len(got) != 0 {
		t.Errorf("already on and saving: nothing to say, got %+v", got)
	}

	worse := experiment.Result{Optimizer: "output_trimmer", Status: experiment.WorseOutcomes, NOn: 15, NOff: 15}
	f = Facts{Sessions: busy, Repo: repoOf("M", "unknown"), Experiments: []experiment.Result{worse}, States: map[string]string{"output_trimmer": "on"}}
	got, _ = NewLocal().Recommend(context.Background(), f)
	if len(got) != 1 || got[0].Action != ActionAvoid || got[0].Confidence != Low {
		t.Errorf("a loss on your own data means avoid it, got %+v", got)
	}
	f.States["output_trimmer"] = "off"
	got, _ = NewLocal().Recommend(context.Background(), f)
	if len(got) != 0 {
		t.Errorf("a loss while off must also block the rule of thumb, got %+v", got)
	}

	// A "saves" result whose status was rolled back elsewhere must not be enabled.
	f = Facts{Experiments: []experiment.Result{saves("output_trimmer", "high")}, RolledBack: []string{"output_trimmer"}}
	if got, _ := NewLocal().Recommend(context.Background(), f); len(got) != 0 {
		t.Errorf("got %+v", got)
	}
}

func TestBenchmarkEvidenceRaisesConfidenceOnlyWithEnoughSessions(t *testing.T) {
	d, err := LoadDefaults([]byte(`version = 1
min_sessions = 3
[[rule]]
id = "t"
kind = "optimizer"
name = "output_trimmer"
action = "shadow"
evidence = "benchmark"
source = "bench-run-42"
reason = "{sessions} sessions"
[rule.when]
min_large_results = 1
`))
	if err != nil {
		t.Fatal(err)
	}
	l := Local{Defaults: d}
	for sessions, want := range map[int]Confidence{3: Low, 4: Low, 5: Medium, 20: Medium} {
		got, _ := l.Recommend(context.Background(), Facts{Sessions: Aggregate{Sessions: sessions, LargeResults: 2}})
		if len(got) != 1 || got[0].Confidence != want || got[0].Evidence != EvidenceBenchmark {
			t.Errorf("%d sessions: got %+v, want %s", sessions, got, want)
		}
	}
}

func TestLocalIsDeterministic(t *testing.T) {
	f := Facts{Repo: repoOf("L", "go_test"), Sessions: Aggregate{Sessions: 8, FilesRead: 40, RepeatReads: 4, LargeResults: 6, DupRatio: 0.5, ToolResults: 30}, Task: "refactor"}
	a, _ := NewLocal().Recommend(context.Background(), f)
	for i := 0; i < 20; i++ {
		b, _ := NewLocal().Recommend(context.Background(), f)
		if !reflect.DeepEqual(a, b) {
			t.Fatal("the same facts gave a different list")
		}
	}
}

type fake struct {
	name string
	recs []Recommendation
	err  error
}

func (f fake) Name() string { return f.name }
func (f fake) Recommend(context.Context, Facts) ([]Recommendation, error) {
	return f.recs, f.err
}

func TestComposePrioritySkipsFailuresAndKeepsGoing(t *testing.T) {
	hosted := fake{name: "hosted", recs: []Recommendation{
		{Kind: KindOptimizer, Name: "output_trimmer", Action: ActionShadow, Confidence: High, Reason: "hosted"},
	}}
	local := fake{name: "local", recs: []Recommendation{
		{Kind: KindOptimizer, Name: "output_trimmer", Action: ActionShadow, Confidence: Low, Reason: "local"},
		{Kind: KindOptimizer, Name: "hook_repeat_read", Action: ActionShadow, Confidence: Low, Reason: "local"},
	}}
	got, err := Compose(context.Background(), Facts{}, hosted, local)
	if err != nil || len(got) != 2 || got[0].Reason != "hosted" || got[0].Source != "hosted" || got[1].Name != "hook_repeat_read" {
		t.Errorf("got %+v err=%v", got, err)
	}
	broken := fake{name: "hosted", err: errors.New("offline")}
	got, err = Compose(context.Background(), Facts{}, broken, local)
	if len(got) != 2 || err == nil || !strings.Contains(err.Error(), "offline") {
		t.Errorf("a failing provider must not stop the others: got %d recs, err=%v", len(got), err)
	}
}

// TestRecommendationsCarryNoContent pins that the numeric inputs have no text
// field that could carry content, and that recommendations are built only from
// numbers and fixed names.
func TestRecommendationsCarryNoContent(t *testing.T) {
	ty := reflect.TypeOf(Aggregate{})
	for i := 0; i < ty.NumField(); i++ {
		if k := ty.Field(i).Type.Kind(); k != reflect.Int && k != reflect.Float64 {
			t.Errorf("Aggregate.%s has kind %s; it may hold numbers only", ty.Field(i).Name, k)
		}
	}
	f := Facts{Repo: repoOf("L", "go_test"), Sessions: Aggregate{Sessions: 9, LargeResults: 9, RepeatReads: 9},
		Task: "quick-fix", States: map[string]string{}}
	recs, _ := NewLocal().Recommend(context.Background(), f)
	if len(recs) == 0 {
		t.Fatal("expected recommendations")
	}
	for _, r := range recs {
		for _, w := range strings.Fields(r.Reason) {
			if strings.ContainsAny(w, "/\\") && !strings.HasPrefix(w, "`") {
				t.Errorf("reason mentions something path-like %q: %s", w, r.Reason)
			}
		}
	}
	if _, err := json.Marshal(recs); err != nil {
		t.Fatal(err)
	}
}

func TestSummarize(t *testing.T) {
	got := Summarize([]features.SessionFeatures{
		{FilesRead: 10, LargeResults: 2, AvgDupRatio: 0.2},
		{FilesRead: 20, LargeResults: 4, AvgDupRatio: 0.4},
	})
	if got.Sessions != 2 || got.FilesRead != 15 || got.LargeResults != 3 || got.DupRatio < 0.299 || got.DupRatio > 0.301 {
		t.Errorf("got %+v", got)
	}
	if (Summarize(nil) != Aggregate{}) {
		t.Error("no sessions should give a zero aggregate")
	}
}
