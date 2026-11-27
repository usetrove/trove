package contribute

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/measure/limitfit"
	"github.com/usetrove/trove/internal/regress"
	"github.com/usetrove/trove/internal/report"
)

func rng(p, lo, hi float64) limitfit.Range { return limitfit.Range{Point: p, Lo: lo, Hi: hi} }

func check(kind, key, metric, status string, n int) regress.Check {
	return regress.Check{Kind: kind, Key: key, Metric: metric, Status: status, N: n, Value: 123.456, Baseline: 99.5,
		MADs: 4.2, Sessions: 7, Stratum: "claude-secret-model/high", Label: "measured"}
}

// limitsSample is a fitted 5-hour meter, a weekly meter that borrows its profile, and a regression watch
// with version and day checks. Everything that must not be sent carries a recognisable value.
func limitsSample() (*report.PlanLimits, *report.Regressions) {
	five := limitfit.MeterFit{
		Meter: limitfit.FiveHour, Tier: limitfit.TierFit, CleanIntervals: 61, Windows: 9, InputDetermined: true,
		PerUSD:  &limitfit.Range{Point: 0.777, Lo: 0.7, Hi: 0.8},
		Outside: &limitfit.Range{Point: 12.4, Lo: 8, Hi: 17},
		Weights: []limitfit.Weight{{Type: "input", Range: rng(3.1415, 3, 3.3)}},
		Ratios: []limitfit.Weight{
			{Type: "input", Range: rng(1, 1, 1)},
			{Type: "output", Range: rng(5.04, 4.4, 5.62)},
			{Type: "cache_read", Range: rng(0.0834, 0.061, 0.112)},
			{Type: "cache_write_5m", Range: rng(1.27, 1.05, 1.46)},
			{Type: "cache_write_1h", Range: rng(12.6, 10.1, 15.3)},
		},
		Reference: "secret-family", RegimeSince: 1_790_000_000_123,
		Families: []limitfit.FamilyMult{{Family: "secret-family", Separate: true, Range: rng(1, 1, 1)}},
	}
	week := five
	week.Meter, week.Profile, week.Outside = limitfit.SevenDay, true, &limitfit.Range{Point: 15.2}
	pl := &report.PlanLimits{Label: "estimated", FiveHour: &report.LimitMeter{MeterFit: five}, Week: &report.LimitMeter{MeterFit: week}}

	alert := regress.Alert{Check: check(regress.KindVersion, "2.1.90", regress.MetricLimitPerKOE, regress.StatusAlert, 30),
		Cause: regress.CauseClient, Sentence: "secret sentence", Advice: "secret advice"}
	rg := &report.Regressions{
		Alerts: []regress.Alert{alert},
		Checks: []regress.Check{
			alert.Check,
			check(regress.KindVersion, "2.1.90", regress.MetricRebuilds, regress.StatusNormal, 400),
			check(regress.KindVersion, "2.1.89", regress.MetricRebuilds, regress.StatusNormal, 900),
			check(regress.KindVersion, "2.1.89", regress.MetricThinkShare, regress.StatusTooLittleData, 900),
			check(regress.KindVersion, "2.1.88-beta /home/x", regress.MetricRebuilds, regress.StatusAlert, 5000),
			check(regress.KindDay, "2026-11-20", regress.MetricRebuilds, regress.StatusAlert, 300),
		},
	}
	return pl, rg
}

func TestLimitsNeedTheirOwnConsent(t *testing.T) {
	r := sample()
	r.PlanLimits, r.Regressions = limitsSample()
	if p := Build(r, Meta{}); p.Limits != nil {
		t.Errorf("limits built without a plan: %+v", p.Limits)
	}
	if p := Build(r, Meta{Plan: "free text"}); p.Limits != nil {
		t.Errorf("limits built with a plan outside the list")
	}
	for _, st := range []State{
		{Enabled: true, InstallID: "a"},
		{Enabled: true, InstallID: "a", Limits: true},                     // no plan
		{Enabled: false, InstallID: "a", Limits: true, Plan: "max"},       // sharing off
		{Enabled: true, InstallID: "a", Limits: true, Plan: "my-company"}, // not in the list
	} {
		if st.LimitsPlan() != "" {
			t.Errorf("LimitsPlan(%+v) = %q, want empty", st, st.LimitsPlan())
		}
	}
	if (State{Enabled: true, Limits: true, Plan: "pro"}).LimitsPlan() != "pro" {
		t.Error("consented state gave no plan")
	}
}

func TestAutoDropsLimitsWithoutConsent(t *testing.T) {
	rec := &recorder{}
	srv := rec.server()
	defer srv.Close()
	dir := enabledDir(t) // sharing on, limits not
	r := sample()
	r.PlanLimits, r.Regressions = limitsSample()
	p := Build(r, Meta{Plan: "max"}) // a caller that built them anyway
	if p.Limits == nil {
		t.Fatal("sample built no limits")
	}
	if _, err := Auto(context.Background(), dir, srv.URL, p, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rec.body, "limits") {
		t.Errorf("upload carried limits without consent: %s", rec.body)
	}
}

func TestLimitsHoldOnlyTheApprovedFields(t *testing.T) {
	r := sample()
	r.PlanLimits, r.Regressions = limitsSample()
	l := Build(r, Meta{Plan: "max"}).Limits
	if l == nil || l.FiveHour == nil || l.FiveHour.Ratios == nil {
		t.Fatalf("limits = %+v", l)
	}
	rs := l.FiveHour.Ratios
	for name, got := range map[string]Ratio{
		"output": *rs.Output, "cache_read": *rs.CacheRead, "cache_write_5m": *rs.CacheWrite5m, "cache_write_1h": *rs.CacheWrite1h,
	} {
		want := map[string]Ratio{
			"output": {5, 1.2}, "cache_read": {0.083, 0.051}, "cache_write_5m": {1.3, 0.41}, "cache_write_1h": {13, 5.2},
		}[name]
		if got != want {
			t.Errorf("%s = %+v, want %+v", name, got, want)
		}
	}
	if l.FiveHour.UnexplainedPct == nil || *l.FiveHour.UnexplainedPct != 12 {
		t.Errorf("5h unexplained = %v", l.FiveHour.UnexplainedPct)
	}
	if l.SevenDay == nil || l.SevenDay.Ratios != nil || *l.SevenDay.UnexplainedPct != 15 {
		t.Errorf("weekly meter borrowing the 5h profile must send its unexplained share only: %+v", l.SevenDay)
	}
	if l.ClaudeCodeVersion != "2.1.89" {
		t.Errorf("main version = %q, want the most used valid one", l.ClaudeCodeVersion)
	}
	want := []VersionCheck{
		{Version: "2.1.90", Metric: regress.MetricLimitPerKOE, Status: "alert", Cause: "client"},
		{Version: "2.1.90", Metric: regress.MetricRebuilds, Status: "normal"},
		{Version: "2.1.89", Metric: regress.MetricRebuilds, Status: "normal"},
	}
	if fmt.Sprint(l.VersionChecks) != fmt.Sprint(want) {
		t.Errorf("version checks = %+v\nwant %+v", l.VersionChecks, want)
	}
	b, _ := json.Marshal(l)
	for _, leak := range []string{"secret", "/home", "2026-11-20", "beta", "123.4", "99.5", "3.14", "0.777", "1790000000", "high", "61", "measured"} {
		if strings.Contains(string(b), leak) {
			t.Errorf("limits leaked %q: %s", leak, b)
		}
	}
}

func TestLimitsGates(t *testing.T) {
	pl, _ := limitsSample()
	// Price tier only: nothing from that meter, not even the unexplained share.
	prices := pl.FiveHour.MeterFit
	prices.Tier = limitfit.TierPrices
	if m := limitMeter(prices); m != nil {
		t.Errorf("api_prices tier sent %+v", m)
	}
	// Input weight too uncertain: no ratios, but the unexplained share still goes.
	loose := pl.FiveHour.MeterFit
	loose.InputDetermined = false
	if m := limitMeter(loose); m == nil || m.Ratios != nil || m.UnexplainedPct == nil {
		t.Errorf("undetermined input = %+v", m)
	}
	// Nothing at all: no section.
	if l := buildLimits(report.Report{}, "max"); l != nil {
		t.Errorf("empty report built %+v", l)
	}
}

func TestVersionChecksKeepTheNewestFive(t *testing.T) {
	rg := &report.Regressions{}
	for i := 1; i <= 8; i++ {
		rg.Checks = append(rg.Checks, check(regress.KindVersion, fmt.Sprintf("2.%d.0", i*3%8+1), regress.MetricRebuilds, regress.StatusNormal, i))
	}
	_, got := versionChecks(rg)
	var vs []string
	for _, c := range got {
		vs = append(vs, c.Version)
	}
	if s := strings.Join(vs, " "); s != "2.8.0 2.7.0 2.6.0 2.5.0 2.4.0" {
		t.Errorf("versions = %s", s)
	}
}

func TestRoundRatio(t *testing.T) {
	for _, c := range []struct{ in, want float64 }{
		{0, 0}, {-1, 0}, {0.0834, 0.083}, {0.1, 0.1}, {0.00456, 0.0046}, {0.999, 1}, {1.27, 1.3},
		{5.04, 5}, {9.96, 10}, {12.6, 13}, {2500, 1000},
	} {
		if got := RoundRatio(c.in); got != c.want {
			t.Errorf("RoundRatio(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestLargestPayloadFitsTheReceiver(t *testing.T) {
	r := sample()
	r.Optimizers.Rows = nil
	for i := 0; i < 40; i++ { // the receiver's limit on optimizers, with the longest names it accepts
		r.Optimizers.Rows = append(r.Optimizers.Rows, report.OptimizerRow{Name: fmt.Sprintf("%041d", i), Effective: "shadow", RolledBack: "x",
			Measured: &report.Measured{Enough: true, Reduced: true, Percent: -999}})
	}
	r.PlanLimits, r.Regressions = limitsSample()
	r.PlanLimits.Week.Profile = false
	r.Regressions.Checks = nil
	for v := 0; v < MaxVersions; v++ {
		for _, m := range regress.Metrics {
			r.Regressions.Checks = append(r.Regressions.Checks, check(regress.KindVersion, fmt.Sprintf("999.999.%d", 9990+v), m, regress.StatusAlert, 1))
		}
	}
	p := Build(r, Meta{TroveVersion: strings.Repeat("9", 32), OS: strings.Repeat("a", 16), Arch: strings.Repeat("a", 16), Plan: "unspecified"})
	p.InstallID = strings.Repeat("f", 32)
	b, err := Encode(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Limits.VersionChecks) != MaxVersions*len(regress.Metrics) {
		t.Fatalf("built %d checks", len(p.Limits.VersionChecks))
	}
	if len(b) >= MaxBody {
		t.Errorf("largest payload is %d bytes, over the receiver's %d", len(b), MaxBody)
	}
	t.Logf("largest payload: %d bytes", len(b))
}
