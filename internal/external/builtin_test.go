package external

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func builtinByName(t *testing.T, name string) Manifest {
	t.Helper()
	ms, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.Name == name {
			return m
		}
	}
	t.Fatalf("no built-in manifest %q", name)
	return Manifest{}
}

func TestBuiltinManifestFacts(t *testing.T) {
	ms, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 3 {
		t.Fatalf("want rtk, terse-rules and token-savior, got %d manifests", len(ms))
	}
	for _, m := range ms {
		if m.SupportsShadow {
			t.Errorf("%s: none of the first three tools can run in shadow mode", m.Name)
		}
		if m.ShadowNote == "" || m.Claimed.Text == "" {
			t.Errorf("%s: needs a shadow note and a recorded claim", m.Name)
		}
	}
	rtk := builtinByName(t, "rtk")
	if !strings.Contains(strings.Join(rtk.Install.Argv, " "), "--tag v"+rtk.Version) {
		t.Errorf("rtk install must pin the tag: %v", rtk.Install.Argv)
	}
	if !strings.Contains(strings.Join(rtk.Install.Argv, " "), "--no-default-features") || ClassifyLicense(rtk.License) != LicensePermissive {
		t.Errorf("rtk is the Apache-only build: %v license %s", rtk.Install.Argv, rtk.License)
	}
	if !strings.Contains(strings.Join(builtinByName(t, "token-savior").Install.Argv, " "), "==4.24.0") {
		t.Error("token-savior install must pin the version")
	}
	// Both of these are code/shell tools in exclusive categories; the rules file is a third.
	if rtk.Category == builtinByName(t, "token-savior").Category {
		t.Error("rtk and token-savior must not conflict with each other")
	}
}

// The acceptance criterion: all three install with approval, enable, and come off
// cleanly, leaving the user's files exactly as they were.
func TestAllThreeInstallEnableRemoveCleanly(t *testing.T) {
	ctx := context.Background()
	ms, _ := Builtin()
	env, r, data, claude := testEnv(t, ms...)
	r.out["rtk"] = "rtk 1.1.0"
	r.out["pip"] = "Name: token-savior-recall\nVersion: 4.24.0\n"

	settings := filepath.Join(claude, "settings.json")
	md := filepath.Join(claude, "CLAUDE.md")
	origSettings := `{"hooks":{"PreToolUse":[{"matcher":"Write","hooks":[{"type":"command","command":"mine.sh"}]}]},"model":"x"}` + "\n"
	origMD := "# my rules\n"
	if err := os.WriteFile(settings, []byte(origSettings), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(md, []byte(origMD), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, n := range []string{"rtk", "token-savior", "terse-rules"} {
		if err := env.Install(ctx, n, Approval{Tool: n}); err != nil {
			t.Fatalf("install %s: %v", n, err)
		}
		if _, err := env.Enable(ctx, n); err != nil {
			t.Fatalf("enable %s: %v", n, err)
		}
	}
	gotSettings, _ := os.ReadFile(settings)
	if !strings.Contains(string(gotSettings), "rtk hook claude") || !strings.Contains(string(gotSettings), "mine.sh") {
		t.Fatalf("rtk hook missing or user hook lost:\n%s", gotSettings)
	}
	if got, _ := os.ReadFile(md); !strings.Contains(string(got), "Read only the lines you need") {
		t.Fatalf("rules not added:\n%s", got)
	}
	var sawAdd bool
	for _, c := range r.ran {
		if len(c) > 2 && c[0] == "claude" && c[1] == "mcp" && c[2] == "add" {
			sawAdd = true
		}
	}
	if !sawAdd {
		t.Fatalf("token-savior was not registered through the agent's CLI: %v", r.ran)
	}

	for _, n := range []string{"rtk", "token-savior", "terse-rules"} {
		if err := env.Remove(ctx, n, Approval{Tool: n}); err != nil {
			t.Fatalf("remove %s: %v", n, err)
		}
	}
	if got, _ := os.ReadFile(settings); string(got) != origSettings {
		t.Errorf("settings.json not restored byte for byte:\n%s", got)
	}
	if got, _ := os.ReadFile(md); string(got) != origMD {
		t.Errorf("CLAUDE.md not restored byte for byte:\n%q", got)
	}
	var removed [][]string
	for _, c := range r.ran {
		if (len(c) > 1 && c[0] == "claude" && c[2] == "remove") || (c[0] == "cargo" && c[1] == "uninstall") || (c[0] == "pip" && c[1] == "uninstall") {
			removed = append(removed, c)
		}
	}
	if len(removed) != 3 {
		t.Errorf("want the three uninstall/unregister commands, got %v", removed)
	}
	if l, _ := env.Store.List(ctx); len(l) != 0 {
		t.Errorf("records left behind: %+v", l)
	}
	if cs, _ := os.ReadDir(filepath.Join(data, "backups")); len(cs) != 0 {
		t.Errorf("backups should not outlive the changes they protect: %v", cs)
	}
}

func TestRTKRefusedWhileBuiltinCompressorOn(t *testing.T) {
	ctx := context.Background()
	ms, _ := Builtin()
	env, r, _, _ := testEnv(t, ms...)
	r.out["rtk"] = "rtk 1.1.0"
	env.BuiltinsOn = func() []string { return []string{"output_trimmer"} }
	if err := env.Install(ctx, "rtk", Approval{Tool: "rtk"}); err != nil {
		t.Fatal(err)
	}
	_, err := env.Enable(ctx, "rtk")
	c, ok := err.(Conflict)
	if !ok || !c.Builtin || c.Other != "output_trimmer" {
		t.Fatalf("want a conflict with the built-in output_trimmer, got %v", err)
	}
}

func TestMCPEnableNeedsAgentCLI(t *testing.T) {
	ctx := context.Background()
	ms, _ := Builtin()
	env, r, _, _ := testEnv(t, ms...)
	r.out["pip"] = "Version: 4.24.0"
	_ = env.Install(ctx, "token-savior", Approval{Tool: "token-savior"})
	r.missing["claude"] = true
	if _, err := env.Enable(ctx, "token-savior"); err == nil || !strings.Contains(err.Error(), "needs claude on your PATH") {
		t.Fatalf("got %v", err)
	}
	if rec, _, _ := env.Store.Get(ctx, "token-savior"); rec.State != "off" {
		t.Fatal("a failed enable must leave the tool off")
	}
}

func TestChargeForBuiltins(t *testing.T) {
	want := map[string]int{"rtk": 0, "token-savior": 1500}
	for n, w := range want {
		if got := ChargeFor(builtinByName(t, n)).PerRequest; got != w {
			t.Errorf("%s overhead = %d, want %d", n, got, w)
		}
	}
	rules := builtinByName(t, "terse-rules")
	c := ChargeFor(rules)
	if c.PerRequest != EstimateTokens(rules.Rules) || c.PerRequest == 0 || !reflect.DeepEqual(c.Label, "estimated") {
		t.Errorf("rules charge = %+v", c)
	}
}
