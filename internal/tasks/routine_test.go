package tasks

import (
	"strings"
	"testing"
)

func frontmatter(t *testing.T, content string) string {
	t.Helper()
	parts := strings.SplitN(content, "---\n", 3)
	if len(parts) != 3 || parts[0] != "" {
		t.Fatalf("no frontmatter block:\n%s", content)
	}
	return parts[1]
}

func TestRoutineAgentsAreCheapLowEffortAndStrict(t *testing.T) {
	fs, err := RoutineFiles(cat(t), Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		RoleTestRunner: ".claude/agents/trove-test-runner.md",
		RoleLintFixer:  ".claude/agents/trove-lint-fixer.md",
		RoleTestSkill:  ".claude/skills/trove-test/SKILL.md",
		RoleLintSkill:  ".claude/skills/trove-lint/SKILL.md",
	}
	if len(fs) != len(want) {
		t.Fatalf("want %d files, got %d", len(want), len(fs))
	}
	for _, f := range fs {
		if want[f.Role] != f.Path {
			t.Errorf("%s: path %s, want %s", f.Role, f.Path, want[f.Role])
		}
		fm := frontmatter(t, f.Content)
		if !strings.Contains(f.Content, Managed) {
			t.Errorf("%s lacks the managed marker", f.Path)
		}
		switch f.Role {
		case RoleTestRunner, RoleLintFixer:
			for _, line := range []string{"model: haiku\n", "effort: low\n", "maxTurns: "} {
				if !strings.Contains(fm, line) {
					t.Errorf("%s frontmatter lacks %q:\n%s", f.Path, line, fm)
				}
			}
			if !strings.Contains(f.Content, "STATUS: done") || !strings.Contains(f.Content, "at most 8 short lines") {
				t.Errorf("%s lacks the strict return format", f.Path)
			}
			if !strings.Contains(fm, "general-purpose") {
				t.Errorf("%s description must say where to re-delegate", f.Path)
			}
		default:
			// A skill's own model or effort would apply to the main conversation's turn; it must fork instead.
			if strings.Contains(fm, "model:") || strings.Contains(fm, "effort:") {
				t.Errorf("%s must not set model or effort itself:\n%s", f.Path, fm)
			}
			for _, line := range []string{"context: fork\n", "agent: trove-", "disable-model-invocation: true\n"} {
				if !strings.Contains(fm, line) {
					t.Errorf("%s frontmatter lacks %q", f.Path, line)
				}
			}
		}
	}
	if strings.Contains(frontmatter(t, fs[0].Content), "Edit") {
		t.Error("the test runner must not be able to edit files")
	}
}

// Frontmatter values are written as plain YAML scalars, so none may hold ": ", " #", or start with a character
// YAML treats specially; otherwise Claude Code could fail to read the file.
func TestFrontmatterValuesArePlainYAMLScalars(t *testing.T) {
	c := cat(t)
	var all []File
	for _, r := range []func(*Catalog, Options) ([]File, error){ScoutFiles, RoutineFiles, ExploreFiles, CascadeFiles} {
		fs, err := r(c, Options{TestCmd: "go test ./...", LintCmd: "golangci-lint run"})
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, fs...)
	}
	for _, f := range all {
		if f.Role == RoleSnippet {
			continue
		}
		for _, line := range strings.Split(strings.TrimSuffix(frontmatter(t, f.Content), "\n"), "\n") {
			k, v, ok := strings.Cut(line, ": ")
			if !ok || k == "" || strings.ContainsAny(k, " \t") {
				t.Errorf("%s: %q is not a key: value line", f.Path, line)
				continue
			}
			if k == "argument-hint" {
				continue // "[task]" is the form Claude Code's own command examples use
			}
			if strings.Contains(v, ": ") || strings.Contains(v, " #") || strings.HasSuffix(v, ":") ||
				strings.ContainsAny(v[:1], "!&*?|>'\"%@`{}[],#-") {
				t.Errorf("%s: the value of %s is not a safe plain YAML scalar: %q", f.Path, k, v)
			}
		}
	}
}

func TestRoutineUsesConfiguredCommandsAndRejectsBadOnes(t *testing.T) {
	fs, err := RoutineFiles(cat(t), Options{TestCmd: "go test ./...", LintCmd: "gofmt -l .", RoutineModel: "sonnet", RoutineEffort: "medium"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fs[0].Content, "`go test ./...`") || !strings.Contains(fs[1].Content, "`gofmt -l .`") {
		t.Error("configured commands must be written into the agents")
	}
	if !strings.Contains(fs[0].Content, "model: sonnet\neffort: medium\n") {
		t.Errorf("user overrides must win:\n%s", fs[0].Content)
	}
	for _, o := range []Options{
		{RoutineEffort: "ultra"}, {ScoutEffort: "none"}, {RoutineModel: "gpt"},
		{LintCmd: "a\nb"}, {TestCmd: "echo `x`"},
	} {
		if _, err := RoutineFiles(cat(t), o); err == nil {
			t.Errorf("%+v must be rejected", o)
		}
	}
}

func TestScoutGetsLowEffortAndStatus(t *testing.T) {
	fs, err := ScoutFiles(cat(t), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if fm := frontmatter(t, fs[0].Content); !strings.Contains(fm, "effort: low\n") {
		t.Errorf("scout must default to low effort:\n%s", fm)
	}
	if !strings.Contains(fs[0].Content, "STATUS: done") {
		t.Error("scout must use the status format")
	}
}

func TestExploreOverrideIsReadOnly(t *testing.T) {
	fs, err := ExploreFiles(cat(t), Options{})
	if err != nil {
		t.Fatal(err)
	}
	f := fs[0]
	fm := frontmatter(t, f.Content)
	if f.Path != ".claude/agents/trove-explore.md" || !strings.Contains(fm, "name: Explore\n") {
		t.Fatalf("unexpected explore file %s:\n%s", f.Path, fm)
	}
	if !strings.Contains(fm, "tools: Read, Grep, Glob\n") || !strings.Contains(fm, "effort: low\n") {
		t.Errorf("explore must be read-only and low effort:\n%s", fm)
	}
}

func TestRoutineRenderingIsDeterministic(t *testing.T) {
	a, _ := RoutineFiles(cat(t), Options{TestCmd: "make test"})
	b, _ := RoutineFiles(cat(t), Options{TestCmd: "make test"})
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("file %d differs between runs", i)
		}
	}
}

func TestParseStatus(t *testing.T) {
	for in, want := range map[string]string{
		"STATUS: done\nTESTS: 3 passed":            StatusDone,
		"status: Failed - no test command":         StatusFailed,
		"**STATUS: unsure**\n- a.go:3":             StatusUnsure,
		"Here you go.\nSTATUS: done":               StatusDone,
		"`STATUS: failed`":                         StatusFailed,
		"All tests passed.":                        StatusUnknown,
		"STATUS: maybe":                            StatusUnknown,
		"":                                         StatusUnknown,
		strings.Repeat("x\n", 50) + "STATUS: done": StatusUnknown, // only the head is read
	} {
		if got := ParseStatus(in); got != want {
			t.Errorf("ParseStatus(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestAgentTypeLabelKeepsUserNamesPrivate(t *testing.T) {
	for in, want := range map[string]string{
		"trove-test-runner": "trove-test-runner",
		"Explore":           "Explore",
		"general-purpose":   "general-purpose",
		"my-secret-project": "user",
		"trove-a/b":         "user",
		"":                  "unknown",
	} {
		if got := AgentTypeLabel(in); got != want {
			t.Errorf("AgentTypeLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCatalogHasVerifiedEffortAndSkillFields(t *testing.T) {
	c := cat(t)
	if !c.EffortOK("low") || c.EffortOK("ultra") {
		t.Error("effort levels must come from the catalog")
	}
	names := map[string]bool{}
	for _, f := range c.SkillFields {
		names[f.Name] = true
	}
	for _, n := range []string{"name", "description", "context", "agent", "disable-model-invocation"} {
		if !names[n] {
			t.Errorf("skill field %s missing from the catalog", n)
		}
	}
}
