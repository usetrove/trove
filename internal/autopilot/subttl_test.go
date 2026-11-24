package autopilot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/analyze"
	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/fixer"
	"github.com/usetrove/trove/internal/measure/limitfit"
	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/setup"
	"github.com/usetrove/trove/internal/store"
)

// seedSubagents stores n sessions, each with one main-conversation request and one subagent whose requests
// follow each other after gaps (minutes). After a gap past the lifetime the prompt is rewritten in full.
func seedSubagents(t *testing.T, db *store.DB, prefix string, n int, start time.Time, gaps []int, oneHour bool, plan string) {
	t.Helper()
	const prompt = 40_000
	ttl := 5
	if oneHour {
		ttl = 60
	}
	k := 0
	put := func(session, agent string, side int, at time.Time, write, read int64) {
		k++
		w5, w1 := write, int64(0)
		if oneHour {
			w5, w1 = 0, write
		}
		if _, err := db.Exec(`INSERT INTO history_requests (request_key, source, session_key, agent_key, sidechain, ts, model, plan,
			cache_write_5m, cache_write_1h, cache_read) VALUES (?, 'claude_code', ?, ?, ?, ?, 'claude-sonnet-5-5', ?, ?, ?, ?)`,
			fmt.Sprintf("%s-%d", prefix, k), session, agent, side, at.UnixMilli(), plan, w5, w1, read); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < n; i++ {
		session := fmt.Sprintf("%s%d", prefix, i)
		at := start.Add(time.Duration(i) * 3 * time.Hour)
		put(session, "", 0, at, 30_000, 0)
		at = at.Add(time.Minute)
		put(session, "a1", 1, at, prompt, 0)
		for _, g := range gaps {
			at = at.Add(time.Duration(g) * time.Minute)
			if g > ttl {
				put(session, "a1", 1, at, prompt, 0)
			} else {
				put(session, "a1", 1, at, 200, prompt)
			}
		}
	}
}

type subTTLFx struct {
	db       *store.DB
	dir      string
	settings string
	deps     SettingsDeps
}

func newSubTTLFx(t *testing.T) *subTTLFx {
	t.Helper()
	db, dir := open(t)
	home := t.TempDir()
	claude := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claude, 0o755); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(claude, "settings.json")
	if err := os.WriteFile(settings, []byte("{\n  \"theme\": \"dark\"\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tbl, err := pricing.Default()
	if err != nil {
		t.Fatal(err)
	}
	fe := &fixer.Env{Home: home, ClaudeDir: claude, DataDir: dir, DB: db.DB, Getenv: func(string) string { return "" }, Cfg: config.Default().Fixer}
	f := config.AutopilotFile{Enabled: true, Policy: config.AutopilotPolicy, ConsentedAt: t0.UnixMilli(),
		SettingsPolicy: config.AutopilotSettingsPolicy, SettingsConsentedAt: t0.UnixMilli(), Entries: map[string]config.AutopilotEntry{}}
	if err := config.SaveAutopilot(dir, f); err != nil {
		t.Fatal(err)
	}
	return &subTTLFx{db: db, dir: dir, settings: settings, deps: SettingsDeps{DB: db, Fixer: fe, Pricing: tbl, SessionID: "ending"}}
}

func (x *subTTLFx) advance(t *testing.T, at time.Time) config.AutopilotSetting {
	t.Helper()
	x.deps.Fixer.Now = at
	if _, err := AdvanceSettings(context.Background(), x.dir, x.deps, at); err != nil {
		t.Fatal(err)
	}
	f, err := config.LoadAutopilot(x.dir)
	if err != nil {
		t.Fatal(err)
	}
	return f.Settings[SubTTLName]
}

func (x *subTTLFx) value(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(x.settings)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"subagentPromptCacheTtl": "1h"`) {
		return "1h"
	}
	if strings.Contains(string(b), "subagentPromptCacheTtl") {
		return "other"
	}
	return ""
}

func TestSubTTLSyntheticHistories(t *testing.T) {
	start := t0.Add(-20 * 24 * time.Hour)
	cases := []struct {
		name    string
		seed    func(t *testing.T, db *store.DB)
		state   string
		value   string
		because string
	}{
		{name: "no subagents", seed: func(t *testing.T, db *store.DB) { seedSubagents(t, db, "m", 40, start, nil, false, "api_key") },
			state: config.AutopilotShadow, because: "not enough subagent history"},
		{name: "short gaps only", seed: func(t *testing.T, db *store.DB) {
			seedSubagents(t, db, "s", 30, start, []int{1, 2, 1, 3}, false, "api_key")
		}, state: config.AutopilotShadow, because: "only 0 subagent cache rebuilds"},
		{name: "long gaps", seed: func(t *testing.T, db *store.DB) {
			seedSubagents(t, db, "l", 30, start, []int{20, 20, 20, 2}, false, "api_key")
		}, state: config.AutopilotOn, value: "1h", because: "estimated from API prices"},
		{name: "long gaps but every one past an hour", seed: func(t *testing.T, db *store.DB) {
			seedSubagents(t, db, "h", 30, start, []int{90, 90, 90, 2}, false, "api_key")
		}, state: config.AutopilotShadow, because: "only 0 subagent cache rebuilds"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			x := newSubTTLFx(t)
			c.seed(t, x.db)
			st := x.advance(t, t0)
			if st.State != c.state || x.value(t) != c.value || !strings.Contains(st.Reason, c.because) {
				t.Fatalf("state %q value %q reason %q; want %q %q containing %q", st.State, x.value(t), st.Reason, c.state, c.value, c.because)
			}
			if st.Estimate == nil && c.name != "no subagents" {
				t.Fatal("no estimate recorded")
			}
			if st.Estimate != nil && st.Estimate.Label != "estimated" {
				t.Fatalf("label %q", st.Estimate.Label)
			}
		})
	}
}

func TestSubTTLReversal(t *testing.T) {
	x := newSubTTLFx(t)
	seedSubagents(t, x.db, "l", 30, t0.Add(-20*24*time.Hour), []int{20, 20, 20, 2}, false, "api_key")
	if st := x.advance(t, t0); st.State != config.AutopilotOn {
		t.Fatalf("not applied: %s", st.Reason)
	}
	// After the change the person stops pausing: every gap is short, so the one-hour premium buys nothing.
	seedSubagents(t, x.db, "after", 30, t0.Add(time.Hour), []int{1, 2, 1, 2}, true, "api_key")

	if st := x.advance(t, t0.Add(7*24*time.Hour)); st.State != config.AutopilotOn || !strings.Contains(st.Reason, "measuring") {
		t.Fatalf("judged before 14 days: %q %q", st.State, st.Reason)
	}
	st := x.advance(t, t0.Add(15*24*time.Hour))
	if st.State != config.AutopilotShadow || x.value(t) != "" || !strings.Contains(st.Reason, "set back to the default") {
		t.Fatalf("not reverted: state %q value %q reason %q", st.State, x.value(t), st.Reason)
	}
	if st.Estimate == nil || st.Estimate.NetUSD >= 0 || st.Estimate.WindowDays != subTTLTripwireDays {
		t.Fatalf("estimate %+v", st.Estimate)
	}
	b, _ := os.ReadFile(x.settings)
	if string(b) != "{\n  \"theme\": \"dark\"\n}\n" {
		t.Fatalf("undo not byte-exact:\n%s", b)
	}
	// Cooldown: the same evidence does not turn it straight back on.
	if st := x.advance(t, t0.Add(16*24*time.Hour)); st.State != config.AutopilotShadow || !strings.Contains(st.Reason, "waiting") {
		t.Fatalf("no cooldown: %q %q", st.State, st.Reason)
	}
	f, _ := config.LoadAutopilot(x.dir)
	if n := len(f.History); n != 2 || f.History[1].To != config.AutopilotShadow {
		t.Fatalf("history %+v", f.History)
	}
}

func TestSubTTLKeptWhileItPays(t *testing.T) {
	x := newSubTTLFx(t)
	seedSubagents(t, x.db, "l", 30, t0.Add(-20*24*time.Hour), []int{20, 20, 20, 2}, false, "api_key")
	x.advance(t, t0)
	seedSubagents(t, x.db, "after", 30, t0.Add(time.Hour), []int{20, 20, 20, 2}, true, "api_key")
	st := x.advance(t, t0.Add(15*24*time.Hour))
	if st.State != config.AutopilotOn || x.value(t) != "1h" || !strings.HasPrefix(st.Reason, "kept") {
		t.Fatalf("state %q value %q reason %q", st.State, x.value(t), st.Reason)
	}
}

func TestSubTTLNeverOverTheUsersValue(t *testing.T) {
	x := newSubTTLFx(t)
	seedSubagents(t, x.db, "l", 30, t0.Add(-20*24*time.Hour), []int{20, 20, 20, 2}, false, "api_key")
	orig := "{\n  \"subagentPromptCacheTtl\": \"5m\"\n}\n"
	if err := os.WriteFile(x.settings, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	st := x.advance(t, t0)
	if b, _ := os.ReadFile(x.settings); string(b) != orig || st.State == config.AutopilotOn || !strings.Contains(st.Reason, "left alone") {
		t.Fatalf("changed the user's value: state %q reason %q", st.State, st.Reason)
	}
}

func TestSubTTLUserUndoIsFinal(t *testing.T) {
	x := newSubTTLFx(t)
	seedSubagents(t, x.db, "l", 30, t0.Add(-20*24*time.Hour), []int{20, 20, 20, 2}, false, "api_key")
	st := x.advance(t, t0)
	res, err := setup.UndoSelected(x.dir, []string{st.FixID})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixer.MarkReverted(x.dir, res, t0); err != nil {
		t.Fatal(err)
	}
	st = x.advance(t, t0.Add(24*time.Hour))
	if st.State != config.AutopilotOff || st.Attempts != subTTLMaxAttempts {
		t.Fatalf("state %q attempts %d", st.State, st.Attempts)
	}
	if st = x.advance(t, t0.Add(60*24*time.Hour)); st.State == config.AutopilotOn || x.value(t) != "" {
		t.Fatalf("set again after the user undid it: %q", st.Reason)
	}
}

func TestSubTTLNeedsTheSettingsConsent(t *testing.T) {
	x := newSubTTLFx(t)
	seedSubagents(t, x.db, "l", 30, t0.Add(-20*24*time.Hour), []int{20, 20, 20, 2}, false, "api_key")
	f, _ := config.LoadAutopilot(x.dir)
	f.SettingsPolicy = ""
	if err := config.SaveAutopilot(x.dir, f); err != nil {
		t.Fatal(err)
	}
	x.advance(t, t0)
	if x.value(t) != "" {
		t.Fatal("wrote the setting without the settings policy")
	}
}

// The plan-limit fit, when it has weights per type, decides for a subscription and is labeled so.
func TestEstimateSubTTLUsesTheFitForSubscriptions(t *testing.T) {
	s := analyze.SubTTL{Requests: 150, Sessions: 30, ByModel: map[string]*analyze.SubTTLTokens{
		"claude-sonnet-5-5": {Requests: 150, Write5m: 3_000_000, GapRebuilds: 60, GapRebuildTokens: 2_400_000}}}
	w := []float64{1, 5, 0.1, 1.25, 2} // % of the limit per million tokens, by limitfit type
	fit := &limitfit.Result{FiveHour: limitfit.MeterFit{Tier: limitfit.TierFit}}
	for i, v := range w {
		fit.FiveHour.Weights = append(fit.FiveHour.Weights, limitfit.Weight{Type: limitfit.TypeNames[i], Range: limitfit.Range{Point: v}})
	}
	for _, scale := range []float64{0.9, 1, 1.1} {
		var rep []float64
		for _, v := range w {
			rep = append(rep, v*scale)
		}
		fit.FiveHour.Reps = append(fit.FiveHour.Reps, rep)
	}
	tbl, _ := pricing.Default()
	est := estimateSubTTL(s, false, tbl, t0, fit, 30)
	// 2.4M rewrites become reads (1.25 -> 0.1) and 0.6M writes move to one hour (1.25 -> 2): 2.76 - 0.45 = 2.31.
	if est.Basis != limitfit.BasisFit || est.NetPct5h < 2.30 || est.NetPct5h > 2.32 || est.NetPct5hLo <= 0 {
		t.Fatalf("estimate %+v", est.AutopilotSettingEstimate)
	}
	if ok, why := est.recommend(); !ok || !strings.Contains(why, "estimated from your meter") {
		t.Fatalf("recommend %v %q", ok, why)
	}
	s.APIKey = s.Requests // on an API key the bill is in dollars: API prices decide
	if est := estimateSubTTL(s, false, tbl, t0, fit, 30); est.Basis != limitfit.BasisAPIPrices {
		t.Fatalf("basis %q for an API key", est.Basis)
	}
}

func TestSubTTLWithdrawnTakesItsChangeBack(t *testing.T) {
	x := newSubTTLFx(t)
	seedSubagents(t, x.db, "l", 30, t0.Add(-20*24*time.Hour), []int{20, 20, 20, 2}, false, "api_key")
	if st := x.advance(t, t0); st.State != config.AutopilotOn {
		t.Fatalf("not applied: %s", st.Reason)
	}
	id, err := DisableSettings(x.dir)
	if err != nil || id == "" {
		t.Fatalf("disable: %q %v", id, err)
	}
	if x.value(t) != "1h" {
		t.Fatal("withdrawing must not edit settings.json outside a session end")
	}
	if st := x.advance(t, t0.Add(time.Hour)); st.State != config.AutopilotOff || x.value(t) != "" {
		t.Fatalf("not taken back: %q value %q", st.State, x.value(t))
	}
	if _, err := EnableSettings(x.dir, t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	f, _ := config.LoadAutopilot(x.dir)
	if !f.SettingsActive() || f.Settings[SubTTLName].State != config.AutopilotShadow {
		t.Fatalf("re-agreeing: %+v", f.Settings[SubTTLName])
	}
}

// `trove autopilot off` must not forget a setting Autopilot changed: it is taken back at the next quiet end.
func TestSubTTLAutopilotOffStillTakesItBack(t *testing.T) {
	x := newSubTTLFx(t)
	seedSubagents(t, x.db, "l", 30, t0.Add(-20*24*time.Hour), []int{20, 20, 20, 2}, false, "api_key")
	st := x.advance(t, t0)
	if st.State != config.AutopilotOn {
		t.Fatalf("not applied: %s", st.Reason)
	}
	if err := Disable(x.dir); err != nil {
		t.Fatal(err)
	}
	if id := SettingChangeLeft(x.dir); id != st.FixID {
		t.Fatalf("change left = %q, want %q", id, st.FixID)
	}
	if x.value(t) != "1h" {
		t.Fatal("turning Autopilot off edited settings.json at once")
	}
	if st := x.advance(t, t0.Add(time.Hour)); st.State != config.AutopilotOff || x.value(t) != "" {
		t.Fatalf("not taken back after off: %q value %q", st.State, x.value(t))
	}
}

// A database Trove cannot read is "cannot tell", never "the user undid it", and no reason holds a path.
func TestSubTTLUnreadableRecordChangesNothing(t *testing.T) {
	x := newSubTTLFx(t)
	seedSubagents(t, x.db, "l", 30, t0.Add(-20*24*time.Hour), []int{20, 20, 20, 2}, false, "api_key")
	before := x.advance(t, t0)
	if _, err := x.db.Exec(`ALTER TABLE fixes RENAME TO fixes_gone`); err != nil {
		t.Fatal(err)
	}
	st := x.advance(t, t0.Add(15*24*time.Hour))
	if st.State != config.AutopilotOn || st.FixID != before.FixID || st.Attempts != before.Attempts || x.value(t) != "1h" {
		t.Fatalf("a read error changed the state: %+v", st)
	}
	if strings.ContainsAny(st.Reason, `/\`) {
		t.Fatalf("reason holds a path: %q", st.Reason)
	}
}
