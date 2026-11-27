package report

import (
	"context"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/measure/limitfit"
)

func rng(p, lo, hi float64) limitfit.Range { return limitfit.Range{Point: p, Lo: lo, Hi: hi} }

// fits returns a saved fit at each tier.
func fits() map[string]*limitfit.Result {
	none := &limitfit.Result{Samples: 12,
		FiveHour: limitfit.MeterFit{Meter: limitfit.FiveHour, Tier: limitfit.TierNone, CleanIntervals: 3, Windows: 1, NeedIntervals: 7, NeedWindows: 1, HoursPerInterval: 0.8},
		Week:     limitfit.MeterFit{Meter: limitfit.SevenDay, Tier: limitfit.TierNone, NeedIntervals: 10, NeedWindows: 2}}
	per := rng(0.52, 0.44, 0.61)
	wper := rng(0.041, 0.035, 0.05)
	out := rng(9, 5, 14)
	prices := &limitfit.Result{Samples: 140,
		FiveHour: limitfit.MeterFit{Meter: limitfit.FiveHour, Tier: limitfit.TierPrices, CleanIntervals: 18, Windows: 3, NeedIntervals: 22, NeedWindows: 1, HoursPerInterval: 0.9, PerUSD: &per, Outside: &out},
		Week:     limitfit.MeterFit{Meter: limitfit.SevenDay, Tier: limitfit.TierPrices, CleanIntervals: 12, Windows: 1, NeedIntervals: 28, NeedWindows: 1, HoursPerInterval: 3, PerUSD: &wper}}
	w := []limitfit.Weight{
		{Type: "input", Range: rng(2.1, 0.8, 3.0)}, {Type: "output", Range: rng(12, 10, 14)}, {Type: "cache_read", Range: rng(0.25, 0.24, 0.27)},
		{Type: "cache_write_5m", Range: rng(2.4, 2.1, 2.8)}, {Type: "cache_write_1h", Range: rng(3.9, 3.7, 4.1)}}
	ratios := []limitfit.Weight{{Type: "input", Range: rng(1, 1, 1)}, {Type: "output", Range: rng(5.7, 4, 9)}, {Type: "cache_read", Range: rng(0.12, 0.09, 0.2)},
		{Type: "cache_write_5m", Range: rng(1.1, 0.8, 1.6)}, {Type: "cache_write_1h", Range: rng(1.9, 1.4, 2.6)}}
	fams := []limitfit.FamilyMult{{Family: "haiku", Range: rng(1, 1, 1)}, {Family: "opus", Separate: true, Range: rng(1.8, 1.6, 2.1)}, {Family: "sonnet", Separate: true, Range: rng(1, 1, 1)}}
	ww := make([]limitfit.Weight, len(w))
	for i, x := range w {
		ww[i] = limitfit.Weight{Type: x.Type, Range: rng(x.Point*0.08, x.Lo*0.08, x.Hi*0.08)}
	}
	wout := rng(11, 6, 16)
	fit := &limitfit.Result{Samples: 900,
		FiveHour: limitfit.MeterFit{Meter: limitfit.FiveHour, Tier: limitfit.TierFit, CleanIntervals: 112, Windows: 31, Trimmed: 9, PerUSD: &per, Outside: &out,
			Weights: w, InputDetermined: true, Ratios: ratios, Reference: "sonnet", Families: fams, RegimeSince: now.AddDate(0, 0, -20).UnixMilli()},
		Week: limitfit.MeterFit{Meter: limitfit.SevenDay, Tier: limitfit.TierFit, Profile: true, CleanIntervals: 30, Windows: 2, PerUSD: &wper, Outside: &wout,
			Weights: ww, Reference: "sonnet", Families: fams}}
	return map[string]*limitfit.Result{"none": none, "api_prices": prices, "fit": fit}
}

func TestPlanLimitsGolden(t *testing.T) {
	for name, f := range fits() {
		t.Run(name, func(t *testing.T) {
			db := openDB(t)
			seedHistory(t, db, "subscription")
			r, err := Build(context.Background(), db, Inputs{Config: config.Default(), Now: now, LimitFit: f}, mustPeriod(t, Month))
			if err != nil {
				t.Fatal(err)
			}
			if r.PlanLimits == nil || r.PlanLimits.Label != "estimated" || r.PlanLimits.FiveHour.Label != "estimated" {
				t.Fatalf("section: %+v", r.PlanLimits)
			}
			if name == "none" && r.PlanLimits.Waste != nil {
				t.Error("no conversion below the price gate")
			}
			if name != "none" && (r.PlanLimits.Waste == nil || r.PlanLimits.Waste.FiveHour.Basis != limitfit.BasisAPIPrices) {
				t.Errorf("waste should convert by API prices: %+v", r.PlanLimits.Waste)
			}
			assertLabeled(t, r)
			text := Render(r, Options{Width: 80})
			if strings.Contains(strings.ToLower(text), "official weight") && !strings.Contains(text, "not official weights") {
				t.Error("must never claim official weights")
			}
			golden(t, "plan_limits_"+name+".txt", text[strings.Index(text, "Plan limits"):strings.Index(text, "Biggest leaks")])
			golden(t, "plan_limits_"+name+".json", toJSON(t, r.PlanLimits))
		})
	}
}

func TestPlanLimitsAbsentWithoutMeter(t *testing.T) {
	db := openDB(t)
	seedHistory(t, db, "subscription")
	for _, f := range []*limitfit.Result{nil, {}} {
		r, err := Build(context.Background(), db, Inputs{Config: config.Default(), Now: now, LimitFit: f}, mustPeriod(t, Month))
		if err != nil {
			t.Fatal(err)
		}
		if r.PlanLimits != nil {
			t.Errorf("no meter readings, no section: %+v", r.PlanLimits)
		}
	}
}

func TestStatusLineSavedLimit(t *testing.T) {
	s := Status{Period: "month", Requests: 30, Saved: measure.Figure{Value: 47, Label: measure.Estimated}}
	plain := RenderStatus(s, FormatShell, false)
	s.SavedLimit = fits()["api_prices"].PercentUSD(47)
	got := RenderStatus(s, FormatShell, false)
	if !strings.Contains(got, "saved ~$47.00 (~24% of 5h)") || strings.Contains(plain, "% of 5h") {
		t.Errorf("with fit: %q\nwithout: %q", got, plain)
	}
}
