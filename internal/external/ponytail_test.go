package external

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/technique"
)

// The Ponytail rules are the project's AGENTS.md at tag v5.3.3 (sha256
// b98b15e1d683ee205e124b52b8d330128593f4a87b545cf8185e2712cd1bb25f), minus its
// last line, which only addresses agents working on the Ponytail repository.
func TestPonytailManifestFacts(t *testing.T) {
	m := builtinByName(t, "ponytail")
	if m.Version != "5.3.3" || m.License != "MIT" || ClassifyLicense(m.License) != LicensePermissive {
		t.Errorf("version %q license %q", m.Version, m.License)
	}
	if m.ActsAs != ActsRulesFile || m.Category != CategoryBehaviorRules || m.SupportsShadow {
		t.Errorf("ponytail is a rules block in the behavior_rules slot with no shadow mode: %+v", m)
	}
	if !strings.HasPrefix(m.Rules, "# Ponytail, lazy senior dev mode\n") || strings.Contains(m.Rules, "ponytail repo itself") {
		t.Errorf("rules text is not the pinned AGENTS.md block:\n%s", m.Rules)
	}
	if len(m.Install.Argv) != 0 || len(m.Remove.Argv) != 0 {
		t.Error("ponytail installs nothing; it is only a rules block")
	}
	// The MIT notice ships in the binary next to the text it covers.
	lic, err := manifestFS.ReadFile("manifests/ponytail.LICENSE")
	if err != nil || !strings.Contains(string(lic), "Copyright (c) 2026 DietrichGebert") {
		t.Fatalf("ponytail's MIT notice must be embedded: %v", err)
	}
	if x, ok := technique.Get("ponytail"); !ok || x.Tier != technique.T2 {
		t.Errorf("ponytail must be registered as a T2 behavioral technique, got %+v", x)
	}
}

// Done when: installed and removed cleanly, with overhead counted per request.
func TestPonytailInstallEnableRemoveCleanly(t *testing.T) {
	ctx := context.Background()
	ms, _ := Builtin()
	env, r, data, claude := testEnv(t, ms...)
	md := filepath.Join(claude, "CLAUDE.md")
	orig := "# my rules\nno tabs\n"
	if err := os.WriteFile(md, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := env.PlanInstall(ctx, "ponytail")
	if err != nil {
		t.Fatal(err)
	}
	m := builtinByName(t, "ponytail")
	block := RulesBlock(m)
	if p.OverheadTokens != EstimateTokens(block) || p.OverheadTokens <= EstimateTokens(m.Rules) {
		t.Errorf("overhead %d must count the whole managed block (%d tokens), markers included", p.OverheadTokens, EstimateTokens(block))
	}
	if err := env.Install(ctx, "ponytail", Approval{Tool: "ponytail"}); err != nil {
		t.Fatal(err)
	}
	ep, err := env.PlanEnable(ctx, "ponytail")
	if err != nil || len(ep.Edits) != 1 || ep.Edits[0].Path != md {
		t.Fatalf("want one edit to CLAUDE.md, got %+v %v", ep.Edits, err)
	}
	if _, err := env.Enable(ctx, "ponytail"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(md)
	if !strings.HasPrefix(string(got), orig) || !strings.Contains(string(got), ManagedMarker) || !strings.Contains(string(got), "lazy senior developer") {
		t.Fatalf("managed block not appended after the user's text:\n%s", got)
	}
	if strings.Count(string(got), ManagedMarker) != 1 {
		t.Errorf("exactly one managed marker expected:\n%s", got)
	}
	if err := env.Remove(ctx, "ponytail", Approval{Tool: "ponytail"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(md); string(got) != orig {
		t.Errorf("CLAUDE.md not restored byte for byte:\n%q", got)
	}
	if len(r.ran) != 0 {
		t.Errorf("a rules block runs no commands, ran %v", r.ran)
	}
	if cs, _ := os.ReadDir(filepath.Join(data, "backups")); len(cs) != 0 {
		t.Errorf("backups left behind: %v", cs)
	}
}

func TestPonytailAndTerseRulesNeverBothOn(t *testing.T) {
	ctx := context.Background()
	ms, _ := Builtin()
	env, _, _, _ := testEnv(t, ms...)
	for _, n := range []string{"terse-rules", "ponytail"} {
		if err := env.Install(ctx, n, Approval{Tool: n}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := env.Enable(ctx, "terse-rules"); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Enable(ctx, "ponytail"); err == nil {
		t.Fatal("two output-style rule sets must not be on at once")
	} else if _, ok := err.(Conflict); !ok {
		t.Fatalf("want a Conflict, got %v", err)
	}
}

func TestPonytailRulesHashPinned(t *testing.T) {
	// Guards against an accidental edit of the embedded text: a change must come
	// with a new pinned version and a re-verified source.
	sum := sha256.Sum256([]byte(builtinByName(t, "ponytail").Rules))
	const want = "ca99e5958383063029b206c7c50a0eaad1c29914a362bedd46033ec16b537e1e"
	if got := hex.EncodeToString(sum[:]); got != want {
		t.Errorf("ponytail rules hash = %s; update the pin only after re-verifying the source", got)
	}
}
