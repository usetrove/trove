// Package install records how the Trove MCP server was registered with agents,
// so it can be removed again and so other parts of Trove can tell whether the
// agents can reach the recovery tools.
//
// File edits (Codex config.toml, a project's .mcp.json) are recorded by
// internal/setup with backups. Claude Code's user-scope registration lives in
// ~/.claude.json, which Trove never edits (it holds account details), so it is
// done with Claude Code's own `claude mcp add` command and recorded here.
package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/usetrove/trove/internal/setup"
)

const recordFile = "mcp_installs.json"

// CLIInstall is a registration made through an agent's own command.
type CLIInstall struct {
	Agent     string    `json:"agent"`
	Scope     string    `json:"scope"`
	Remove    []string  `json:"remove"` // the command that undoes it
	AppliedAt time.Time `json:"applied_at"`
}

// LoadCLI reads the records (none if the file does not exist).
func LoadCLI(dataDir string) ([]CLIInstall, error) {
	b, err := os.ReadFile(filepath.Join(dataDir, recordFile)) //nolint:gosec // inside the data dir
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %v", recordFile, err)
	}
	var out []CLIInstall
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("%s is damaged (%v). Delete it and run `trove mcp install` again", filepath.Join(dataDir, recordFile), err)
	}
	return out, nil
}

// SaveCLI writes the records, removing the file when there are none.
func SaveCLI(dataDir string, recs []CLIInstall) error {
	p := filepath.Join(dataDir, recordFile)
	if len(recs) == 0 {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	b, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, append(b, '\n'), 0o600)
}

// MCPChanges returns the recorded file edits that registered the server.
func MCPChanges(dataDir string) ([]setup.Change, error) {
	cs, err := setup.LoadChanges(dataDir)
	if err != nil {
		return nil, err
	}
	var out []setup.Change
	for _, c := range cs {
		if c.Agent == setup.CodexMCP || c.Agent == setup.ProjectMCP {
			out = append(out, c)
		}
	}
	return out, nil
}

// Registered reports whether Trove has registered its MCP server with any agent.
// It reads only Trove's own records, so it is cheap and safe to call often.
func Registered(dataDir string) bool {
	if recs, err := LoadCLI(dataDir); err == nil && len(recs) > 0 {
		return true
	}
	cs, err := MCPChanges(dataDir)
	return err == nil && len(cs) > 0
}
