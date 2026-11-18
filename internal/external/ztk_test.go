package external

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/technique"
)

// ZTK facts verified on 2026-11-18 against github.com/codejunkie99/ztk at v0.3.1
// (README, LICENSE, releases page, src/hooks/claude.zig, src/version.zig).
func TestZTKManifestFacts(t *testing.T) {
	m := builtinByName(t, "ztk")
	if m.Version != "0.3.1" || m.License != "MIT" || ClassifyLicense(m.License) != LicensePermissive {
		t.Errorf("version %q license %q", m.Version, m.License)
	}
	if m.ActsAs != ActsHook || m.Category != CategoryShellCompressor || m.SupportsShadow {
		t.Errorf("ztk is a Bash hook in the shell_compressor slot with no shadow mode: %+v", m)
	}
	if m.Wire.HookEvent != "PreToolUse" || m.Wire.HookMatcher != "Bash" || m.Wire.HookCommand != "ztk rewrite" {
		t.Errorf("wire = %+v; ztk init -g writes PreToolUse/Bash/\"ztk rewrite\"", m.Wire)
	}
	if strings.Join(m.Verify.Argv, " ") != "ztk --version" || m.Verify.Contains != "ztk 0.3.1" {
		t.Errorf("verify = %+v", m.Verify)
	}
	if x, ok := technique.Get("ztk"); !ok || x.Tier != technique.T1 {
		t.Errorf("ztk must be registered as a T1 technique, got %+v", x)
	}
}

// Plan 9.1, done when: the conflict check allows only one owner per Bash call.
// Every tool or built-in that rewrites or shortens a Bash call is an owner; any two
// owners from different projects are refused, in both orders.
func TestOneOwnerPerBashCall(t *testing.T) {
	ms, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Manifest{}
	for _, m := range ms {
		byName[m.Name] = m
	}
	external := []string{"rtk", "ztk"}
	builtins := []string{"noise_normalizer", "output_trimmer", "hook_large_output", "hook_quiet_commands"}

	for _, a := range external {
		for _, b := range external {
			if a == b {
				continue
			}
			if cs := CheckEnable(byName[a], []Manifest{byName[b]}, nil); len(cs) != 1 || cs[0].Other != b {
				t.Errorf("enabling %s with %s on: conflicts %+v, want one naming %s", a, b, cs, b)
			}
		}
		for _, b := range builtins {
			if cs := CheckEnable(byName[a], nil, []string{b}); len(cs) != 1 || cs[0].Other != b || !cs[0].Builtin {
				t.Errorf("enabling %s with built-in %s on: conflicts %+v", a, b, cs)
			}
			// The other order: turning the built-in on while the tool is on (config check).
			errs := ValidateStates(map[string]string{Prefix + a: "on"}, ms, []string{b})
			if len(errs) != 1 {
				t.Errorf("built-in %s on with %s on: %v", b, a, errs)
			}
		}
	}
	// Trove's own Bash owners share one owner, so they never refuse each other.
	for _, a := range builtins {
		for _, b := range builtins {
			if a != b {
				if r := technique.Check([]string{a, b}, technique.Context{}); len(r.Conflicts) > 0 {
					t.Errorf("built-ins %s and %s must not conflict", a, b)
				}
			}
		}
	}
}

// A Bash hook the user installed by hand (`rtk init`, `ztk init -g`) also owns the
// call, so Trove refuses to add a second one.
func TestEnableRefusesWhenAnotherBashHookIsWiredByHand(t *testing.T) {
	ctx := context.Background()
	ms, _ := Builtin()
	env, r, _, claude := testEnv(t, ms...)
	r.out["ztk"] = "ztk 0.3.1\n"
	settings := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"rtk hook claude"}]}]}}`
	if err := os.WriteFile(filepath.Join(claude, "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := env.Install(ctx, "ztk", Approval{Tool: "ztk"}); err != nil {
		t.Fatal(err)
	}
	_, err := env.Enable(ctx, "ztk")
	c, ok := err.(Conflict)
	if !ok || c.Other != "rtk" || !strings.Contains(c.Reason, "settings.json") {
		t.Fatalf("want a Conflict naming the hand-wired rtk hook, got %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(claude, "settings.json")); string(got) != settings {
		t.Errorf("settings.json changed on a refused enable:\n%s", got)
	}

	// Without the foreign hook, ztk is wired as the only Bash owner.
	if err := os.WriteFile(filepath.Join(claude, "settings.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Enable(ctx, "ztk"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(claude, "settings.json"))
	if !strings.Contains(string(got), `"ztk rewrite"`) || !strings.Contains(string(got), `"Bash"`) {
		t.Errorf("ztk hook not wired:\n%s", got)
	}
}

// Headroom stays reference-only: its proxy claims the agent's endpoint, which the
// gateway owns, so the two are never chained.
func TestHeadroomProxyNeverChainedWithGateway(t *testing.T) {
	if _, err := Builtin(); err != nil {
		t.Fatal(err)
	}
	for _, m := range mustBuiltin(t) {
		if strings.HasPrefix(m.Name, "headroom") {
			t.Errorf("no Headroom manifest may ship: its only shell-output path is its proxy (%s)", m.Name)
		}
	}
	x, ok := technique.Get("headroom")
	if !ok || x.Status != technique.StatusReference {
		t.Fatalf("headroom must stay a reference entry: %+v", x)
	}
	if r := technique.Check([]string{"headroom", "duplicate_result_collapser"}, technique.Context{}); len(r.Conflicts) == 0 {
		t.Error("headroom's proxy and the gateway must conflict over the agent's endpoint")
	}
}

func mustBuiltin(t *testing.T) []Manifest {
	t.Helper()
	ms, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	return ms
}
