package setup

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Agent identifiers for Cursor's and Gemini CLI's hook registrations in the manifest.
const (
	CursorHooks = "cursor_hooks"
	GeminiHooks = "gemini_cli_hooks"
)

// PlanGeminiHooks plans adding Trove's hooks to a Gemini CLI settings.json (user:
// ~/.gemini/settings.json; project: .gemini/settings.json). Verified against
// geminicli.com/docs/hooks/ on 2026-11-17: hooks.<Event> is a list of {matcher,
// hooks:[{type:"command", command, timeout}]} groups, as in Claude Code, but timeout
// is in milliseconds, so specs must carry milliseconds.
func PlanGeminiHooks(settingsPath string, specs []HookSpec, base string) (*Edit, error) {
	return planHooks(GeminiHooks, settingsPath, specs, base)
}

// PlanCursorHooks plans adding Trove's hooks to a Cursor hooks.json (user:
// ~/.cursor/hooks.json). Verified against cursor.com/docs/agent/hooks on 2026-11-17:
// the file has "version": 1 and hooks.<event> is a flat list of {command, type,
// timeout (seconds), matcher} entries. Entries Trove did not write are never
// changed; an entry already present is skipped.
func PlanCursorHooks(path string, specs []HookSpec, base string) (*Edit, error) {
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
	if _, ok := doc["version"]; !ok {
		doc["version"] = json.RawMessage("1")
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
		var list []json.RawMessage
		if raw, ok := events[s.Event]; ok {
			if err := json.Unmarshal(raw, &list); err != nil {
				return nil, fmt.Errorf("hooks.%s in %s is not a list, so Trove left it alone. Fix it or add the hook by hand (see docs/hooks.md)", s.Event, path)
			}
		}
		present := false
		for _, h := range list {
			present = present || handlerCommand(h) == s.Command
		}
		if present {
			continue
		}
		entry := struct {
			Command string `json:"command"`
			Type    string `json:"type"`
			Timeout int    `json:"timeout,omitempty"`
			Matcher string `json:"matcher,omitempty"`
		}{s.Command, "command", s.Timeout, s.Matcher}
		b, _ := json.Marshal(entry)
		list = append(list, b)
		events[s.Event], _ = json.Marshal(list)
		shown = append(shown, fmt.Sprintf("hooks.%s += %s", s.Event, b))
	}
	e := &Edit{Agent: CursorHooks, Path: path, value: base, old: old,
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

// stripCursorHooks removes Trove's entries (commands starting with base) from a
// Cursor hooks.json and nothing else; events left empty are dropped.
func stripCursorHooks(cur []byte, base string) ([]byte, error) {
	doc := map[string]json.RawMessage{}
	if err := json.Unmarshal(cur, &doc); err != nil {
		return nil, errors.New("the file is no longer valid JSON; remove the Trove entries under \"hooks\" by hand (their commands contain `hook`)")
	}
	events := map[string]json.RawMessage{}
	if raw, ok := doc["hooks"]; ok && json.Unmarshal(raw, &events) != nil {
		return cur, nil // not the shape Trove wrote; leave it
	}
	for ev, raw := range events {
		var list []json.RawMessage
		if json.Unmarshal(raw, &list) != nil {
			continue
		}
		var kept []json.RawMessage
		for _, h := range list {
			if !strings.HasPrefix(handlerCommand(h), base) {
				kept = append(kept, h)
			}
		}
		if len(kept) == len(list) {
			continue
		}
		if len(kept) == 0 {
			delete(events, ev)
		} else {
			events[ev], _ = json.Marshal(kept)
		}
	}
	doc["hooks"], _ = json.Marshal(events)
	return marshalIndent(doc)
}
