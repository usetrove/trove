package fixer

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/usetrove/trove/internal/tasks"
)

// detectSubagentModel plans one fix per subagent file in the project's .claude/agents
// that names no model, adding "model: <chosen>" to its frontmatter. Files that name a
// model, including "inherit", are the user's choice and are skipped (rule 6).
func detectSubagentModel(e *Env) ([]*Fix, error) {
	model := e.Tasks.Options.SubagentModel
	dir := filepath.Join(e.ProjectDir, ".claude", "agents")
	ents, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("could not list %s: %w", dir, err)
	}
	var names []string
	for _, en := range ents {
		if en.Type().IsRegular() && strings.HasSuffix(en.Name(), ".md") {
			names = append(names, en.Name())
		}
	}
	sort.Strings(names)
	var out []*Fix
	for _, n := range names {
		path := filepath.Join(dir, n)
		old, err := e.readFile(path)
		if err != nil {
			continue
		}
		upd, why := tasks.SetModel(old, model)
		if upd == nil && why == "" {
			continue // already names a model
		}
		if upd == nil && strings.HasPrefix(why, "Trove wrote") {
			continue
		}
		f := &Fix{
			ID: fixID(TaskSubagentModel, path), Kind: TaskSubagentModel, Agent: "claude_code", Path: path,
			Suggestion: true, Task: TaskSubagentModel, Keys: []string{tasks.RoleSubagent}, old: old,
			Title: "Run the subagent " + strings.TrimSuffix(n, ".md") + " on " + model,
			Why:   "This subagent names no model, so it runs on the main conversation's model. It works in its own context window, so a cheaper model here leaves the main conversation and its cache untouched.",
		}
		f.Notes = append(f.Notes,
			"Overhead: none. The model line is read by Claude Code, not sent to the model.",
			"No saving is estimated yet. After you apply it, `trove report` compares sessions before and after; a session A/B is not possible, because a file cannot be switched off for half of the sessions.",
			"Applied only between sessions, backed up, and reversed with `trove undo`.")
		switch {
		case e.getenv("CLAUDE_CODE_SUBAGENT_MODEL_FORCE") != "":
			f.Blocked = "CLAUDE_CODE_SUBAGENT_MODEL_FORCE is set, so Claude Code ignores the model in every subagent file. Unset it first"
		case e.getenv("CLAUDE_CODE_SUBAGENT_MODEL") != "":
			f.Blocked = "CLAUDE_CODE_SUBAGENT_MODEL is set, so you already chose a subagent model; a model line in the file would override it. Unset it first if you want per-file models"
		case why != "":
			f.Blocked = why
		default:
			f.updated = upd
		}
		out = append(out, f)
	}
	return out, nil
}
