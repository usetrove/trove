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

// The Caveman rules are skills/caveman/SKILL.md at tag v4.1.0 (sha256
// 415d43518f2b1a9498c15a445d4657117b129ee5e81b19454ce348439cc6672b) without its
// frontmatter and the paragraph about commands Trove does not install, with a
// first line stating that change (Apache-2.0 section 4(b)).
func TestCavemanManifestFacts(t *testing.T) {
	m := builtinByName(t, "caveman")
	if m.Version != "4.1.0" || m.License != "Apache-2.0" || ClassifyLicense(m.License) != LicensePermissive {
		t.Errorf("version %q license %q", m.Version, m.License)
	}
	if m.ActsAs != ActsRulesFile || m.Category != CategoryBehaviorRules || m.SupportsShadow {
		t.Errorf("caveman is a rules block in the behavior_rules slot with no shadow mode: %+v", m)
	}
	if !strings.HasPrefix(m.Rules, "Adapted by Trove from Caveman v4.1.0") || !strings.Contains(m.Rules, "\n# caveman\n") {
		t.Errorf("rules text must open with the change notice, then the skill body:\n%.200s", m.Rules)
	}
	for _, gone := range []string{"name: caveman", "/caveman ultra", "ultracave", "megacave", "/caveman status"} {
		if strings.Contains(m.Rules, gone) {
			t.Errorf("rules text still carries %q; only the skill's rules are wrapped", gone)
		}
	}
	if len(m.Install.Argv) != 0 || len(m.Remove.Argv) != 0 {
		t.Error("caveman installs nothing; its proxy, CLI and Auto mode are never wrapped")
	}
	for f, want := range map[string]string{
		"manifests/caveman.LICENSE": "Apache License",
		"manifests/caveman.NOTICE":  "Copyright 2026 Julius Brussee",
	} {
		b, err := manifestFS.ReadFile(f)
		if err != nil || !strings.Contains(string(b), want) {
			t.Errorf("%s must be embedded and contain %q: %v", f, want, err)
		}
	}
	if x, ok := technique.Get("caveman"); !ok || x.Tier != technique.T2 || x.Status != technique.StatusWrapped {
		t.Errorf("caveman must be registered as a wrapped T2 behavioral technique, got %+v", x)
	}
}

// Done when: installed and removed cleanly, with overhead counted per request.
func TestCavemanInstallEnableRemoveCleanly(t *testing.T) {
	ctx := context.Background()
	ms, _ := Builtin()
	env, r, data, claude := testEnv(t, ms...)
	md := filepath.Join(claude, "CLAUDE.md")
	orig := "# my rules\nno tabs\n"
	if err := os.WriteFile(md, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := env.PlanInstall(ctx, "caveman")
	if err != nil {
		t.Fatal(err)
	}
	block := RulesBlock(builtinByName(t, "caveman"))
	if p.OverheadTokens != EstimateTokens(block) {
		t.Errorf("overhead %d must count the whole managed block (%d tokens)", p.OverheadTokens, EstimateTokens(block))
	}
	if err := env.Install(ctx, "caveman", Approval{Tool: "caveman"}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Enable(ctx, "caveman"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(md)
	if !strings.HasPrefix(string(got), orig) || strings.Count(string(got), ManagedMarker) != 1 || !strings.Contains(string(got), "Respond terse like smart caveman.") {
		t.Fatalf("managed block not appended once after the user's text:\n%s", got)
	}
	if err := env.Remove(ctx, "caveman", Approval{Tool: "caveman"}); err != nil {
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

func TestCavemanRulesHashPinned(t *testing.T) {
	sum := sha256.Sum256([]byte(builtinByName(t, "caveman").Rules))
	const want = "836fa53130a20d1bac5f4e27b2bd84325fd1bad416c37a8131b5751e07daaa31"
	if got := hex.EncodeToString(sum[:]); got != want {
		t.Errorf("caveman rules hash = %s; update the pin only after re-verifying the source", got)
	}
}

// 6.3: only one output-style rule set may be on at a time, whichever pair is tried.
func TestOnlyOneOutputStyleRuleSetAtATime(t *testing.T) {
	sets := []string{"caveman", "ponytail", "terse-rules"}
	for _, first := range sets {
		for _, second := range sets {
			if first == second {
				continue
			}
			t.Run(first+"_then_"+second, func(t *testing.T) {
				ctx := context.Background()
				ms, _ := Builtin()
				env, _, _, _ := testEnv(t, ms...)
				for _, n := range sets {
					if err := env.Install(ctx, n, Approval{Tool: n}); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := env.Enable(ctx, first); err != nil {
					t.Fatal(err)
				}
				_, err := env.Enable(ctx, second)
				if c, ok := err.(Conflict); !ok || c.Other != first {
					t.Fatalf("want a Conflict naming %s, got %v", first, err)
				}
			})
		}
	}
}
