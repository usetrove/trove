package root

import (
	"errors"
	"path"
	"strings"
)

// ErrSensitive is returned for files that usually hold secrets.
var ErrSensitive = errors.New("this file usually holds credentials or keys, so Trove's tools will not read it. " +
	"If the agent really needs it, the agent's own file tools, with their permission prompts, are the right way to open it")

// Why this exists: an agent's own permission rules (for example a deny rule on
// reading .env) apply to the agent's built-in tools, not to an MCP server. So
// without a check here, trove_read and trove_search would be a way around them,
// and a prompt-injected agent could use them to pull secrets into its context.

var sensitiveBases = map[string]bool{
	".netrc": true, "_netrc": true, ".npmrc": true, ".pypirc": true, ".git-credentials": true,
	".htpasswd": true, ".pgpass": true, ".my.cnf": true,
	"credentials": true, "credentials.json": true, ".credentials.json": true, "auth.json": true, ".claude.json": true,
	"id_rsa": true, "id_dsa": true, "id_ecdsa": true, "id_ed25519": true,
	"terraform.tfstate": true, "terraform.tfstate.backup": true,
	"secrets.yml": true, "secrets.yaml": true, "secrets.json": true, "secrets.toml": true,
}

var sensitiveExts = []string{".pem", ".key", ".p12", ".pfx", ".jks", ".keystore", ".kdbx", ".tfvars"}

// Templates people commit on purpose; they hold placeholders, not secrets.
var envTemplates = map[string]bool{
	".env.example": true, ".env.sample": true, ".env.template": true, ".env.dist": true, ".env.defaults": true,
}

var sensitiveDirs = map[string]bool{".ssh": true, ".aws": true, ".gnupg": true, ".kube": true, ".docker": true}

// Sensitive reports whether a project-relative, slash-separated path names a
// file that usually holds secrets. It looks at the name only, so it is cheap and
// has no side effects; it errs toward refusing.
func Sensitive(rel string) bool {
	rel = strings.ToLower(strings.ReplaceAll(rel, `\`, "/"))
	parts := strings.Split(rel, "/")
	base := parts[len(parts)-1]
	for _, d := range parts[:len(parts)-1] {
		if sensitiveDirs[d] {
			return true
		}
	}
	// .git/config can hold tokens inside remote URLs.
	if n := len(parts); n >= 2 && parts[n-2] == ".git" && base == "config" {
		return true
	}
	if base == ".env" || (strings.HasPrefix(base, ".env.") && !envTemplates[base]) {
		return true
	}
	if sensitiveBases[base] {
		return true
	}
	ext := path.Ext(base)
	for _, e := range sensitiveExts {
		if ext == e {
			return true
		}
	}
	return false
}
