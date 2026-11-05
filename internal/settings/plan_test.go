package settings

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type memFS map[string]string

func (m memFS) read(p string) ([]byte, error) {
	if s, ok := m[p]; ok {
		if s == "<unreadable>" {
			return nil, errors.New("permission denied")
		}
		return []byte(s), nil
	}
	return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
}

func (m memFS) dir(string) ([]os.DirEntry, error) { return nil, fs.ErrNotExist }

func planFor(t *testing.T, task string, files memFS, mut func(*Inputs)) *Plan {
	t.Helper()
	c, ps := loadAll(t)
	in := Inputs{
		Task: task, ProjectDir: filepath.FromSlash("/proj"), ClaudeDir: filepath.FromSlash("/home/.claude"),
		CodexDir: filepath.FromSlash("/home/.codex"), GOOS: "linux",
		ReadFile: files.read, ReadDir: files.dir, Getenv: func(string) string { return "" },
		Version: func(h string) string {
			return map[string]string{ClaudeCode: "2.1.314 (Claude Code)", Codex: "codex-cli 0.134.0"}[h]
		},
		ManagedClaudeDir: filepath.FromSlash("/managed/claude"), ManagedCodexDir: filepath.FromSlash("/managed/codex"),
	}
	if mut != nil {
		mut(&in)
	}
	p, err := BuildPlan(c, ps, in)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func hpOf(p *Plan, h string) *HarnessPlan {
	for _, x := range p.Harnesses {
		if x.Harness == h {
			return x
		}
	}
	return nil
}

func keys(cs []Change) string {
	var k []string
	for _, c := range cs {
		k = append(k, c.Key)
	}
	return strings.Join(k, ",")
}

func TestPlanDefaultsAreConservative(t *testing.T) {
	p := planFor(t, "exploration", memFS{}, nil)
	cc, cx := hpOf(p, ClaudeCode), hpOf(p, Codex)
	// outputStyle changes behavior and has no evidence: skipped by default.
	if len(cc.Changes) != 0 || len(cc.Skipped) != 1 || cc.Skipped[0].Value.Key != "outputStyle" {
		t.Errorf("claude plan = %+v", cc)
	}
	// Codex summary 'none' is risk none: planned, as a profile file.
	if keys(cx.Changes) != "model_reasoning_summary" {
		t.Errorf("codex changes = %v", keys(cx.Changes))
	}
	if cx.Path != filepath.Join(filepath.FromSlash("/home/.codex"), "trove-exploration.config.toml") {
		t.Errorf("codex path = %s", cx.Path)
	}
	if len(cx.Commands) != 1 || cx.Commands[0] != "codex --profile trove-exploration" {
		t.Errorf("commands = %v", cx.Commands)
	}
	// With the explicit opt-in the Claude value is planned.
	p = planFor(t, "exploration", memFS{}, func(in *Inputs) { in.AllowUnproven = true })
	if keys(hpOf(p, ClaudeCode).Changes) != "outputStyle" {
		t.Errorf("allow_unproven should plan outputStyle: %+v", hpOf(p, ClaudeCode))
	}
}

func TestPlanNeverOverridesManaged(t *testing.T) {
	files := memFS{
		filepath.FromSlash("/managed/claude/managed-settings.json"): `{"outputStyle":"Learning"}`,
		filepath.FromSlash("/managed/codex/managed_config.toml"):    "model_reasoning_summary = \"auto\"\n",
	}
	p := planFor(t, "exploration", files, func(in *Inputs) { in.AllowUnproven = true })
	cc, cx := hpOf(p, ClaudeCode), hpOf(p, Codex)
	if len(cc.Changes) != 0 || len(cc.Blocked) != 1 || !strings.Contains(cc.Blocked[0].Reason, "managed-settings.json") {
		t.Errorf("claude: %+v", cc)
	}
	if len(cx.Changes) != 0 || len(cx.Blocked) != 1 || !strings.Contains(cx.Blocked[0].Reason, "managed_config.toml") {
		t.Errorf("codex: %+v", cx)
	}
	if !strings.Contains(cc.Blocked[0].Reason, "cannot be overridden") {
		t.Errorf("reason should say why: %s", cc.Blocked[0].Reason)
	}
}

func TestPlanManagedUnknownsAreSaid(t *testing.T) {
	cc := hpOf(planFor(t, "quick-fix", memFS{}, nil), ClaudeCode)
	if !strings.Contains(strings.Join(cc.Notes, " "), "server-managed or MDM") {
		t.Errorf("notes must admit what Trove cannot see: %v", cc.Notes)
	}
	files := memFS{filepath.FromSlash("/managed/claude/managed-settings.json"): "<unreadable>"}
	cc = hpOf(planFor(t, "quick-fix", files, nil), ClaudeCode)
	if !strings.Contains(strings.Join(cc.Notes, " "), "could not read") {
		t.Errorf("an unreadable managed file must be reported: %v", cc.Notes)
	}
}

func TestPlanHigherLayerBlocks(t *testing.T) {
	files := memFS{
		filepath.FromSlash("/proj/.claude/settings.local.json"): `{"outputStyle":"Learning"}`,
		filepath.FromSlash("/proj/.codex/config.toml"):          "model_reasoning_summary = \"auto\"\n",
	}
	p := planFor(t, "exploration", files, func(in *Inputs) { in.AllowUnproven = true })
	if b := hpOf(p, ClaudeCode).Blocked; len(b) != 1 || !strings.Contains(b[0].Reason, "settings.local.json") {
		t.Errorf("claude blocked = %+v", b)
	}
	if b := hpOf(p, Codex).Blocked; len(b) != 1 || !strings.Contains(b[0].Reason, ".codex/config.toml") {
		t.Errorf("codex blocked = %+v", b)
	}
}

func TestPlanVersionGating(t *testing.T) {
	// Unknown version: nothing is planned.
	p := planFor(t, "exploration", memFS{}, func(in *Inputs) {
		in.AllowUnproven = true
		in.Version = func(string) string { return "" }
	})
	for _, h := range p.Harnesses {
		if len(h.Changes) != 0 || len(h.Notes) == 0 {
			t.Errorf("%s with unknown version: %+v", h.Harness, h)
		}
	}
	// Codex older than profile files: skipped with the version reason.
	p = planFor(t, "exploration", memFS{}, func(in *Inputs) {
		in.Version = func(h string) string {
			if h == Codex {
				return "codex-cli 0.133.9"
			}
			return "2.1.314"
		}
	})
	cx := hpOf(p, Codex)
	if len(cx.Changes) != 0 || len(cx.Skipped) == 0 || !strings.Contains(cx.Skipped[0].Reason, "0.134.0") {
		t.Errorf("codex 0.133.9: %+v", cx)
	}
}

func TestPlanUnavailableAndErrors(t *testing.T) {
	p := planFor(t, "quick-fix", memFS{}, func(in *Inputs) { in.ProjectDir = "" })
	if hpOf(p, ClaudeCode).Unavailable == "" {
		t.Error("claude without a project must be unavailable")
	}
	c, ps := loadAll(t)
	if _, err := BuildPlan(c, ps, Inputs{Task: "nope"}); err == nil || !strings.Contains(err.Error(), "quick-fix") {
		t.Errorf("unknown task error should list the task types: %v", err)
	}
}

func TestPlanShellEnvNote(t *testing.T) {
	c, _ := loadAll(t)
	ps, err := ParseProfiles([]byte(`version = 1
[[profile]]
task = "t"
description = "d"
[[profile.value]]
harness = "claude_code"
key = "CLAUDE_AUTOCOMPACT_PCT_OVERRIDE"
value = "70"
evidence = "bench:x"
why = "w"
`))
	if err != nil {
		t.Fatal(err)
	}
	p, err := BuildPlan(c, ps, Inputs{Task: "t", ProjectDir: "/proj", CodexDir: "/c", GOOS: "linux",
		ReadFile: memFS{}.read, ReadDir: memFS{}.dir,
		Getenv:  func(k string) string { return map[string]string{"CLAUDE_AUTOCOMPACT_PCT_OVERRIDE": "50"}[k] },
		Version: func(string) string { return "2.1.314" }})
	if err != nil {
		t.Fatal(err)
	}
	cc := hpOf(p, ClaudeCode)
	if keys(cc.Changes) != "CLAUDE_AUTOCOMPACT_PCT_OVERRIDE" || !strings.Contains(strings.Join(cc.Notes, " "), "set in your shell") {
		t.Errorf("claude: %+v", cc)
	}
}

func TestInstalledVersion(t *testing.T) {
	run := func(out string, err error) func(context.Context, string, ...string) ([]byte, error) {
		return func(context.Context, string, ...string) ([]byte, error) { return []byte(out), err }
	}
	if v := InstalledVersion(context.Background(), ClaudeCode, run("2.1.314 (Claude Code)\n", nil)); v != "2.1.314 (Claude Code)" {
		t.Errorf("v = %q", v)
	}
	if v := InstalledVersion(context.Background(), Codex, run("", errors.New("not found"))); v != "" {
		t.Errorf("missing program should give no version, got %q", v)
	}
	if v := InstalledVersion(context.Background(), Codex, run("hello\n", nil)); v != "" {
		t.Errorf("output without a version should give none, got %q", v)
	}
}
