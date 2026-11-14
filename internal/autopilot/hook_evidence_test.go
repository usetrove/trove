package autopilot

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/hookcaps"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/store"
)

var hookNow = time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)

func setInstall(t *testing.T, db *store.DB, daysAgo int) {
	t.Helper()
	if _, err := db.Exec(`INSERT OR REPLACE INTO settings(key, value) VALUES('measure.install_at', ?)`,
		fmt.Sprint(hookNow.AddDate(0, 0, -daysAgo).UnixMilli())); err != nil {
		t.Fatal(err)
	}
}

// hookSessions records n sessions in which an optimizer would have rewritten twice, tokens each time.
func hookSessions(t *testing.T, db *store.DB, optimizer string, n int, tokens int64) {
	t.Helper()
	for i := 0; i < n; i++ {
		for j := 0; j < 2; j++ {
			ts := hookNow.AddDate(0, 0, -1-i%9).Add(time.Duration(j) * time.Minute)
			if _, err := db.Exec(`INSERT INTO hook_events(ts, session_key, hook, kind, bytes, tokens) VALUES(?,?,?,?,?,?)`,
				ts.UnixMilli(), fmt.Sprintf("h%s%02d", optimizer[:3], i), optimizer, "would_rewrite", tokens*4, tokens); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// gatewaySessions records n gateway sessions, each with one request that carries a shadow figure.
func gatewaySessions(t *testing.T, db *store.DB, optimizer string, n int, tokens int64) {
	t.Helper()
	for i := 0; i < n; i++ {
		ts := hookNow.AddDate(0, 0, -1-i%9)
		key := fmt.Sprintf("g%s%02d", optimizer[:3], i)
		shadow := fmt.Sprintf(`{"label":"estimated","optimizers":{%q:{"tokens":%d,"usd":0.01}}}`, optimizer, tokens)
		if _, err := db.Exec(`INSERT INTO requests(ts, session_key, agent, provider, model, status_code, shadow_savings) VALUES(?,?,?,?,?,200,?)`,
			ts.UnixMilli(), key, "claude_code", "anthropic", "m", shadow); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO sessions(session_key, agent, started_at, ended_at, request_count) VALUES(?,?,?,?,1)`,
			key, "claude_code", ts.UnixMilli(), ts.UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
}

func planOf(f config.AutopilotFile, name string) config.AutopilotPlanItem {
	for _, it := range f.Plan {
		if it.Optimizer == name {
			return it
		}
	}
	return config.AutopilotPlanItem{}
}

// The case this exists for: no gateway at all, only the plugin's hooks. Autopilot has to be able to
// take noise_normalizer from shadow to on from the hooks' own evidence, and has to say plainly that the
// duplicate collapser, which needs the gateway, cannot run here.
func TestAHooksOnlyUserIsPromoted(t *testing.T) {
	db, dir := open(t)
	ctx := context.Background()
	setInstall(t, db, 30)
	hookSessions(t, db, "noise_normalizer", 25, 200) // 25 sessions, 10,000 estimated tokens
	probed(t, dir)                                   // the hooks only act where a probe has seen the rewrite work
	if _, err := Enable(dir, DefaultPolicy(), hookNow.AddDate(0, 0, -30)); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	var states []string
	for i := 0; i < 3; i++ {
		res, err := Advance(ctx, db, dir, cfg, DefaultPolicy(), hookNow.Add(time.Duration(i)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		states = append(states, res.File.Entries["noise_normalizer"].State)
	}
	want := []string{config.AutopilotCandidate, config.AutopilotOn, config.AutopilotOn}
	for i := range want {
		if states[i] != want[i] {
			t.Fatalf("noise_normalizer went %v, want %v (it needs evidence, then a second check)", states, want)
		}
	}
	got, err := config.Load(dir, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if got.OptimizerState("noise_normalizer") != config.StateOn {
		t.Error("the promotion did not reach the configuration the hooks read")
	}

	f, _ := config.LoadAutopilot(dir)
	dup := f.Entries["duplicate_result_collapser"]
	if dup.State != config.AutopilotShadow || !strings.Contains(dup.Reason, "not available in your setup") || !strings.Contains(dup.Reason, "needs the gateway") {
		t.Errorf("the collapser should be held back as unavailable without a gateway: %+v", dup)
	}
	if v := planOf(f, "duplicate_result_collapser"); v.Verdict != VerdictUnavailable {
		t.Errorf("planner verdict %q, want unavailable", v.Verdict)
	}
}

// Sessions for the baseline come from both channels: without that, a person with no gateway is in
// the baseline for ever, because the gateway's table of sessions is empty.
func TestTheBaselineCountsHookSessions(t *testing.T) {
	cases := map[string]struct {
		sessions int
		days     int
		done     bool
	}{
		"enough sessions and days": {25, 30, true},
		"too few sessions":         {5, 30, false},
		"too few days":             {25, 2, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			db, dir := open(t)
			setInstall(t, db, c.days)
			probed(t, dir)
			hookSessions(t, db, "noise_normalizer", c.sessions, 500)
			Enable(dir, DefaultPolicy(), hookNow.AddDate(0, 0, -c.days))
			res, err := Advance(context.Background(), db, dir, config.Default(), DefaultPolicy(), hookNow)
			if err != nil {
				t.Fatal(err)
			}
			e := res.File.Entries["noise_normalizer"]
			if c.done {
				if e.State != config.AutopilotCandidate {
					t.Errorf("state %q, want candidate", e.State)
				}
				return
			}
			if e.State != config.AutopilotShadow || !strings.Contains(e.Reason, "baseline") {
				t.Errorf("state %q reason %q; want shadow and a baseline reason", e.State, e.Reason)
			}
		})
	}
}

// When both channels saw the same waste, the evidence is the larger figure, never the sum.
func TestEvidenceFromBothChannelsIsNotAdded(t *testing.T) {
	db, dir := open(t)
	setInstall(t, db, 30)
	gatewaySessions(t, db, "noise_normalizer", 12, 250) // 3,000 tokens
	hookSessions(t, db, "noise_normalizer", 12, 200)    // 12 sessions x 2 events x 200 = 4,800 tokens
	Enable(dir, DefaultPolicy(), hookNow.AddDate(0, 0, -30))
	res, err := Advance(context.Background(), db, dir, config.Default(), DefaultPolicy(), hookNow)
	if err != nil {
		t.Fatal(err)
	}
	it := planOf(res.File, "noise_normalizer")
	if it.NetTokens != 4800 || it.Sessions != 12 {
		t.Errorf("plan %+v: want the larger channel's 4800 tokens over 12 sessions, not 7800 or 24", it)
	}
	// With both channels in use, neither optimizer is unavailable.
	for _, n := range []string{"noise_normalizer", "duplicate_result_collapser"} {
		if v := planOf(res.File, n); v.Verdict == VerdictUnavailable {
			t.Errorf("%s was called unavailable although both channels are in use: %+v", n, v)
		}
	}
}

func TestNothingSeenYetIsCollectingNotUnavailable(t *testing.T) {
	db, dir := open(t)
	Enable(dir, DefaultPolicy(), hookNow)
	res, err := Advance(context.Background(), db, dir, config.Default(), DefaultPolicy(), hookNow.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range DefaultPolicy().Eligible {
		e := res.File.Entries[n]
		if strings.Contains(e.Reason, "not available") {
			t.Errorf("%s called unavailable on a fresh install with no traffic: %q", n, e.Reason)
		}
		if v := planOf(res.File, n); v.Verdict == VerdictUnavailable {
			t.Errorf("%s: planner says unavailable with no traffic", n)
		}
	}
}

func TestUnavailableForReadsWhereEachTechniqueRuns(t *testing.T) {
	cases := []struct {
		name    string
		tr      measure.Traffic
		wantSub string // "" means available
	}{
		{"duplicate_result_collapser", measure.Traffic{Hooks: true}, "needs the gateway"},
		{"duplicate_result_collapser", measure.Traffic{Gateway: true}, ""},
		{"duplicate_result_collapser", measure.Traffic{Gateway: true, Hooks: true}, ""},
		{"noise_normalizer", measure.Traffic{Hooks: true}, ""}, // runs in the hooks too
		{"noise_normalizer", measure.Traffic{Gateway: true}, ""},
		{"hook_repeat_read", measure.Traffic{Gateway: true}, "plugin's hooks"},
		{"hook_repeat_read", measure.Traffic{Hooks: true}, ""},
		{"hook_repeat_read", measure.Traffic{}, ""},           // nothing seen yet: still collecting
		{"duplicate_result_collapser", measure.Traffic{}, ""}, // likewise
		{"not_a_technique", measure.Traffic{Hooks: true}, ""}, // unknown names are not blamed
	}
	for _, c := range cases {
		got := unavailableFor(c.name, c.tr)
		if (c.wantSub == "") != (got == "") || !strings.Contains(got, c.wantSub) {
			t.Errorf("%s with %+v: %q, want one containing %q", c.name, c.tr, got, c.wantSub)
		}
	}
}

func TestDecideHoldsAnUnavailableOptimizerBack(t *testing.T) {
	p := DefaultPolicy()
	ev := Evidence{BaselineDone: true, ShadowSessions: 50, NetTokens: 1e6, Unavailable: "it needs the gateway"}
	for _, state := range []string{config.AutopilotShadow, config.AutopilotCandidate} {
		d := p.Decide(config.AutopilotEntry{State: state, SinceMS: hookNow.UnixMilli()}, ev, hookNow)
		if d.Entry.State == config.AutopilotOn || d.Entry.State == config.AutopilotCandidate && state == config.AutopilotShadow {
			t.Errorf("%s with an unavailable optimizer -> %q", state, d.Entry.State)
		}
		if !strings.Contains(d.Entry.Reason, "not available in your setup: it needs the gateway") {
			t.Errorf("%s: reason %q", state, d.Entry.Reason)
		}
	}
}

func TestCombineTakesTheLargerOfEachFigure(t *testing.T) {
	gw := measure.ShadowRow{Sessions: 10, Tokens: measure.Figure{Value: 900}}
	hk := measure.HookShadow{Sessions: 7, Tokens: 1500}
	s, tok := combine(gw, hk)
	if s != 10 || tok != 1500 {
		t.Errorf("combine = %d sessions, %v tokens; want 10 and 1500 (each figure's larger value)", s, tok)
	}
	if s, tok := combine(measure.ShadowRow{}, measure.HookShadow{}); s != 0 || tok != 0 {
		t.Errorf("no evidence gave %d, %v", s, tok)
	}
	if got := combinedRows([]string{"a", "b"}, map[string]measure.ShadowRow{"a": gw}, nil); len(got) != 1 || got["a"].Tokens.Label != measure.Estimated {
		t.Errorf("combinedRows = %+v; want only the optimizer with evidence, labeled estimated", got)
	}
	if sessionsSeen(3, 25) != 25 || sessionsSeen(30, 2) != 30 {
		t.Error("sessionsSeen must take the larger count")
	}
}

// writeProbe stores a hook probe record where the hooks keep it.
func writeProbe(t *testing.T, dir string, c hookcaps.Capabilities) {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(hookcaps.Path(dir)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hookcaps.Path(dir), b, 0o600); err != nil {
		t.Fatal(err)
	}
}
