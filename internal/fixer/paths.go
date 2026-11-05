package fixer

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

// trovePerfileRE matches the Codex profile files Trove generates:
// ~/.codex/trove-<task>.config.toml.
var trovePerfileRE = regexp.MustCompile(`^trove-[a-z0-9-]+\.config\.toml$`)

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
		// The refusal lists must not depend on the host OS: lower-case and treat backslashes as
		// separators everywhere, so a macOS or Windows managed path is refused on Linux too.
		n := strings.ToLower(strings.ReplaceAll(norm(p), `\`, "/"))
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
	// Resolving both sides below would make a symlinked CLAUDE.md match itself, and an edit would
	// then write through the link to a file outside the allowed set. Refuse a link outright.
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symbolic link. Trove does not edit through links; edit the file it points to yourself, or replace the link with a regular file", path)
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
	// Codex profiles are files in the user's Codex folder (learn.chatgpt.com/docs/
	// config-file/config-advanced, read 2026-11-05). Trove only writes the ones it
	// names itself, so it never touches a profile the user made.
	if e.CodexDir != "" && trovePerfileRE.MatchString(filepath.Base(path)) &&
		norm(resolve(filepath.Dir(path))) == norm(resolve(e.CodexDir)) {
		return nil
	}
	return fmt.Errorf("%s is not one of the agent config files Trove edits (CLAUDE.md, AGENTS.md, .mcp.json, settings and config files in the project or your agent's own folder)", path)
}
