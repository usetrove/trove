package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
