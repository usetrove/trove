// Package plugin renders the files of Trove's Claude Code plugin that are derived
// from code: the manifest, the hook registrations and the marketplace entry. The
// plugin only wires Claude Code to the trove binary; it holds no optimization
// logic. The hand-written files (the wrapper script and the slash commands) live
// in plugin/trove next to these, and a test fails when a rendered file differs
// from the one committed, so the two cannot drift.
//
// The plugin deliberately ships no MCP server entry and no subagent. A plugin
// cannot ship either one switched off, and both cost tokens in every session
// (tool definitions, an agent description), which rule 15 charges to Trove and
// the "nothing enabled without approval" rule forbids. /trove:setup offers them
// instead, through `trove mcp install` and `trove tasks apply scout`, which are
// tracked, measured and reversed by `trove uninstall`.
//
// Formats verified against code.claude.com/docs/en/plugins/{manifest-reference,
// components,marketplace-reference} on 2026-11-05.
package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/usetrove/trove/internal/setup"
)

// Name is the plugin, marketplace and MCP server name. Rule 13: no provider or
// product brand appears in any name Trove creates.
const Name = "trove"

// Dir is where the plugin lives in the repository.
const Dir = "plugin/trove"

// Bin is how a plugin hook or MCP entry reaches the wrapper. Plugin hooks and MCP
// processes do not get the plugin's bin/ on PATH (components docs, 2026-11-05), so
// the wrapper is always called by its absolute path.
const Bin = "${CLAUDE_PLUGIN_ROOT}/bin/trove"

// Description is shown in /plugin and the marketplace.
const Description = "Cuts the tokens your coding agent wastes and shows that results did not suffer. Works with Claude Code on any login, local only, and starts in shadow mode."

// Run is how every plugin command starts the wrapper: through sh, so a lost
// executable bit (a commit made on Windows) cannot break the plugin, and with the
// path quoted so a plugin folder with a space in it still works.
const Run = `sh "` + Bin + `"`

// HookCommand is the command for a `trove hook <event>` registration.
func HookCommand(event string) string { return Run + " hook " + event }

// StartCommand is the SessionStart command that does the first-run report and the
// capability check.
func StartCommand() string { return Run + " plugin start" }

// File is one rendered file; Path is relative to the repository root.
type File struct {
	Path    string
	Content []byte
}

type hookHandler struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

type hookGroup struct {
	Matcher string        `json:"matcher,omitempty"`
	Hooks   []hookHandler `json:"hooks"`
}

func encode(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// HooksJSON renders hooks/hooks.json from the specs `trove hooks install` uses
// for Claude Code, so the plugin registers exactly the same handlers. The spec
// commands must have been built with HookCommand. The fetch-back hook's matcher
// is rewritten to the plugin's MCP tool name. A SessionStart entry for the
// first-run command is added after the existing ones.
func HooksJSON(specs []setup.HookSpec) ([]byte, error) {
	events := map[string][]hookGroup{}
	for _, s := range specs {
		if !strings.HasPrefix(s.Command, Run+" hook ") {
			return nil, fmt.Errorf("hook command %q was not built with plugin.HookCommand", s.Command)
		}
		events[s.Event] = append(events[s.Event], hookGroup{Matcher: s.Matcher, Hooks: []hookHandler{{Type: "command", Command: s.Command, Timeout: s.Timeout}}})
	}
	// 90 s is the ceiling for the one-time history scan; later sessions return at once.
	events["SessionStart"] = append(events["SessionStart"], hookGroup{Hooks: []hookHandler{{Type: "command", Command: StartCommand(), Timeout: 90}}})
	return encode(map[string]any{
		"description": "Trove hooks. They stay inert until an optimizer is set to shadow or on in Trove's config, and every one exits quietly on any error.",
		"hooks":       events,
	})
}

// ManifestVersion is the version written to plugin.json. Set it to the release version (no "v") with
// plugin/trove/VERSION before tagging; release.yml fails the release if either differs from the tag.
const ManifestVersion = "0.3.0"

// Render returns every derived file. specs are the Claude Code hook specs built
// with HookCommand.
func Render(specs []setup.HookSpec) ([]File, error) {
	var out []File
	add := func(path string, b []byte, err error) error {
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, File{Path: path, Content: b})
		return nil
	}

	b, err := encode(map[string]any{
		"name":        Name,
		"description": Description,
		"author":      map[string]string{"name": "Trove"},
		"homepage":    "https://github.com/usetrove/trove",
		"repository":  "https://github.com/usetrove/trove",
		"license":     "Apache-2.0",
		"keywords":    []string{"tokens", "cost", "efficiency", "local-first"},
		"version":     ManifestVersion,
	})
	if err := add(Dir+"/.claude-plugin/plugin.json", b, err); err != nil {
		return nil, err
	}

	b, err = HooksJSON(specs)
	if err := add(Dir+"/hooks/hooks.json", b, err); err != nil {
		return nil, err
	}

	b, err = encode(map[string]any{
		"name":        Name,
		"owner":       map[string]string{"name": "Trove"},
		"description": "Trove's plugin for Claude Code.",
		"plugins": []map[string]any{{
			"name": Name, "source": "./" + Dir, "description": Description,
			"category": "productivity", "tags": []string{"tokens", "cost", "local-first"},
		}},
	})
	if err := add(".claude-plugin/marketplace.json", b, err); err != nil {
		return nil, err
	}
	return out, nil
}
