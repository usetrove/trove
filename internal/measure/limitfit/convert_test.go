package limitfit

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/store"
)

func fitted() *Result {
	five := MeterFit{Meter: FiveHour, Tier: TierFit, Reference: "sonnet",
		Families: []FamilyMult{{Family: "opus", Separate: true, Range: Range{2, 1.5, 2.5}}, {Family: "sonnet", Separate: true, Range: Range{1, 1, 1}}},
		PerUSD:   &Range{Point: 0.5, Lo: 0.4, Hi: 0.6}}
	for k := 0; k < NTypes; k++ {
		five.Weights = append(five.Weights, Weight{Type: TypeNames[k], Range: Range{Point: truth[k]}})
	}
	for i := 0; i < 100; i++ { // replicates spread ±10% around the point
		f := 0.9 + 0.2*float64(i)/99
		row := make([]float64, NTypes+2)
		for k := 0; k < NTypes; k++ {
			row[k] = truth[k] * f
		}
		row[NTypes], row[NTypes+1] = 2, 1
		five.Reps = append(five.Reps, row)
	}
	week := MeterFit{Meter: SevenDay, Tier: TierPrices, PerUSD: &Range{Point: 0.05, Lo: 0.04, Hi: 0.07}}
	return &Result{FiveHour: five, Week: week, TopModel: "claude-sonnet-5-5", Mix: [NTypes]float64{TInput: 0.1, TCacheRead: 0.8, TWrite1h: 0.1}}
}

func TestPercent(t *testing.T) {
	r := fitted()
	d := Delta{Model: "claude-sonnet-5-5", USD: 2}
	d.Tokens[TOutput] = 1_000_000 // 12 points per million on the 5h meter
	e := r.Percent(d)
	if e == nil || e.FiveHour == nil || e.Week == nil || e.Label != "estimated" {
		t.Fatalf("%+v", e)
	}
	if e.FiveHour.Basis != BasisFit || math.Abs(e.FiveHour.Point-12) > 1e-9 || e.FiveHour.Lo >= 12 || e.FiveHour.Hi <= 12 {
		t.Errorf("5h by type weights: %+v", e.FiveHour)
	}
	if e.Week.Basis != BasisAPIPrices || math.Abs(e.Week.Point-0.1) > 1e-9 || math.Abs(e.Week.Hi-0.14) > 1e-9 {
		t.Errorf("week by API prices: %+v", e.Week)
	}
	d.Model = "claude-opus-5-5"
	if e := r.Percent(d); math.Abs(e.FiveHour.Point-24) > 1e-9 {
		t.Errorf("opus multiplier: %+v", e.FiveHour)
	}
	// No dollars and no type fit for the week: the week stays empty rather than guessed.
	d.USD = 0
	if e := r.Percent(d); e.Week != nil {
		t.Errorf("week without dollars: %+v", e.Week)
	}
	if e := (&Result{FiveHour: MeterFit{Tier: TierNone}, Week: MeterFit{Tier: TierNone}}).Percent(d); e != nil {
		t.Errorf("no fit must give nothing: %+v", e)
	}
	var nilRes *Result
	if nilRes.Percent(d) != nil || nilRes.PercentUSD(5) != nil {
		t.Error("nil result")
	}
	if e := r.PercentUSD(10); e.FiveHour.Basis != BasisAPIPrices || math.Abs(e.FiveHour.Point-5) > 1e-9 {
		t.Errorf("dollars: %+v", e.FiveHour)
	}
}

func TestContextDeltaAndPrice(t *testing.T) {
	r := fitted()
	d := r.ContextDelta(1_000_000)
	if d.Model != "claude-sonnet-5-5" || d.Tokens[TCacheRead] != 800_000 || d.Tokens[TInput] != 100_000 || d.Tokens[TOutput] != 0 {
		t.Fatalf("%+v", d)
	}
	tbl, err := pricing.Default()
	if err != nil {
		t.Fatal(err)
	}
	PriceDelta(tbl, &d, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	if d.USD <= 0 {
		t.Errorf("not priced: %+v", d)
	}
}

func TestCache(t *testing.T) {
	dir := t.TempDir()
	if ReadCache(dir) != nil {
		t.Fatal("missing cache should read as nil")
	}
	r := fitted()
	r.ComputedAt, r.LastSample = 1000, 900
	if err := WriteCache(dir, *r); err != nil {
		t.Fatal(err)
	}
	got := ReadCache(dir)
	if got == nil || got.FiveHour.Weights[TOutput].Point != truth[TOutput] || len(got.FiveHour.Reps) != 100 {
		t.Fatalf("round trip: %+v", got)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(filepath.Join(dir, CacheFile)); fi.Mode().Perm() != 0o600 {
			t.Errorf("permissions %v", fi.Mode().Perm())
		}
	}
	if err := os.WriteFile(filepath.Join(dir, CacheFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ReadCache(dir) != nil {
		t.Error("a corrupt cache must read as nil")
	}
	now := time.UnixMilli(2000)
	if !Stale(nil, 1, now) || !Stale(r, 950, now) || Stale(r, 900, now) || !Stale(r, 900, now.Add(25*time.Hour)) {
		t.Error("staleness rules")
	}
}

func TestLoadAndRefresh(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	if r := Refresh(ctx, db, dir, now); r != nil {
		t.Fatalf("no samples, no fit: %+v", r)
	}
	ts := now.Add(-time.Hour).UnixMilli()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO limit_samples(ts, session_key, five_hour_pct, five_hour_resets_at, seven_day_pct, seven_day_resets_at) VALUES(?,?,?,?,?,?)`,
		ts, "c-1", 12.0, ts+3600_000, 40.0, ts+86_400_000)
	exec(`INSERT INTO limit_samples(ts, session_key, seven_day_pct, seven_day_resets_at) VALUES(?,?,?,?)`, ts+1, "c-1", 41.0, ts+86_400_000)
	exec(`INSERT INTO history_requests(request_key, source, session_key, ts, model, input_tokens, output_tokens, cache_read,
		cache_write_5m, cache_write_1h, cache_write_other, cost_usd) VALUES('k1','claude_code','c-1',?, 'claude-opus-5-5', 1, 2, 3, 4, 5, 6, 0.5)`, ts)
	exec(`INSERT INTO history_requests(request_key, source, session_key, ts, model, output_tokens) VALUES('k2','codex','o-1',?, 'gpt-5', 9)`, ts)
	exec(`INSERT INTO history_files(file_key, source, size, mtime, offset, parser_version, updated_at) VALUES('f','claude_code',1,1,1,3,?)`, ts+3600_000)
	d, err := Load(ctx, db, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Samples) != 2 || d.Samples[0].FiveHour == nil || *d.Samples[0].FiveHour != 12 || d.Samples[1].FiveHour != nil || *d.Samples[1].Week != 41 {
		t.Errorf("samples: %+v", d.Samples)
	}
	if len(d.Requests) != 1 {
		t.Fatalf("only Claude Code requests: %+v", d.Requests)
	}
	q := d.Requests[0]
	if q.Tokens != [NTypes]int64{1, 2, 3, 4, 5} || !q.Unsplit || !q.Priced || q.USD != 0.5 || q.Session != "c-1" {
		t.Errorf("request: %+v", q)
	}
	if d.HistoryThrough != ts+3600_000-holdBack {
		t.Errorf("history through %d", d.HistoryThrough)
	}
	r := Refresh(ctx, db, dir, now)
	if r == nil || r.FiveHour.Tier != TierNone || r.Samples != 2 || ReadCache(dir) == nil {
		t.Fatalf("refresh: %+v", r)
	}
	// Not stale: the saved fit is returned as is.
	again := Refresh(ctx, db, dir, now.Add(time.Minute))
	if again == nil || again.ComputedAt != r.ComputedAt {
		t.Errorf("a fresh fit should not be recomputed")
	}
}
