package external

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/store"
)

type fakeRunner struct {
	ran     [][]string
	out     map[string]string // first argv element -> output
	fail    map[string]bool
	missing map[string]bool
}

func (f *fakeRunner) Run(_ context.Context, argv []string) (string, error) {
	f.ran = append(f.ran, argv)
	if f.fail[argv[0]] {
		return "boom token=sk-ant-api03-SECRETSECRETSECRET1234567890", errors.New("exit 1")
	}
	return f.out[argv[0]], nil
}

func (f *fakeRunner) LookPath(n string) (string, error) {
	if f.missing[n] {
		return "", errors.New("not found")
	}
	return "/bin/" + n, nil
}

type memStore struct{ m map[string]store.ExternalTool }

func (s *memStore) Put(_ context.Context, t store.ExternalTool) error { s.m[t.Name] = t; return nil }
func (s *memStore) Get(_ context.Context, n string) (store.ExternalTool, bool, error) {
	t, ok := s.m[n]
	return t, ok, nil
}
func (s *memStore) List(context.Context) ([]store.ExternalTool, error) {
	var out []store.ExternalTool
	for _, t := range s.m {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
func (s *memStore) SetState(_ context.Context, n, st string) (bool, error) {
	t, ok := s.m[n]
	t.State = st
	s.m[n] = t
	return ok, nil
}
func (s *memStore) Delete(_ context.Context, n string) error { delete(s.m, n); return nil }

func testEnv(t *testing.T, ms ...Manifest) (Env, *fakeRunner, string, string) {
	t.Helper()
	data, claude := t.TempDir(), t.TempDir()
	r := &fakeRunner{out: map[string]string{}, fail: map[string]bool{}, missing: map[string]bool{}}
	return Env{DataDir: data, ClaudeDir: claude, Runner: r, Store: &memStore{m: map[string]store.ExternalTool{}},
		Now: func() time.Time { return time.Unix(1_700_000_000, 0) }, Manifests: ms}, r, data, claude
}

func rulesTool(name string) Manifest {
	return Manifest{Name: name, Version: "1.0.0", License: "Trove-project", SourceURL: "https://x", VerifiedOn: "2026-11-04",
		ActsAs: ActsRulesFile, Category: CategoryBehaviorRules, Points: []string{"request"}, Rules: "Be brief.\n",
		Claimed: Claim{Text: "c", SourceURL: "https://x"}}
}

func shellTool(name string, lic string) Manifest {
	return Manifest{Name: name, Version: "2.0.0", License: lic, SourceURL: "https://x", VerifiedOn: "2026-11-04",
		ActsAs: ActsShellWrapper, Category: CategoryShellCompressor, Points: []string{"tool_output"},
		Install: Command{Argv: []string{"pkg", "install", name}, Requires: []string{"pkg"}},
		Remove:  Command{Argv: []string{"pkg", "remove", name}},
		Verify:  Verify{Argv: []string{name, "--version"}, Contains: "2.0.0"}}
}

func TestInstallNeedsApprovalAndLicenseConfirm(t *testing.T) {
	ctx := context.Background()
	env, r, _, _ := testEnv(t, shellTool("sq", "Elastic-2.0"))
	r.out["sq"] = "sq 2.0.0"
	if err := env.Install(ctx, "sq", Approval{}); err == nil || len(r.ran) != 0 {
		t.Fatalf("install without approval must not run anything: err=%v ran=%v", err, r.ran)
	}
	if err := env.Install(ctx, "sq", Approval{Tool: "sq"}); err == nil || !strings.Contains(err.Error(), "Type its name") || len(r.ran) != 0 {
		t.Fatalf("restricted license needs typed confirmation: err=%v ran=%v", err, r.ran)
	}
	if err := env.Install(ctx, "sq", Approval{Tool: "sq", Typed: "sq"}); err != nil {
		t.Fatal(err)
	}
	rec, ok, _ := env.Store.Get(ctx, "sq")
	if !ok || rec.State != "off" || rec.Version != "2.0.0" {
		t.Fatalf("record = %+v ok=%v", rec, ok)
	}
	if len(r.ran) != 2 || r.ran[0][0] != "pkg" || r.ran[1][0] != "sq" {
		t.Fatalf("ran = %v", r.ran)
	}
}

func TestInstallFailureRecordsNothing(t *testing.T) {
	ctx := context.Background()
	env, r, _, _ := testEnv(t, shellTool("sq", "MIT"))
	ap := Approval{Tool: "sq"}

	r.missing["pkg"] = true
	if err := env.Install(ctx, "sq", ap); err == nil || !strings.Contains(err.Error(), "needs pkg") {
		t.Fatalf("missing requirement: %v", err)
	}
	r.missing["pkg"] = false

	r.fail["pkg"] = true
	err := env.Install(ctx, "sq", ap)
	if err == nil || strings.Contains(err.Error(), "SECRETSECRET") {
		t.Fatalf("install failure must be reported with secrets redacted: %v", err)
	}
	r.fail["pkg"] = false

	r.out["sq"] = "sq 9.9.9" // wrong version landed
	if err := env.Install(ctx, "sq", ap); err == nil || !strings.Contains(err.Error(), "did not report version") {
		t.Fatalf("version mismatch: %v", err)
	}
	if l, _ := env.Store.List(ctx); len(l) != 0 {
		t.Fatalf("nothing should be recorded, got %+v", l)
	}
}

func TestEnableDisableRulesIsByteExact(t *testing.T) {
	ctx := context.Background()
	env, _, _, claude := testEnv(t, rulesTool("rules"))
	path := filepath.Join(claude, "CLAUDE.md")
	orig := "# My rules\n\nKeep tests green.\n"
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := env.Install(ctx, "rules", Approval{Tool: "rules"}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Enable(ctx, "rules"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "Be brief.") || !strings.HasPrefix(string(got), orig) {
		t.Fatalf("rules block not appended after the user's text:\n%s", got)
	}
	if _, err := env.Disable(ctx, "rules"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != orig {
		t.Fatalf("disable did not restore the original byte for byte:\n%q", got)
	}
	if rec, _, _ := env.Store.Get(ctx, "rules"); rec.State != "off" {
		t.Fatalf("state = %s", rec.State)
	}
}

func TestDisableAfterUserEditKeepsUserLines(t *testing.T) {
	ctx := context.Background()
	env, _, _, claude := testEnv(t, rulesTool("rules"))
	path := filepath.Join(claude, "CLAUDE.md")
	_ = os.WriteFile(path, []byte("# Mine\n"), 0o644)
	_ = env.Install(ctx, "rules", Approval{Tool: "rules"})
	if _, err := env.Enable(ctx, "rules"); err != nil {
		t.Fatal(err)
	}
	cur, _ := os.ReadFile(path)
	_ = os.WriteFile(path, append(cur, []byte("\nAdded later by me.\n")...), 0o644)
	if _, err := env.Disable(ctx, "rules"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if strings.Contains(string(got), "Be brief.") || !strings.Contains(string(got), "Added later by me.") || !strings.Contains(string(got), "# Mine") {
		t.Fatalf("only Trove's block should be gone:\n%s", got)
	}
}

func TestEnableCreatesAndRemoveDeletesRulesFile(t *testing.T) {
	ctx := context.Background()
	env, _, _, claude := testEnv(t, rulesTool("rules"))
	_ = env.Install(ctx, "rules", Approval{Tool: "rules"})
	if _, err := env.Enable(ctx, "rules"); err != nil {
		t.Fatal(err)
	}
	if err := env.Remove(ctx, "rules", Approval{Tool: "rules"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(claude, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Fatalf("file Trove created should be gone, stat err = %v", err)
	}
	if l, _ := env.Store.List(ctx); len(l) != 0 {
		t.Fatalf("record should be deleted: %+v", l)
	}
}

func TestEnableRefusesConflict(t *testing.T) {
	ctx := context.Background()
	a, b := rulesTool("a"), rulesTool("b")
	env, _, _, claude := testEnv(t, a, b)
	for _, n := range []string{"a", "b"} {
		if err := env.Install(ctx, n, Approval{Tool: n}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := env.Enable(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(claude, "CLAUDE.md"))
	_, err := env.Enable(ctx, "b")
	var c Conflict
	if !errors.As(err, &c) || c.Other != "a" {
		t.Fatalf("want a Conflict with a, got %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(claude, "CLAUDE.md"))
	if string(before) != string(after) {
		t.Fatal("a refused enable must not touch files")
	}
}

func TestRemoveRunsRemoveCommandAndKeepsRecordOnFailure(t *testing.T) {
	ctx := context.Background()
	env, r, _, _ := testEnv(t, shellTool("sq", "MIT"))
	r.out["sq"] = "2.0.0"
	_ = env.Install(ctx, "sq", Approval{Tool: "sq"})
	r.fail["pkg"] = true
	if err := env.Remove(ctx, "sq", Approval{Tool: "sq"}); err == nil {
		t.Fatal("expected failure")
	}
	if _, ok, _ := env.Store.Get(ctx, "sq"); !ok {
		t.Fatal("record must stay when the remove command fails")
	}
	r.fail["pkg"] = false
	if err := env.Remove(ctx, "sq", Approval{Tool: "sq"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := env.Store.Get(ctx, "sq"); ok {
		t.Fatal("record should be gone")
	}
}

func TestEnableUnwiredKindIsClearError(t *testing.T) {
	ctx := context.Background()
	env, r, _, _ := testEnv(t, shellTool("sq", "MIT"))
	r.out["sq"] = "2.0.0"
	_ = env.Install(ctx, "sq", Approval{Tool: "sq"})
	if _, err := env.Enable(ctx, "sq"); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("got %v", err)
	}
}
