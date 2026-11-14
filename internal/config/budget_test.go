package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSetBudgetKeepsEverythingElse(t *testing.T) {
	dir := t.TempDir()
	custom := strings.Replace(DefaultTOML, "port = 8787", "port = 9001", 1)
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(custom), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetBudget(dir, "weekly", 250); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Budget.Period != "weekly" || cfg.Budget.CapUSD != 250 || cfg.Budget.WarnAtPercent != 80 || cfg.Gateway.Port != 9001 {
		t.Errorf("unexpected config: %+v", cfg)
	}
	b, _ := os.ReadFile(filepath.Join(dir, FileName))
	want := strings.NewReplacer(`period = "monthly"`, `period = "weekly"`, "cap_usd = 0.0", "cap_usd = 250.0").Replace(custom)
	if string(b) != want {
		t.Errorf("file changed beyond the two budget lines:\n%s", b)
	}
}

func TestSetBudgetCreatesFileAndHandlesMissingSection(t *testing.T) {
	dir := t.TempDir()
	if err := SetBudget(dir, "monthly", 12.5); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir, func(string) string { return "" })
	if err != nil || cfg.Budget.CapUSD != 12.5 {
		t.Fatalf("cfg %+v err %v", cfg, err)
	}

	d2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(d2, FileName), []byte("retention_days = 30\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetBudget(d2, "daily", 5); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(d2, func(string) string { return "" })
	if err != nil || cfg.Budget.Period != "daily" || cfg.RetentionDays != 30 {
		t.Fatalf("cfg %+v err %v", cfg, err)
	}
}

func TestSetBudgetRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	if err := SetBudget(dir, "yearly", 10); err == nil {
		t.Error("want an error for an unknown period")
	}
	if err := SetBudget(dir, "monthly", -1); err == nil {
		t.Error("want an error for a negative amount")
	}
}

func TestBudgetWindow(t *testing.T) {
	loc := time.FixedZone("test", -8*3600)
	cases := []struct {
		period   string
		now      time.Time
		from, to time.Time
	}{
		{"daily", time.Date(2026, 11, 13, 14, 30, 0, 0, loc), time.Date(2026, 11, 13, 0, 0, 0, 0, loc), time.Date(2026, 11, 14, 0, 0, 0, 0, loc)},
		{"daily", time.Date(2026, 11, 13, 0, 0, 0, 0, loc), time.Date(2026, 11, 13, 0, 0, 0, 0, loc), time.Date(2026, 11, 14, 0, 0, 0, 0, loc)},
		{"daily", time.Date(2026, 12, 31, 23, 59, 59, 0, loc), time.Date(2026, 12, 31, 0, 0, 0, 0, loc), time.Date(2027, 1, 1, 0, 0, 0, 0, loc)},
		// 2026-11-13 is a Friday; weeks start on Monday the 9th. On a Monday the week starts that day, on a Sunday six days back.
		{"weekly", time.Date(2026, 11, 13, 14, 30, 0, 0, loc), time.Date(2026, 11, 9, 0, 0, 0, 0, loc), time.Date(2026, 11, 16, 0, 0, 0, 0, loc)},
		{"weekly", time.Date(2026, 11, 9, 8, 0, 0, 0, loc), time.Date(2026, 11, 9, 0, 0, 0, 0, loc), time.Date(2026, 11, 16, 0, 0, 0, 0, loc)},
		{"weekly", time.Date(2026, 11, 15, 22, 0, 0, 0, loc), time.Date(2026, 11, 9, 0, 0, 0, 0, loc), time.Date(2026, 11, 16, 0, 0, 0, 0, loc)},
		{"monthly", time.Date(2026, 11, 13, 14, 30, 0, 0, loc), time.Date(2026, 11, 1, 0, 0, 0, 0, loc), time.Date(2026, 12, 1, 0, 0, 0, 0, loc)},
		{"monthly", time.Date(2026, 12, 31, 23, 0, 0, 0, loc), time.Date(2026, 12, 1, 0, 0, 0, 0, loc), time.Date(2027, 1, 1, 0, 0, 0, 0, loc)},
		{"", time.Date(2026, 2, 10, 1, 0, 0, 0, loc), time.Date(2026, 2, 1, 0, 0, 0, 0, loc), time.Date(2026, 3, 1, 0, 0, 0, 0, loc)}, // the default period is monthly
	}
	for _, c := range cases {
		from, to := Budget{Period: c.period}.Window(c.now)
		if !from.Equal(c.from) || !to.Equal(c.to) {
			t.Errorf("%s at %v: %v to %v, want %v to %v", c.period, c.now, from, to, c.from, c.to)
		}
		if c.now.Before(from) || !c.now.Before(to) {
			t.Errorf("%s at %v: the window does not contain now", c.period, c.now)
		}
	}
}

func TestGatewayBehaviorsAreKnown(t *testing.T) {
	for _, n := range []string{OptimizerInflightDedup, OptimizerSpendCap} {
		if !IsGatewayBehavior(n) {
			t.Errorf("%s should be a gateway behavior", n)
		}
	}
	for _, n := range []string{"noise_normalizer", "output_trimmer", "", "nope"} {
		if IsGatewayBehavior(n) {
			t.Errorf("%q is not a gateway behavior", n)
		}
	}
}
