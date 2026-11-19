package report

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
)

const shadowInflight = `{"label":"estimated","optimizers":{"inflight_dedup":{"tokens":4020,"usd":0.50}}}`

func dedupRow(r Report) *OptimizerRow {
	for i := range r.Optimizers.Rows {
		if r.Optimizers.Rows[i].Name == config.OptimizerInflightDedup {
			return &r.Optimizers.Rows[i]
		}
	}
	return nil
}

// The in-flight dedup estimate shows in the report as an estimate with an honest note, is never
// offered as something to switch on, and stays out of the report entirely when it is off and empty.
func TestReportShowsInflightDedupAsAnEstimateOnlyAndNeverOffersIt(t *testing.T) {
	db := openDB(t)
	installedDaysAgo(t, db, 30)
	for i := 0; i < 40; i++ {
		r := req{ts: now.Add(-time.Duration(i+1) * time.Hour), session: fmt.Sprintf("s%02d", i), agent: "claude_code", model: "claude-a", cost: 1.0}
		if i < 25 {
			r.shadow = shadowInflight
		}
		addReq(t, db, r)
	}
	p := mustPeriod(t, Month)
	for _, state := range []string{"shadow", "on"} {
		cfg := config.Default()
		cfg.Optimizers[config.OptimizerInflightDedup] = state
		r, err := Build(context.Background(), db, Inputs{Config: cfg, Now: now}, p)
		if err != nil {
			t.Fatal(err)
		}
		row := dedupRow(r)
		if row == nil {
			t.Fatalf("%s: no inflight_dedup row", state)
		}
		if row.Effective != config.StateShadow {
			t.Errorf("%s: effective %q; sharing a call is not built, so it can only be shadow", state, row.Effective)
		}
		if row.WouldSave == nil || row.WouldSave.Label != "estimated" || row.WouldSave.Value < 12 {
			t.Errorf("%s: would-save %+v, want an estimated figure of about $12.50", state, row.WouldSave)
		}
		if row.Saved != nil || row.Measured != nil {
			t.Errorf("%s: it claims a saving it did not make: %+v %+v", state, row.Saved, row.Measured)
		}
		if row.Enable != "" {
			t.Errorf("%s: offered to switch on something that cannot be switched on: %q", state, row.Enable)
		}
		if !strings.Contains(row.Note, "Sharing the call is not built") {
			t.Errorf("%s: note %q", state, row.Note)
		}
	}
	// Off, but with an estimate already recorded: it still shows, so the figure is not lost.
	cfg := config.Default()
	r, err := Build(context.Background(), db, Inputs{Config: cfg, Now: now}, p)
	if err != nil {
		t.Fatal(err)
	}
	if row := dedupRow(r); row == nil || row.WouldSave == nil {
		t.Errorf("an estimate that was recorded disappeared when the setting went back to off: %+v", row)
	}
}

func TestReportLeavesOutInflightDedupWhenItIsOffAndHasNothing(t *testing.T) {
	db := openDB(t)
	seedNormal(t, db)
	r, err := Build(context.Background(), db, Inputs{Config: testConfig(), Now: now}, mustPeriod(t, Month))
	if err != nil {
		t.Fatal(err)
	}
	if dedupRow(r) != nil {
		t.Error("a default report mentions in-flight dedup")
	}
}

// Plan item 4.4: sharing a call is built only if shadow shows more than 1% of gateway spend. The row
// states the share, labeled estimated, and which side of that bar it is on.
func TestReportStatesInflightDedupShareAgainstTheOnePercentBar(t *testing.T) {
	cases := []struct {
		name      string
		flagged   int
		total     int
		wantPct   float64
		wantAbove bool
	}{
		{"well above", 25, 40, 31.25, true}, // $12.50 of $40
		{"below", 1, 200, 0.25, false},      // $0.50 of $200
		{"exactly 1% is not above", 2, 100, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openDB(t)
			installedDaysAgo(t, db, 30)
			for i := 0; i < tc.total; i++ {
				r := req{ts: now.Add(-time.Duration(i+1) * time.Minute * 10), session: fmt.Sprintf("s%03d", i), agent: "claude_code", model: "claude-a", cost: 1.0}
				if i < tc.flagged {
					r.shadow = shadowInflight
				}
				addReq(t, db, r)
			}
			cfg := config.Default()
			cfg.Optimizers[config.OptimizerInflightDedup] = "shadow"
			r, err := Build(context.Background(), db, Inputs{Config: cfg, Now: now}, mustPeriod(t, Month))
			if err != nil {
				t.Fatal(err)
			}
			row := dedupRow(r)
			if row == nil || row.SpendShare == nil {
				t.Fatalf("no share of spend on the row: %+v", row)
			}
			if row.SpendShare.Label != "estimated" {
				t.Errorf("label %q, want estimated", row.SpendShare.Label)
			}
			if d := row.SpendShare.Value - tc.wantPct; d > 0.01 || d < -0.01 {
				t.Errorf("share %.4f%%, want %.2f%%", row.SpendShare.Value, tc.wantPct)
			}
			above := strings.Contains(row.Note, "above the 1% bar")
			below := strings.Contains(row.Note, "below the 1% bar")
			if above != tc.wantAbove || below == tc.wantAbove {
				t.Errorf("note %q; want above=%v", row.Note, tc.wantAbove)
			}
		})
	}
}

const shadowCap = `{"label":"estimated","optimizers":{"spend_cap":{"tokens":4020,"usd":0.50}}}`

func gatewayRow(r Report, name string) *OptimizerRow {
	for i := range r.Optimizers.Rows {
		if r.Optimizers.Rows[i].Name == name {
			return &r.Optimizers.Rows[i]
		}
	}
	return nil
}

// What a hard spend cap would have stopped shows as an estimate with an honest note, is never offered
// as something to switch on, and stays out of a default report.
func TestReportShowsTheSpendCapAsAnEstimateOnly(t *testing.T) {
	db := openDB(t)
	installedDaysAgo(t, db, 30)
	for i := 0; i < 40; i++ {
		r := req{ts: now.Add(-time.Duration(i+1) * time.Hour), session: fmt.Sprintf("s%02d", i), agent: "claude_code", model: "claude-a", cost: 1.0}
		if i < 10 {
			r.shadow = shadowCap
		}
		addReq(t, db, r)
	}
	for _, state := range []string{"shadow", "on"} {
		cfg := config.Default()
		cfg.Optimizers[config.OptimizerSpendCap] = state
		r, err := Build(context.Background(), db, Inputs{Config: cfg, Now: now}, mustPeriod(t, Month))
		if err != nil {
			t.Fatal(err)
		}
		row := gatewayRow(r, config.OptimizerSpendCap)
		if row == nil {
			t.Fatalf("%s: no spend_cap row", state)
		}
		// "on" is active (plan 8.1) but keeps marking requests it did not refuse, so the estimate stays.
		if row.Effective != state || row.WouldSave == nil || row.WouldSave.Label != "estimated" || row.WouldSave.Value < 4.9 {
			t.Errorf("%s: row %+v would-save %+v; want %s and an estimated $5.00", state, row, row.WouldSave, state)
		}
		if row.Saved != nil || row.Measured != nil || row.Enable != "" {
			t.Errorf("%s: it claims a saving or offers to switch on: %+v", state, row)
		}
		want := map[string]string{"shadow": "no request was refused", "on": "trove budget probe"}[state]
		if !strings.Contains(row.Note, want) {
			t.Errorf("%s: note %q lacks %q", state, row.Note, want)
		}
		if d := gatewayRow(r, config.OptimizerInflightDedup); d != nil {
			t.Errorf("%s: in-flight dedup appears although it has no estimate and is off", state)
		}
	}
	empty := openDB(t)
	seedNormal(t, empty)
	r, err := Build(context.Background(), empty, Inputs{Config: testConfig(), Now: now}, mustPeriod(t, Month))
	if err != nil {
		t.Fatal(err)
	}
	if gatewayRow(r, config.OptimizerSpendCap) != nil {
		t.Error("a default report mentions the spend cap")
	}
}
