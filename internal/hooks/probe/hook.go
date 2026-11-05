package probe

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// setup creates the throwaway project for one tool and returns the prompt that
// makes the model call that tool exactly once.
func setup(project string, p plan) (string, error) {
	write := func(name, body string) error {
		return os.WriteFile(filepath.Join(project, name), []byte(body), 0o600)
	}
	tail := " Then reply with only the exact text the tool returned, nothing else."
	switch p.Tool {
	case "Bash":
		return "Use the Bash tool exactly once to run: echo TROVE_PROBE_SRC." + tail, nil
	case "Read":
		var b strings.Builder
		for i := 1; i <= 30; i++ {
			fmt.Fprintf(&b, "L%02d probe line\n", i)
		}
		if err := write("probe.txt", b.String()); err != nil {
			return "", err
		}
		return "Use the Read tool exactly once on " + filepath.Join(project, "probe.txt") + "." + tail, nil
	case "Grep":
		if err := write("a.txt", "TROVE_PROBE_SRC is here\n"); err != nil {
			return "", err
		}
		if err := write("b.txt", p.MarkerIn+" is here\n"); err != nil {
			return "", err
		}
		return "Use the Grep tool exactly once to search for TROVE_PROBE_SRC in " + project + "." + tail, nil
	case "Glob":
		if err := write("a.src", "x\n"); err != nil {
			return "", err
		}
		if err := write(p.MarkerIn+".dat", "x\n"); err != nil {
			return "", err
		}
		return "Use the Glob tool exactly once with the pattern *.src in " + project + "." + tail, nil
	}
	return "", fmt.Errorf("%s is not probed by default; it needs the network and its own permission", p.Tool)
}

// HookMain is the probe hook the agent runs. mode is "pre" or "post". It reads
// the hook input from stdin, appends what it saw to the state directory named by
// StateEnv, and writes a replacement if it can build one. It never fails the
// call: on any problem it prints nothing.
func HookMain(mode string, stdin io.Reader, stdout io.Writer, getenv func(string) string) int {
	state := getenv(StateEnv)
	if state == "" {
		return 0
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
	if err != nil {
		return 0
	}
	var in struct {
		Tool     string          `json:"tool_name"`
		Input    json.RawMessage `json:"tool_input"`
		Response json.RawMessage `json:"tool_response"`
	}
	pb, perr := os.ReadFile(filepath.Join(state, "plan.json")) //nolint:gosec // our own temp dir
	var p plan
	if json.Unmarshal(raw, &in) != nil || perr != nil || json.Unmarshal(pb, &p) != nil || in.Tool != p.Tool {
		return 0
	}
	s := seen{Mode: mode, Tool: in.Tool, Input: in.Input, Raw: raw}
	var out []byte
	switch mode {
	case "pre":
		if upd, ok := rewriteInput(p, in.Input); ok {
			s.Replaced = true
			out, _ = json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
				"hookEventName": "PreToolUse", "updatedInput": upd}})
		}
	case "post":
		s.Response = in.Response
		if upd, note, ok := replaceMarker(in.Response, p.MarkerOut); ok {
			s.Replaced = true
			out, _ = json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
				"hookEventName": "PostToolUse", "updatedToolOutput": upd}})
		} else {
			s.ShapeNote = note
		}
	}
	line, _ := json.Marshal(s)
	if f, err := os.OpenFile(filepath.Join(state, "seen.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil { //nolint:gosec // our own temp dir
		_, _ = f.Write(append(line, '\n'))
		_ = f.Close()
	}
	if len(out) > 0 {
		_, _ = stdout.Write(out)
	}
	return 0
}

// rewriteInput builds the probe's replacement input: the whole object with one
// field changed, which is how updatedInput works.
func rewriteInput(p plan, in json.RawMessage) (json.RawMessage, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(in, &m) != nil || m == nil {
		return nil, false
	}
	set := func(k string, v any) bool {
		b, err := json.Marshal(v)
		if err != nil {
			return false
		}
		m[k] = b
		return true
	}
	var ok bool
	switch p.Tool {
	case "Bash":
		ok = set("command", "echo "+p.MarkerIn)
	case "Read":
		ok = set("limit", 1)
	case "Grep":
		ok = set("pattern", p.MarkerIn)
	case "Glob":
		ok = set("pattern", "*"+p.MarkerIn+"*")
	}
	if !ok {
		return nil, false
	}
	b, err := json.Marshal(m)
	return b, err == nil
}

// replaceMarker returns the tool_response with its main text replaced by marker,
// in the same shape. It knows the shapes the documentation names (Bash) and makes
// a best guess at the others; when it cannot find a text field it says which
// fields there were, so the next probe can be taught.
func replaceMarker(resp json.RawMessage, marker string) (json.RawMessage, string, bool) {
	if len(resp) == 0 {
		return nil, "empty response", false
	}
	var s string
	if json.Unmarshal(resp, &s) == nil {
		b, _ := json.Marshal(marker)
		return b, "", true
	}
	var m map[string]any
	if json.Unmarshal(resp, &m) != nil {
		return nil, "not an object or string", false
	}
	switch {
	case isStr(m["stdout"]):
		m["stdout"] = marker
	case isStr(m["content"]):
		m["content"] = marker
	case isStr(fileContent(m)):
		m["file"].(map[string]any)["content"] = marker
	case isList(m["filenames"]):
		m["filenames"].([]any)[0] = marker
	default:
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		return nil, "fields: " + strings.Join(keys, ","), false
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, "", false
	}
	return b, "", true
}

func isStr(v any) bool { _, ok := v.(string); return ok }

func isList(v any) bool { l, ok := v.([]any); return ok && len(l) > 0 }

func fileContent(m map[string]any) any {
	if f, ok := m["file"].(map[string]any); ok {
		return f["content"]
	}
	return nil
}

var errNoRecord = errors.New("nothing to record")

// record writes redacted copies of the hook inputs the agent really sent, to be
// used as test fixtures. Probe content is synthetic; the agent's own identifiers
// and local paths are replaced by placeholders.
func record(dir, version, tool string, ev []seen) {
	d := filepath.Join(dir, version)
	if os.MkdirAll(d, 0o755) != nil { //nolint:gosec // fixtures are meant to be committed
		return
	}
	for _, e := range ev {
		if b, err := redact(e.Raw); err == nil {
			_ = os.WriteFile(filepath.Join(d, strings.ToLower(tool)+"_"+e.Mode+".json"), b, 0o644) //nolint:gosec // fixtures are meant to be committed
		}
	}
}

var placeholders = map[string]string{
	"session_id": "{{SESSION}}", "prompt_id": "{{PROMPT}}", "tool_use_id": "{{TOOL_USE}}",
	"transcript_path": "{{PATH}}", "cwd": "{{PATH}}", "scratchpad_dir": "{{PATH}}",
}

// redact replaces identifiers and paths in a hook input, leaving the structure
// and the synthetic probe content.
func redact(raw []byte) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if len(m) == 0 {
		return nil, errNoRecord
	}
	for k, ph := range placeholders {
		if _, ok := m[k]; ok {
			m[k] = ph
		}
	}
	return json.MarshalIndent(scrub(m), "", "  ")
}

// probeRootRe matches the throwaway folder the probe creates, with whatever
// precedes it (the user's home and temp folder, which carry their name).
var probeRootRe = regexp.MustCompile(`(?:[A-Za-z]:)?[\\/](?:[^\\/\s'"]+[\\/])*trove-hookprobe-\d+`)

// scrub replaces the probe's folder, wherever it appears in a string, so no
// recorded input carries a user name or a local path.
func scrub(v any) any {
	switch x := v.(type) {
	case string:
		return probeRootRe.ReplaceAllString(x, "{{TMP}}")
	case []any:
		for i := range x {
			x[i] = scrub(x[i])
		}
	case map[string]any:
		for k := range x {
			x[k] = scrub(x[k])
		}
	}
	return v
}
