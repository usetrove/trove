package setup

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Agent identifiers for hook registrations in the manifest.
const (
	ClaudeHooks = "claude_code_hooks"
	CodexHooks  = "codex_hooks"
)

// HookSpec is one hook handler Trove registers: Command runs when Event fires for
// a tool matching Matcher (empty matches everything). Timeout is in seconds.
type HookSpec struct {
	Event   string
	Matcher string
	Command string
	Timeout int
}

// InlineHooksError means ~/.codex/config.toml already has inline [hooks] tables.
// Codex loads both that and hooks.json when a layer has both and warns about it,
// so Trove adds nothing and leaves the choice to the user.
type InlineHooksError struct{ Path string }

func (e *InlineHooksError) Error() string {
	return fmt.Sprintf("%s already defines [hooks] inline, and Codex warns when a folder has both that and hooks.json, so Trove left it alone", e.Path)
}

// CodexHasInlineHooks reports whether config.toml in codexDir has a hooks table.
func CodexHasInlineHooks(codexDir string) bool {
	b, err := readOptional(filepath.Join(codexDir, "config.toml"))
	if err != nil || b == nil {
		return false
	}
	var doc map[string]any
	if toml.Unmarshal(b, &doc) != nil {
		return false
	}
	_, ok := doc["hooks"]
	return ok
}

// PlanClaudeHooks plans adding Trove's hooks to Claude Code's settings.json
// (user scope: ~/.claude/settings.json; project scope: the project's
// .claude/settings.local.json, which is not committed). Verified against code.claude.com/docs/en/hooks on 2026-10-29:
// hooks.<Event> is an array of {matcher, hooks:[{type:"command", command,
// timeout}]} groups, and groups from every settings file run together.
func PlanClaudeHooks(settingsPath string, specs []HookSpec, base string) (*Edit, error) {
	return planHooks(ClaudeHooks, settingsPath, specs, base)
}

// PlanCodexHooks plans adding Trove's hooks to codexDir/hooks.json. The matcher
// group layout is the one in the Codex hooks page (learn.chatgpt.com/docs/hooks,
// checked 2026-10-30). Codex asks the user to approve new hooks with /hooks.
func PlanCodexHooks(codexDir string, specs []HookSpec, base string) (*Edit, error) {
	if CodexHasInlineHooks(codexDir) {
		return nil, &InlineHooksError{Path: filepath.Join(codexDir, "config.toml")}
	}
	return planHooks(CodexHooks, filepath.Join(codexDir, "hooks.json"), specs, base)
}

// planHooks merges specs into the file at path. Every command in specs must start
// with base, which is how undo recognises Trove's own handlers. Existing groups
// and unrelated keys are never changed; a handler already present is skipped.
func planHooks(agent, path string, specs []HookSpec, base string) (*Edit, error) {
	old, err := readOptional(path)
	if err != nil {
		return nil, err
	}
	doc := map[string]json.RawMessage{}
	if old != nil {
		if err := json.Unmarshal(old, &doc); err != nil {
			return nil, fmt.Errorf("%s is not valid JSON (%v). Fix or move that file, then run `trove hooks install` again; Trove will not overwrite a file it cannot read", path, err)
		}
	}
	events := map[string]json.RawMessage{}
	if raw, ok := doc["hooks"]; ok {
		if err := json.Unmarshal(raw, &events); err != nil {
			return nil, fmt.Errorf("the \"hooks\" entry in %s is not an object of events, so Trove left it alone. Fix it or add the hooks by hand (see docs/hooks.md)", path)
		}
	}
	var shown []string
	for _, s := range specs {
		if !strings.HasPrefix(s.Command, base) {
			return nil, fmt.Errorf("internal error: hook command %q does not start with %q", s.Command, base)
		}
		var groups []json.RawMessage
		if raw, ok := events[s.Event]; ok {
			if err := json.Unmarshal(raw, &groups); err != nil {
				return nil, fmt.Errorf("hooks.%s in %s is not a list, so Trove left it alone. Fix it or add the hook by hand (see docs/hooks.md)", s.Event, path)
			}
		}
		if hasHandler(groups, s.Command) {
			continue
		}
		group := struct {
			Matcher string        `json:"matcher,omitempty"`
			Hooks   []hookHandler `json:"hooks"`
		}{s.Matcher, []hookHandler{{Type: "command", Command: s.Command, Timeout: s.Timeout}}}
		g, _ := json.Marshal(group)
		groups = append(groups, g)
		events[s.Event], _ = json.Marshal(groups)
		shown = append(shown, fmt.Sprintf("hooks.%s += %s", s.Event, g))
	}
	e := &Edit{Agent: agent, Path: path, value: base, old: old,
		Show:    strings.Join(shown, "\n"),
		Summary: "Add Trove's hooks, which skip re-reads of unchanged files and write a receipt when a session ends.",
		Note:    "Your existing hooks are not touched. Every hook exits quietly if anything goes wrong, so it cannot stop the agent. `trove undo` removes only Trove's entries.",
	}
	if len(shown) == 0 {
		e.NoOp = true
		return e, nil
	}
	doc["hooks"], _ = json.Marshal(events)
	out, err := marshalIndent(doc)
	if err != nil {
		return nil, err
	}
	e.updated = out
	return e, nil
}

type hookHandler struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

// groupHandlers reads a matcher group's handlers without losing their other keys.
func groupHandlers(group json.RawMessage) (map[string]json.RawMessage, []json.RawMessage, bool) {
	g := map[string]json.RawMessage{}
	if json.Unmarshal(group, &g) != nil {
		return nil, nil, false
	}
	var hs []json.RawMessage
	if raw, ok := g["hooks"]; ok && json.Unmarshal(raw, &hs) != nil {
		return nil, nil, false
	}
	return g, hs, true
}

func handlerCommand(h json.RawMessage) string {
	var c struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(h, &c)
	return c.Command
}

func hasHandler(groups []json.RawMessage, command string) bool {
	for _, grp := range groups {
		_, hs, ok := groupHandlers(grp)
		if !ok {
			continue
		}
		for _, h := range hs {
			if handlerCommand(h) == command {
				return true
			}
		}
	}
	return false
}

// stripHookGroups removes Trove's handlers (commands starting with base) and
// nothing else: groups left with no handlers are dropped, and so are events left
// with no groups. Handlers the user added, even inside a group Trove created, stay.
func stripHookGroups(cur []byte, base string) ([]byte, error) {
	doc := map[string]json.RawMessage{}
	if err := json.Unmarshal(cur, &doc); err != nil {
		return nil, errors.New("the file is no longer valid JSON; remove the Trove entries under \"hooks\" by hand (their commands contain `hook`)")
	}
	events := map[string]json.RawMessage{}
	if raw, ok := doc["hooks"]; ok {
		if json.Unmarshal(raw, &events) != nil {
			return cur, nil // not the shape Trove wrote; leave it
		}
	}
	for ev, raw := range events {
		var groups []json.RawMessage
		if json.Unmarshal(raw, &groups) != nil {
			continue
		}
		var kept []json.RawMessage
		for _, grp := range groups {
			g, hs, ok := groupHandlers(grp)
			if !ok {
				kept = append(kept, grp)
				continue
			}
			var left []json.RawMessage
			for _, h := range hs {
				if !strings.HasPrefix(handlerCommand(h), base) {
					left = append(left, h)
				}
			}
			switch {
			case len(left) == len(hs):
				kept = append(kept, grp)
			case len(left) > 0:
				g["hooks"], _ = json.Marshal(left)
				ng, _ := json.Marshal(g)
				kept = append(kept, ng)
			}
		}
		if len(kept) == 0 {
			delete(events, ev)
		} else {
			events[ev], _ = json.Marshal(kept)
		}
	}
	if len(events) == 0 {
		delete(doc, "hooks")
	} else {
		doc["hooks"], _ = json.Marshal(events)
	}
	return marshalIndent(doc)
}

// HooksInstalled lists the files whose hooks Trove added, from the manifest.
func HooksInstalled(dataDir string) ([]Change, error) {
	cs, err := LoadChanges(dataDir)
	if err != nil {
		return nil, err
	}
	var out []Change
	for _, c := range cs {
		if c.Agent == ClaudeHooks || c.Agent == CodexHooks {
			out = append(out, c)
		}
	}
	return out, nil
}
