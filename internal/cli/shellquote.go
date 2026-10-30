package cli

import (
	"errors"
	"path/filepath"
	"strings"
)

// shellQuote quotes s for a POSIX shell. Agents run hook and status-line
// commands through a shell, and the binary path and data directory come from the
// environment, so a stray quote, "$", backtick, ";" or "&" in either must stay
// plain text. Strings made only of safe characters are returned unchanged.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("_@%+=:,./-", r):
		default:
			safe = false
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// troveBinary is the absolute path of this binary, for commands written into
// agent config files. It never falls back to a bare "trove", which would run
// whatever is first on PATH when the agent invokes it. A nil Executable (only in
// tests) keeps the bare name.
func troveBinary(a *App) (string, error) {
	if a.Executable == nil {
		return "trove", nil
	}
	p, err := a.Executable()
	// The commands are POSIX shell commands, so a leading "/" counts as absolute
	// even where filepath would say otherwise.
	if err != nil || p == "" || !(filepath.IsAbs(p) || strings.HasPrefix(p, "/")) {
		return "", errors.New("cannot tell where the trove program is installed, so Trove will not write a command that depends on PATH. " +
			"Run it by its full path, for example /usr/local/bin/trove")
	}
	return p, nil
}
