package optimize

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/usetrove/trove/internal/config"
)

func gwEngine(t *testing.T, state string, env map[string]string) (*Engine, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	if state != "" {
		cfg.Optimizers[config.OptimizerInflightDedup] = state
	}
	return New(Options{Config: cfg, KillFile: KillFile(dir), Getenv: func(k string) string { return env[k] }}), dir
}

func TestGatewayStateIsOffByDefaultAndShadowOnRequest(t *testing.T) {
	for state, want := range map[string]string{
		"":              config.StateOff,
		"off":           config.StateOff,
		"shadow":        config.StateShadow,
		"on":            config.StateShadow, // sharing a call is not built, so on only estimates
		"nonsense-text": config.StateOff,
	} {
		e, _ := gwEngine(t, state, nil)
		if got := e.GatewayState(config.OptimizerInflightDedup); got != want {
			t.Errorf("configured %q: effective %q, want %q", state, got, want)
		}
	}
}

func TestGatewayStateRespectsTheKillSwitchAndANilEngine(t *testing.T) {
	e, dir := gwEngine(t, "shadow", nil)
	if err := os.WriteFile(filepath.Join(dir, KillFileName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := e.GatewayState(config.OptimizerInflightDedup); got != config.StateOff {
		t.Errorf("kill file: %q, want off", got)
	}
	e2, _ := gwEngine(t, "shadow", map[string]string{config.EnvOptimize: "off"})
	if got := e2.GatewayState(config.OptimizerInflightDedup); got != config.StateOff {
		t.Errorf("kill environment variable: %q, want off", got)
	}
	var none *Engine
	if got := none.GatewayState(config.OptimizerInflightDedup); got != config.StateOff {
		t.Errorf("nil engine: %q, want off", got)
	}
}

func TestGatewayNamesAreNotInThePipeline(t *testing.T) {
	for _, g := range GatewayNames {
		for _, p := range PipelineNames {
			if g == p {
				t.Errorf("%s is both a gateway behavior and a pipeline optimizer", g)
			}
		}
	}
}
