// Package naming guards CLAUDE.md rule 13: no "Claude", "Anthropic", "Codex" or "OpenAI" in any product,
// plugin, feature, package or command name of ours. Plain-text "works with Claude Code" is fine.
//
// The test walks every folder and file name in the repository, the command tree, the package and plugin
// manifests, the MCP tool names and the subagent names. Names that must contain a banned word are listed in
// allowed with the reason; a new name that is not listed fails the build.
package naming

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/cli"
)

var banned = regexp.MustCompile(`(?i)claude|anthropic|codex|openai`)

// allowed paths (slash-separated, relative to the repo root) may contain a banned word, each with the reason.
var allowed = []struct {
	pattern *regexp.Regexp
	why     string
}{
	{regexp.MustCompile(`(^|/)\.claude-plugin(/|$)`), "the folder name Claude Code's plugin format requires"},
	{regexp.MustCompile(`^CLAUDE\.md$`), "the project file name Claude Code reads"},
	{regexp.MustCompile(`^\.claude(/|$)`), "Claude Code's own project settings folder"},
	{regexp.MustCompile(`^internal/gateway/usage/(anthropic|openai)`), "parsers named for the provider API whose usage format they read"},
	{regexp.MustCompile(`(^|/)testdata/`), "recorded fixtures are named for what they record"},
	{regexp.MustCompile(`^internal/(settings|tasks)/catalog/`), "data files listing the integrated agent's documented settings"},
	{regexp.MustCompile(`^docs/(.*/)?[^/]*(claude|anthropic|codex|openai)[^/]*\.md$`), "documentation pages about the agent or provider being integrated (plain-text description, not a product name)"},
	{regexp.MustCompile(`^bench/(tasks|results|cache)/`), "benchmark data and generated output"},
}

var skipDirs = map[string]bool{".git": true, "dist": true, "node_modules": true, "__pycache__": true, ".pytest_cache": true, "trovebench.egg-info": true}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
		if filepath.Dir(d) == d {
			t.Fatal("go.mod not found above " + dir)
		}
	}
}

func isAllowed(rel string) bool {
	for _, a := range allowed {
		if a.pattern.MatchString(rel) {
			return true
		}
	}
	return false
}

func TestNoBannedWordInFolderOrFileNames(t *testing.T) {
	root := repoRoot(t)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() && skipDirs[d.Name()] {
			return fs.SkipDir
		}
		if rel == "." || !banned.MatchString(d.Name()) || isAllowed(rel) {
			return nil
		}
		if strings.HasSuffix(d.Name(), ".exe") && !strings.Contains(rel, "/") {
			return nil // a locally built binary at the repo root is not checked in
		}
		t.Errorf("%s contains a banned word (rule 13). Rename it, or if the name is required by the agent or describes a provider API, add it to allowed with the reason", rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNoBannedWordInCommandNames(t *testing.T) {
	root := cli.NewRoot(&cli.App{Out: io.Discard, Err: io.Discard, Getenv: func(string) string { return "" }})
	var walk func(c *cobra.Command, path string)
	walk = func(c *cobra.Command, path string) {
		names := append([]string{c.Name()}, c.Aliases...)
		for _, n := range names {
			if banned.MatchString(n) {
				t.Errorf("command %q (%s) contains a banned word (rule 13)", n, path)
			}
		}
		for _, sub := range c.Commands() {
			walk(sub, path+" "+sub.Name())
		}
	}
	walk(root, root.Name())
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return m
}

func TestNoBannedWordInPackageAndPluginNames(t *testing.T) {
	root := repoRoot(t)
	names := map[string]string{}
	for _, f := range []string{"plugin/trove/.claude-plugin/plugin.json", "npm/package.json", "plugins/opencode/package.json", ".claude-plugin/marketplace.json"} {
		m := readJSON(t, filepath.Join(root, filepath.FromSlash(f)))
		names[f+" name"], _ = m["name"].(string)
		if ps, ok := m["plugins"].([]any); ok {
			for i, p := range ps {
				if pm, ok := p.(map[string]any); ok {
					names[f+" plugins["+string(rune('0'+i))+"].name"], _ = pm["name"].(string)
				}
			}
		}
	}
	gomod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	names["go.mod module"] = strings.SplitN(strings.TrimPrefix(string(bytes.SplitN(gomod, []byte("\n"), 2)[0]), "module "), " ", 2)[0]
	if py, err := os.ReadFile(filepath.Join(root, "bench", "pyproject.toml")); err == nil {
		if m := regexp.MustCompile(`(?m)^name\s*=\s*"([^"]+)"`).FindSubmatch(py); m != nil {
			names["bench/pyproject.toml name"] = string(m[1])
		}
	}
	for where, n := range names {
		if n == "" {
			t.Errorf("%s: no name found; the test needs updating if the manifest changed shape", where)
		}
		if banned.MatchString(n) {
			t.Errorf("%s = %q contains a banned word (rule 13)", where, n)
		}
	}
	// The plugin's slash commands are named by file.
	cmds, _ := filepath.Glob(filepath.Join(root, "plugin", "trove", "commands", "*.md"))
	if len(cmds) == 0 {
		t.Error("no plugin commands found; the test needs updating")
	}
	for _, c := range cmds {
		if banned.MatchString(filepath.Base(c)) {
			t.Errorf("plugin command %s contains a banned word (rule 13)", filepath.Base(c))
		}
	}
}

// MCP tool names and subagent names are string literals in the source.
func TestNoBannedWordInMCPToolAndSubagentNames(t *testing.T) {
	root := repoRoot(t)
	tool := regexp.MustCompile(`"(trove[_-][A-Za-z0-9_-]+)"`)
	found := 0
	for _, dir := range []string{"internal/mcp", "internal/tasks"} {
		files, _ := filepath.Glob(filepath.Join(root, filepath.FromSlash(dir), "*.go"))
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range tool.FindAllSubmatch(b, -1) {
				found++
				if banned.Match(m[1]) {
					t.Errorf("%s: %q contains a banned word (rule 13)", filepath.Base(f), m[1])
				}
			}
		}
	}
	if found < 4 {
		t.Errorf("found only %d trove_* / trove-* names; the test needs updating", found)
	}
}
