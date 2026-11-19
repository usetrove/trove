package hooks

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/usetrove/trove/internal/tasks"
)

// subagentModelControl is the "off" arm of the model-per-subagent trial (plan item 7.3). A subagent whose file
// carries a model line Trove wrote (tasks.ModelMarker) would run on that model; in an off session the Agent call's
// per-invocation model is set to the main conversation's model instead, which is what the subagent ran on before
// the fix. The per-invocation model wins over the file and takes only an alias (checked live 2026-11-18 on Claude
// Code 2.1.327), so the main model is mapped to its family alias. Everything uncertain goes through untouched
// (rule 4): an unpinned or "on" session, a rolled-back feature, a call that names a model itself, a subagent the
// user configured, an unreadable transcript, a model with no alias. It returns the PreToolUse reply, or nil.
func (h *handler) subagentModelControl(in Input, key string) []byte {
	if loadSession(h.dir, key).Pins[tasks.FeatureSubagentModel] != stateOff {
		return nil
	}
	for _, n := range h.snap.RolledBack {
		if n == tasks.FeatureSubagentModel {
			return nil
		}
	}
	if in.ToolInput.SubagentType == "" || in.Cwd == "" || !troveSetModelFor(in.Cwd, in.ToolInput.SubagentType) {
		return nil
	}
	alias, ok := tasks.ModelAlias(mainModel(in.TranscriptPath))
	if !ok {
		return nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(in.ToolInput.Raw, &fields) != nil || fields == nil {
		return nil
	}
	if _, set := fields["model"]; set {
		return nil // the agent chose a model for this call; that is not the fix's doing
	}
	fields["model"], _ = json.Marshal(alias)
	raw, err := json.Marshal(fields)
	if err != nil {
		return nil
	}
	return updatedInputJSON(raw, "")
}

// troveSetModelFor reports whether the project subagent called name has a model line Trove wrote. Claude Code
// matches subagent_type against the frontmatter name, so every file in .claude/agents is checked by that field.
// Files are read in memory only; nothing about them is stored.
func troveSetModelFor(cwd, name string) bool {
	dir := filepath.Join(cwd, ".claude", "agents")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range ents {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		b, err := readCapped(filepath.Join(dir, e.Name()), 64<<10)
		if err != nil || frontmatterName(b) != name {
			continue
		}
		return tasks.TroveSetModel(b)
	}
	return false
}

// frontmatterName returns the name: field of a --- frontmatter block, or "".
func frontmatterName(b []byte) string {
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	if !strings.HasPrefix(s, "---\n") {
		return ""
	}
	for _, line := range strings.Split(s[4:], "\n") {
		if line == "---" {
			break
		}
		if v, ok := strings.CutPrefix(line, "name:"); ok {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

// mainModel returns the model of the newest main-conversation (not subagent) assistant message in a Claude Code
// transcript, reading at most its last 1 MiB. "" when there is none.
func mainModel(path string) string {
	if path == "" {
		return ""
	}
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	const tail = 1 << 20
	if st, err := f.Stat(); err == nil && st.Size() > tail {
		if _, err := f.Seek(st.Size()-tail, io.SeekStart); err != nil {
			return ""
		}
	}
	model := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), tail)
	for sc.Scan() {
		ln := sc.Bytes()
		if !bytes.Contains(ln, []byte(`"assistant"`)) {
			continue
		}
		var l struct {
			Type        string `json:"type"`
			IsSidechain bool   `json:"isSidechain"`
			Message     *struct {
				Model string `json:"model"`
			} `json:"message"`
		}
		if json.Unmarshal(ln, &l) == nil && l.Type == "assistant" && !l.IsSidechain && l.Message != nil && strings.HasPrefix(l.Message.Model, "claude-") {
			model = l.Message.Model
		}
	}
	return model
}

func readCapped(path string, n int64) ([]byte, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, n))
}
