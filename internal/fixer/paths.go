package fixer

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
)

// Trove edits only the files listed here, found at the locations the agents
// document (verified 2026-10-29: code.claude.com/docs/en/{memory,settings,mcp,
// managed-settings}, learn.chatgpt.com/docs/config-file/config-basic and
// /docs/agent-configuration/agents-md). Anything else is refused, whatever the
// fix asks for.

// managedMarkers identify organization-managed or policy files. They are
// refused even if a path happens to match an allowed name.
var managedMarkers = []string{
	"/library/application support/claudecode/", // Claude Code managed policy, macOS
	"/etc/claude-code/",                        // Linux and WSL
	"/program files/claudecode/",               // Windows
	"/etc/codex/",                              // Codex system config
}

var managedNames = map[string]bool{
	"managed-settings.json": true,
	"managed-mcp.json":      true,
	"managed_config.toml":   true,
	"requirements.toml":     true,
}

// credentialNames are never read for editing or written.
var credentialNames = map[string]bool{
	".credentials.json": true,
	"auth.json":         true,
	".claude.json":      true, // holds account details and OAuth state
	"credentials.json":  true,
}

func norm(p string) string {
	p = filepath.ToSlash(filepath.Clean(p))
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		p = strings.ToLower(p)
	}
	return p
}

// resolve follows symlinks of the file, or of its directory if it does not exist
// yet, so a link cannot lead outside the allowed set.
func resolve(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	if r, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
		return filepath.Join(r, filepath.Base(p))
	}
	return p
}

// allowedFiles are the exact files fixes may edit; allowedRuleDirs hold *.md rules.
func (e *Env) allowedFiles() []string {
	var fs []string
	if e.ProjectDir != "" {
		p := e.ProjectDir
		fs = append(fs,
			filepath.Join(p, "CLAUDE.md"), filepath.Join(p, "CLAUDE.local.md"),
			filepath.Join(p, "AGENTS.md"), filepath.Join(p, "AGENTS.override.md"),
			filepath.Join(p, ".claude", "CLAUDE.md"),
			filepath.Join(p, ".claude", "settings.json"), filepath.Join(p, ".claude", "settings.local.json"),
			filepath.Join(p, ".mcp.json"), filepath.Join(p, ".codex", "config.toml"))
	}
	if e.ClaudeDir != "" {
		fs = append(fs, filepath.Join(e.ClaudeDir, "CLAUDE.md"), filepath.Join(e.ClaudeDir, "settings.json"))
	}
	if e.CodexDir != "" {
		fs = append(fs, filepath.Join(e.CodexDir, "AGENTS.md"), filepath.Join(e.CodexDir, "config.toml"))
	}
	return fs
}

// Allowed returns nil if a fix may edit path, otherwise a plain-English reason.
func (e *Env) Allowed(path string) error {
	if path == "" {
		return fmt.Errorf("no path given")
	}
	for _, p := range []string{path, resolve(path)} {
		n := norm(p)
		base := n[strings.LastIndex(n, "/")+1:]
		for _, m := range managedMarkers {
			if strings.Contains(n+"/", m) {
				return fmt.Errorf("%s is an organization-managed policy file. Trove never edits managed files; ask your administrator", path)
			}
		}
		if managedNames[base] {
			return fmt.Errorf("%s is a managed or policy file. Trove never edits managed files; ask your administrator", path)
		}
		if credentialNames[base] || strings.HasPrefix(base, ".env") {
			return fmt.Errorf("%s may hold credentials. Trove never edits credential files", path)
		}
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%q is not an absolute path", path)
	}
	rp := norm(resolve(path))
	for _, f := range e.allowedFiles() {
		if rp == norm(resolve(f)) {
			return nil
		}
	}
	if e.ProjectDir != "" {
		rules := norm(resolve(filepath.Join(e.ProjectDir, ".claude", "rules"))) + "/"
		if strings.HasPrefix(rp, rules) && strings.HasSuffix(rp, ".md") {
			return nil
		}
	}
	return fmt.Errorf("%s is not one of the agent config files Trove edits (CLAUDE.md, AGENTS.md, .mcp.json, settings and config files in the project or your agent's own folder)", path)
}
