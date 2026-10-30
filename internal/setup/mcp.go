package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Agent identifiers for the MCP server registration.
const (
	// CodexMCP is the [mcp_servers.trove] table in Codex's config.toml.
	CodexMCP = "codex_mcp"
	// ProjectMCP is the trove entry in a project's .mcp.json (Claude Code,
	// project scope).
	ProjectMCP = "project_mcp"
)

// MCPServerName is the name Trove's MCP server is registered under.
const MCPServerName = "trove"

// The MCP block has its own markers so undoing the gateway change never removes
// it, and the other way round.
const (
	mcpMarkerOpen  = "# >>> trove mcp (managed by `trove mcp install`; remove with `trove mcp uninstall`)"
	mcpMarkerClose = "# <<< trove mcp"
)

func tomlString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// PlanCodexMCP plans adding [mcp_servers.trove] to Codex's config.toml.
// command is the program that starts the server (trove, or its absolute path).
func PlanCodexMCP(codexDir, command string) (*Edit, error) {
	path := filepath.Join(codexDir, "config.toml")
	old, err := readOptional(path)
	if err != nil {
		return nil, err
	}
	block := mcpMarkerOpen + "\n[mcp_servers." + MCPServerName + "]\n" +
		"command = " + tomlString(command) + "\n" +
		"args = [\"mcp\"]\n" + mcpMarkerClose + "\n"
	e := &Edit{Agent: CodexMCP, Path: path, value: command, old: old, Show: block,
		Summary: "Register the Trove MCP server with Codex CLI (read, search and run tools that return less text).",
		Note:    "Codex starts it when needed. It reads only inside the project folder and runs only commands listed in .trove/commands.toml.",
	}
	if bytes.Contains(old, []byte(mcpMarkerOpen)) {
		e.NoOp = true
		return e, nil
	}
	if old != nil {
		var cur map[string]any
		if err := toml.Unmarshal(old, &cur); err != nil {
			return nil, fmt.Errorf("%s is not valid TOML (%v). Fix or move that file, then run `trove mcp install` again; Trove will not overwrite a file it cannot read", path, err)
		}
		if ms, ok := cur["mcp_servers"].(map[string]any); ok {
			if _, dup := ms[MCPServerName]; dup {
				return nil, fmt.Errorf("%s already defines [mcp_servers.%s]. Trove will not replace it; remove or rename it, then run `trove mcp install` again", path, MCPServerName)
			}
		}
	}
	body := string(old)
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	if body != "" {
		body += "\n"
	}
	updated := body + block
	var check map[string]any
	if err := toml.Unmarshal([]byte(updated), &check); err != nil {
		return nil, fmt.Errorf("internal check failed: the planned %s would not be valid TOML (%v); nothing was changed", path, err)
	}
	e.updated = []byte(updated)
	return e, nil
}

type mcpJSONServer struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// PlanProjectMCP plans adding the trove server to <projectDir>/.mcp.json, which
// Claude Code reads for project-scoped servers and asks the user to approve.
func PlanProjectMCP(projectDir, command string) (*Edit, error) {
	path := filepath.Join(projectDir, ".mcp.json")
	old, err := readOptional(path)
	if err != nil {
		return nil, err
	}
	doc := map[string]json.RawMessage{}
	servers := map[string]json.RawMessage{}
	if old != nil && len(bytes.TrimSpace(old)) > 0 {
		if err := json.Unmarshal(old, &doc); err != nil {
			return nil, fmt.Errorf("%s is not valid JSON (%v). Fix or move that file, then run `trove mcp install` again; Trove will not overwrite a file it cannot read", path, err)
		}
		if raw, ok := doc["mcpServers"]; ok {
			if err := json.Unmarshal(raw, &servers); err != nil {
				return nil, fmt.Errorf("%s has an mcpServers value that is not an object. Fix it, then run `trove mcp install` again", path)
			}
		}
	}
	entry, _ := json.Marshal(mcpJSONServer{Command: command, Args: []string{"mcp"}})
	e := &Edit{Agent: ProjectMCP, Path: path, value: command, old: old,
		Show:    `"mcpServers": { "` + MCPServerName + `": ` + string(entry) + ` }`,
		Summary: "Register the Trove MCP server for this project in .mcp.json (Claude Code asks you to approve it the first time).",
		Note:    "This file is meant to be committed, so teammates need trove on their PATH; use user scope instead if you do not want that.",
	}
	if cur, ok := servers[MCPServerName]; ok {
		var have mcpJSONServer
		if json.Unmarshal(cur, &have) == nil && have.Command == command && len(have.Args) == 1 && have.Args[0] == "mcp" {
			e.NoOp = true
			return e, nil
		}
		return nil, fmt.Errorf("%s already defines a server named %q. Trove will not replace it; remove or rename it, then run `trove mcp install` again", path, MCPServerName)
	}
	servers[MCPServerName] = entry
	raw, err := json.Marshal(servers)
	if err != nil {
		return nil, err
	}
	doc["mcpServers"] = raw
	updated, err := marshalIndent(doc)
	if err != nil {
		return nil, err
	}
	e.updated = updated
	return e, nil
}

// stripMCPJSON removes the trove server from .mcp.json, leaving everything else.
func stripMCPJSON(cur []byte) ([]byte, error) {
	doc := map[string]json.RawMessage{}
	if err := json.Unmarshal(cur, &doc); err != nil {
		return nil, fmt.Errorf("the file is no longer valid JSON, so Trove left it alone; remove the %q entry by hand", MCPServerName)
	}
	servers := map[string]json.RawMessage{}
	if raw, ok := doc["mcpServers"]; ok {
		if err := json.Unmarshal(raw, &servers); err != nil {
			return nil, fmt.Errorf("mcpServers is not an object, so Trove left the file alone")
		}
	}
	if _, ok := servers[MCPServerName]; !ok {
		return cur, nil
	}
	delete(servers, MCPServerName)
	if len(servers) == 0 {
		delete(doc, "mcpServers")
	} else {
		raw, _ := json.Marshal(servers)
		doc["mcpServers"] = raw
	}
	return marshalIndent(doc)
}

// stripBetween removes the lines from open through close, the way stripMarked
// does for the gateway block.
func stripBetween(cur []byte, open, close string) ([]byte, error) {
	var out []string
	skip := false
	for _, l := range strings.SplitAfter(string(cur), "\n") {
		t := strings.TrimRight(l, "\r\n")
		switch {
		case t == open:
			skip = true
		case skip && t == close:
			skip = false
		case !skip:
			out = append(out, l)
		}
	}
	if skip {
		return nil, errors.New("Trove's block in this file has an opening marker but no closing one, so Trove left the file alone rather than risk deleting your own lines. Remove the lines from '" + open + "' by hand")
	}
	s := strings.TrimRight(strings.Join(out, ""), "\r\n")
	if strings.TrimSpace(s) == "" {
		return []byte{}, nil
	}
	return []byte(s + "\n"), nil
}
