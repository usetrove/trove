package experiment

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/measure"
)

// weekWeigher stands in for the limit fit: one point of the weekly limit per million tokens.
var weekWeigher = Weigher{Metric: MetricWeekPct, Weigh: func(u SessionUsage) (float64, bool) {
	return float64(u.Input+u.Read+u.Output) / 1e6, true
}}

// A trial judged on the weekly limit says so, is labeled estimated, and also gives the difference in points of the
// limit per session with its own range, matching the planted effect.
func TestHookTrialOnTheWeeklyLimitGivesPointsWithARange(t *testing.T) {
	db := openDB(t)
	historySessions(t, db, "h", 120, t0, 0.70)
	r, err := AnalyzeHookTrialBy(context.Background(), db, hookOpt, testCfg(), t0.Add(-time.Hour).UnixMilli(), trialNow, 30*time.Minute, weekWeigher)
	if err != nil {
		t.Fatal(err)
	}
	if r.Headline != MetricWeekPct || r.Cost == nil || r.Cost.Label != measure.Estimated {
		t.Fatalf("headline %q, cost %+v", r.Headline, r.Cost)
	}
	if r.Status != Saves || !contains(r.Reason, "less of the weekly limit") || !contains(r.Reason, "estimated from your meter") {
		t.Errorf("status %q, reason %q", r.Status, r.Reason)
	}
	p := r.HeadlinePoints
	if p == nil || p.Kind != "points" || p.Label != measure.Estimated {
		t.Fatalf("points: %+v", p)
	}
	// Each session's tokens: 4 requests of input t/8, cache read t/8 and output t/32, so 1.125 t in all, with t
	// about 100k times a wobble averaging 1.09: about 0.123 points off, so 30% less with it on is about -0.037 points.
	if !(p.Low <= p.Value && p.Value <= p.High) || p.High >= 0 || p.Value > -0.032 || p.Value < -0.042 {
		t.Errorf("points %+v", p)
	}
}

// A token headline has no points figure: points only mean something for a share of a limit.
func TestHookTrialOnTokensHasNoPoints(t *testing.T) {
	db := openDB(t)
	historySessions(t, db, "h", 120, t0, 0.70)
	if r := analyzeTrial(t, db, t0.Add(-time.Hour)); r.HeadlinePoints != nil {
		t.Errorf("points on a token headline: %+v", r.HeadlinePoints)
	}
}

// The pass-rate change is pass rate on minus off, with a range, and only once both arms ran enough tests.
func TestHookTrialPassRateChange(t *testing.T) {
	db := openDB(t)
	historySessions(t, db, "h", 120, t0, 1.0)
	// Every on session's last test fails one time in four; every off session's passes.
	i := 0
	rows, err := db.Query(`SELECT session_key, arm FROM experiment_assignments ORDER BY session_key`)
	if err != nil {
		t.Fatal(err)
	}
	type a struct{ key, arm string }
	var as []a
	for rows.Next() {
		var x a
		if err := rows.Scan(&x.key, &x.arm); err != nil {
			t.Fatal(err)
		}
		as = append(as, x)
	}
	_ = rows.Close()
	var onN, onFail int
	for _, x := range as {
		code := 0
		if x.arm == ArmOn {
			onN++
			if onN%4 == 0 {
				code, onFail = 1, onFail+1
			}
		}
		if _, err := db.Exec(`INSERT INTO test_runs(session_key, ts, cmd_hash, exit_code) VALUES(?,?,'h',?)`, x.key, t0.Add(time.Duration(i)*time.Minute).UnixMilli(), code); err != nil {
			t.Fatal(err)
		}
		i++
	}
	r := analyzeTrial(t, db, t0.Add(-time.Hour))
	if r.TestFail == nil || r.TestFail.PassChange == nil {
		t.Fatalf("no pass change: %+v", r.TestFail)
	}
	pc := r.TestFail.PassChange
	want := -float64(onFail) / float64(onN)
	if math.Abs(pc.Value-want) > 1e-9 || pc.High >= 0 || pc.Low > pc.Value || pc.High < pc.Value {
		t.Errorf("pass change %+v, want %.3f with a range below zero", pc, want)
	}
}

func TestPassChangeIsSeededAndBracketsTheEstimate(t *testing.T) {
	a := passChange(40, 50, 45, 50, 7)
	b := passChange(40, 50, 45, 50, 7)
	if a == nil || *a != *b {
		t.Fatalf("not reproducible: %+v %+v", a, b)
	}
	if math.Abs(a.Value-(-0.1)) > 1e-9 || a.Low >= a.Value || a.High <= a.Value || a.Low > -0.15 || a.High < 0 {
		t.Errorf("range %+v", a)
	}
	if passChange(1, 0, 1, 2, 1) != nil || passChange(1, 2, 0, 0, 1) != nil {
		t.Error("an empty arm must give no figure")
	}
	if c := passChange(50, 50, 50, 50, 1); c.Value != 0 || c.Low != 0 || c.High != 0 {
		t.Errorf("all pass in both arms should be exactly zero: %+v", c)
	}
}
