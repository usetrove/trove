package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/optimize"
)

func TestOptimizeStatus(t *testing.T) {
	dir := t.TempDir()
	cfg := "[optimizers]\nnoise_normalizer = \"on\"\noutput_trimmer = \"shadow\"\nmystery = \"on\"\n[optimize]\ncustom_agents = [\"sdk\"]\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, nil)
	if code := h.run("optimize", "status", "--json", "--data-dir", dir); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	var st optimizeStatus
	if err := json.Unmarshal(h.out.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, o := range st.Optimizers {
		order = append(order, o.Name)
	}
	if strings.Join(order, ",") != strings.Join(optimize.PipelineNames, ",") {
		t.Errorf("status must list optimizers in run order: %v", order)
	}
	by := map[string]optimizerInfo{}
	for _, o := range st.Optimizers {
		by[o.Name] = o
	}
	if by["noise_normalizer"].Effective != "on" || by["duplicate_result_collapser"].Effective != "off" || by["output_trimmer"].Effective != "shadow" {
		t.Errorf("states wrong: %+v", by)
	}
	if by["noise_normalizer"].Tier != "lossless" || by["output_trimmer"].Tier != "recoverable" {
		t.Errorf("tiers wrong: %+v", by)
	}
	if len(st.Unknown) != 1 || st.Unknown[0] != "mystery" || st.KillSwitch.On || len(st.CustomAgents) != 1 {
		t.Errorf("status: %+v", st)
	}

	h = newHarness(t, nil)
	h.run("optimize", "status", "--data-dir", dir)
	for _, want := range []string{"RUN ORDER", "volatile_prefix_mover", "custom agents only", "Ignored (no such optimizer): mystery", "next session"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("text output lacks %q:\n%s", want, h.out)
		}
	}
}

func TestOptimizeOffAndResume(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fresh")
	h := newHarness(t, nil)
	if code := h.run("optimize", "off", "--json", "--data-dir", dir); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if on, _ := optimize.Disabled(optimize.KillFile(dir), func(string) string { return "" }); !on {
		t.Fatal("the kill switch file was not written")
	}
	h = newHarness(t, nil)
	h.run("optimize", "status", "--json", "--data-dir", dir)
	var st optimizeStatus
	if err := json.Unmarshal(h.out.Bytes(), &st); err != nil || !st.KillSwitch.On {
		t.Errorf("status should report the kill switch: %v %+v", err, st.KillSwitch)
	}
	h = newHarness(t, nil)
	if code := h.run("optimize", "resume", "--data-dir", dir); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if on, _ := optimize.Disabled(optimize.KillFile(dir), func(string) string { return "" }); on {
		t.Error("resume did not remove the kill switch")
	}
	// Resuming twice is fine.
	if code := newHarness(t, nil).run("optimize", "resume", "--data-dir", dir); code != 0 {
		t.Errorf("second resume exit %d", code)
	}
}

func TestOptimizeResumeWarnsWhenEnvVarStillSet(t *testing.T) {
	h := newHarness(t, map[string]string{"TROVE_OPTIMIZE": "off"})
	if code := h.run("optimize", "resume", "--data-dir", t.TempDir()); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(h.out.String(), "TROVE_OPTIMIZE is still set to off") {
		t.Errorf("should warn that the env var still disables optimizers: %s", h.out)
	}
}

func TestOptimizeSuggestWithNoData(t *testing.T) {
	h := newHarness(t, nil)
	if code := h.run("optimize", "suggest", "--json", "--data-dir", filepath.Join(t.TempDir(), "none")); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	var out suggestOutput
	if err := json.Unmarshal(h.out.Bytes(), &out); err != nil || out.Suggestions == nil || len(out.Suggestions) != 0 || !strings.HasPrefix(out.Basis, "estimated") {
		t.Errorf("%v %+v", err, out)
	}
	if code := newHarness(t, nil).run("optimize", "suggest", "--days", "0"); code == 0 {
		t.Error("--days 0 should be rejected")
	}
}

func TestOptimizeKillSwitchPathMatchesTheGateway(t *testing.T) {
	// The gateway builds its kill-switch path with optimize.KillFile(dir), and
	// the CLI writes the same file; pin the name so they cannot drift apart.
	if got := filepath.Base(optimize.KillFile("x")); got != "optimize.off" {
		t.Errorf("kill file name = %q", got)
	}
}

// In-flight dedup is a gateway behavior, not a pipeline step. It must not join the run order
// that `optimizers` promises, and it must not be reported as an unknown optimizer.
func TestOptimizeStatusListsGatewayBehaviorsApartFromTheRunOrder(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[optimizers]\ninflight_dedup = \"on\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, nil)
	if code := h.run("optimize", "status", "--json", "--data-dir", dir); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	var st optimizeStatus
	if err := json.Unmarshal(h.out.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Optimizers) != len(optimize.PipelineNames) {
		t.Errorf("the run order has %d entries, want the pipeline's %d", len(st.Optimizers), len(optimize.PipelineNames))
	}
	if len(st.Gateway) != len(optimize.GatewayNames) {
		t.Errorf("%d gateway behaviors listed, want %d: %+v", len(st.Gateway), len(optimize.GatewayNames), st.Gateway)
	}
	found := false
	for _, g := range st.Gateway {
		if g.Name == "inflight_dedup" {
			found = true
			if g.Configured != "on" || g.Effective != "shadow" {
				t.Errorf("inflight_dedup %+v; \"on\" must run as shadow because sharing a call is not built", g)
			}
		}
	}
	if !found {
		t.Errorf("inflight_dedup is not listed: %+v", st.Gateway)
	}
	if len(st.Unknown) != 0 {
		t.Errorf("inflight_dedup was reported as an unknown optimizer: %v", st.Unknown)
	}
	h = newHarness(t, nil)
	h.run("optimize", "status", "--data-dir", dir)
	out := h.out.String()
	if !strings.Contains(out, "Gateway behaviors") || !strings.Contains(out, "inflight_dedup") || !strings.Contains(out, `"on" runs as shadow`) {
		t.Errorf("text output lacks the gateway section:\n%s", out)
	}
}

func TestOptimizeStatusSaysWhatTheSpendCapNeeds(t *testing.T) {
	status := func(toml string) optimizeStatus {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(toml), 0o600); err != nil {
			t.Fatal(err)
		}
		h := newHarness(t, nil)
		if code := h.run("optimize", "status", "--json", "--data-dir", dir); code != 0 {
			t.Fatalf("exit %d: %s", code, h.err)
		}
		var st optimizeStatus
		if err := json.Unmarshal(h.out.Bytes(), &st); err != nil {
			t.Fatal(err)
		}
		return st
	}
	find := func(st optimizeStatus) optimizerInfo {
		for _, g := range st.Gateway {
			if g.Name == "spend_cap" {
				return g
			}
		}
		t.Fatalf("no spend_cap in %+v", st.Gateway)
		return optimizerInfo{}
	}
	noCap := find(status("[optimizers]\nspend_cap = \"shadow\"\n"))
	if noCap.Effective != "shadow" || !strings.Contains(noCap.Note, "trove budget set") {
		t.Errorf("without a cap: %+v; it must say a budget is needed", noCap)
	}
	withCap := find(status("[optimizers]\nspend_cap = \"on\"\n[budget]\nperiod = \"daily\"\ncap_usd = 10.0\n"))
	if withCap.Effective != "shadow" || strings.Contains(withCap.Note, "trove budget set") || !strings.Contains(withCap.Note, "pausing is not built") {
		t.Errorf("with a cap: %+v; \"on\" must run as shadow and the note must say pausing is not built", withCap)
	}
	if st := status("[optimizers]\nspend_cap = \"shadow\"\n"); len(st.Unknown) != 0 {
		t.Errorf("spend_cap was reported as an unknown optimizer: %v", st.Unknown)
	}
}
