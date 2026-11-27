package report

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/measure/limitfit"
	"github.com/usetrove/trove/internal/store"
)

// seedHandoff stores n sessions of a randomized hook trial the way a user with no gateway has them: an assignment
// from the hooks (alternating arms), history rows from the agent's logs, and one test run per session. Sessions in
// the on arm use onFactor times the tokens; every failEvery-th on session ends with a failing test (0: none).
func seedHandoff(t *testing.T, db *store.DB, optimizer string, n int, onFactor float64, failEvery int) {
	t.Helper()
	start := now.AddDate(0, 0, -12)
	onSeen := 0
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("%s-s%03d", optimizer, i)
		arm, factor := "off", 1.0
		if i%2 == 0 {
			arm, factor = "on", onFactor
			onSeen++
		}
		at := start.Add(time.Duration(i) * 3 * time.Hour)
		exec(t, db, `INSERT INTO experiment_assignments(session_key, optimizer, arm, stratum, seq, assigned_at) VALUES(?,?,?,'',?,?)`,
			key, optimizer, arm, i, at.UnixMilli())
		tokens := int64(200000 * factor * (1 + 0.05*float64(i%5)))
		for r := 0; r < 4; r++ {
			exec(t, db, `INSERT INTO history_requests(request_key, source, session_key, ts, model, input_tokens, cache_read, output_tokens, errors)
				VALUES(?,?,?,?,?,?,?,?,0)`, fmt.Sprintf("%s-%d", key, r), "claude_code", key, at.Add(time.Duration(r)*time.Minute).UnixMilli(),
				"claude-haiku-4-5", tokens/16, tokens/8, tokens/64)
		}
		code := 0
		if arm == "on" && failEvery > 0 && onSeen%failEvery == 0 {
			code = 1
		}
		exec(t, db, `INSERT INTO test_runs(session_key, ts, cmd_hash, exit_code) VALUES(?,?,'h',?)`, key, at.Add(5*time.Minute).UnixMilli(), code)
	}
}

// toRoundedJSON is toJSON with every number rounded to 8 significant digits and float noise around zero dropped. Bootstrap ranges differ in the last
// bits between CPUs (fused multiply-add on arm64), so a golden of the raw floats would hold only on one platform.
func toRoundedJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var x any
	if err := json.Unmarshal(b, &x); err != nil {
		t.Fatal(err)
	}
	var round func(any) any
	round = func(v any) any {
		switch v := v.(type) {
		case float64:
			if v > -1e-9 && v < 1e-9 {
				return 0.0 // float noise around zero, such as -2e-16, differs between CPUs too
			}
			r, _ := strconv.ParseFloat(strconv.FormatFloat(v, 'g', 8, 64), 64)
			return r
		case []any:
			for i := range v {
				v[i] = round(v[i])
			}
		case map[string]any:
			for k := range v {
				v[k] = round(v[k])
			}
		}
		return v
	}
	return toJSON(t, round(x))
}

// weekFit is a limit fit with only an API-price scale for each meter.
var weekFit = &limitfit.Result{
	FiveHour: limitfit.MeterFit{PerUSD: &limitfit.Range{Point: 2, Lo: 1.5, Hi: 2.5}},
	Week:     limitfit.MeterFit{PerUSD: &limitfit.Range{Point: 0.25, Lo: 0.2, Hi: 0.3}},
}

// A hook tool in the randomized test is judged on that test: tokens, share of the weekly limit and pass rate, each
// with a range, labeled, with the evidence line saying the sessions were assigned at random.
func TestReportExternalHookToolUsesTheRandomizedTest(t *testing.T) {
	db := openDB(t)
	seedExternal(t, db, toolRef{"rtk", "1.1.0", "Apache-2.0"}, "on", 0, 0, 0, 0)
	seedHandoff(t, db, "ext_rtk", 80, 0.6, 4)
	r, err := Build(context.Background(), db, Inputs{Config: config.Default(), Now: now, LimitFit: weekFit}, mustPeriod(t, Month))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.ExternalTools) != 1 || r.ExternalTools[0].Randomized == nil {
		t.Fatalf("no randomized trial: %+v", r.ExternalTools)
	}
	tool := r.ExternalTools[0]
	v := tool.Verdict
	for _, want := range []string{"randomized test", "tokens per session -", "share of the weekly limit per session -", "points of the weekly limit", "estimated from API prices", "pass rate -", "40 sessions on, 40 off"} {
		if !strings.Contains(v.Text, want) {
			t.Errorf("verdict lacks %q: %s", want, v.Text)
		}
	}
	if v.Label != "measured" || !strings.Contains(tool.Evidence, "at random") {
		t.Errorf("label %q evidence %q", v.Label, tool.Evidence)
	}
	golden(t, "external_randomized.txt", Render(r, Options{Width: 80}))
	golden(t, "external_randomized.json", toRoundedJSON(t, r.ExternalTools))
	assertLabeled(t, r)
}

// With no weekly fit the randomized test still reports tokens, and says why the weekly figure is missing.
func TestReportExternalHookToolWithoutAWeeklyFit(t *testing.T) {
	db := openDB(t)
	seedExternal(t, db, toolRef{"rtk", "1.1.0", "Apache-2.0"}, "on", 0, 0, 0, 0)
	seedHandoff(t, db, "ext_rtk", 80, 0.6, 0)
	r, err := Build(context.Background(), db, Inputs{Config: config.Default(), Now: now}, mustPeriod(t, Month))
	if err != nil {
		t.Fatal(err)
	}
	f := r.ExternalTools[0].Randomized
	if f == nil || f.Week != nil || !strings.Contains(f.WeekNote, "no weekly-limit fit yet") {
		t.Fatalf("%+v", f)
	}
	if strings.Contains(r.ExternalTools[0].Verdict.Text, "weekly") {
		t.Errorf("a weekly figure without a fit: %s", r.ExternalTools[0].Verdict.Text)
	}
	if out := Render(r, Options{Width: 200}); !strings.Contains(out, "Weekly limit: no weekly-limit fit yet") {
		t.Errorf("the missing weekly figure is not explained:\n%s", out)
	}
}
