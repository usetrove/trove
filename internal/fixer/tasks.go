package fixer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/usetrove/trove/internal/store"
	"github.com/usetrove/trove/internal/tasks"
)

// Fix kinds for the scout subagent and the cascade workflow (docs/tasks.md).
// They are the same strings as the feature names used as optimizer names.
const (
	TaskScout   = tasks.FeatureScout
	TaskCascade = tasks.FeatureCascade
)

// TasksRequest asks Detect to plan the files of one tasks feature. Nil leaves
// `trove fix` exactly as it was.
type TasksRequest struct {
	Feature string        `json:"feature"` // tasks.FeatureScout or tasks.FeatureCascade
	Options tasks.Options `json:"options"`
}

// PlanTasks renders the files for e.Tasks without writing anything.
func PlanTasks(e *Env) ([]tasks.File, error) {
	if e.Tasks == nil {
		return nil, errors.New("no tasks feature was requested")
	}
	cat, err := tasks.Builtin()
	if err != nil {
		return nil, fmt.Errorf("the tasks catalog could not be loaded: %w", err)
	}
	switch e.Tasks.Feature {
	case tasks.FeatureScout:
		return tasks.ScoutFiles(cat, e.Tasks.Options)
	case tasks.FeatureCascade:
		return tasks.CascadeFiles(cat, e.Tasks.Options)
	}
	return nil, fmt.Errorf("unknown feature %q; use %s or %s", e.Tasks.Feature, tasks.FeatureScout, tasks.FeatureCascade)
}

func detectTasks(_ context.Context, e *Env) ([]*Fix, error) {
	if e.Tasks == nil {
		return nil, nil
	}
	if e.ProjectDir == "" {
		return nil, errors.New("the scout and cascade files are written per project; run this from a project folder or pass --project")
	}
	files, err := PlanTasks(e)
	if err != nil {
		return nil, err
	}
	oh := tasks.OverheadOf(files)
	var out []*Fix
	for _, tf := range files {
		path := filepath.Join(e.ProjectDir, filepath.FromSlash(tf.Path))
		old, _ := e.readFile(path)
		f := &Fix{
			ID:   fixID(e.Tasks.Feature, path),
			Kind: e.Tasks.Feature, Agent: "claude_code", Path: path, Suggestion: true,
			Task: e.Tasks.Feature, Keys: []string{tf.Role}, old: old,
		}
		f.Title = taskTitle(tf)
		f.Why = taskWhy(e.Tasks.Feature)
		f.Notes = append(f.Notes,
			fmt.Sprintf("Overhead (estimated): about %d tokens in every request for the descriptions and context text, and about %d more each time the feature is used. Trove charges this against any saving it reports.", oh.PerRequestTokens, oh.PerUseTokens),
			"No saving is estimated yet: Trove has no measured result for this. After you apply it, `trove report` compares your sessions, and labels the result estimated until there are enough sessions to call it measured.",
			"Applied only between sessions, backed up, and reversed with `trove undo`.")
		if e.Tasks.Feature == tasks.FeatureCascade && e.getenv("CLAUDE_CODE_SUBAGENT_MODEL_FORCE") != "" {
			f.Blocked = "CLAUDE_CODE_SUBAGENT_MODEL_FORCE is set, so Claude Code ignores the model in every subagent file and the cascade could not use a cheaper model. Unset it first"
			out = append(out, f)
			continue
		}
		upd, blocked := taskContent(tf, old)
		switch {
		case blocked != "":
			f.Blocked = blocked
		case upd == nil:
			continue // already in place
		default:
			f.updated = upd
		}
		out = append(out, f)
	}
	return out, nil
}

func taskTitle(tf tasks.File) string {
	switch tf.Role {
	case tasks.RoleScout:
		return "Add the read-only scout subagent (.claude/agents/" + tasks.ScoutName + ".md)"
	case tasks.RoleSnippet:
		return "Tell the main agent when to use the scout (a short block in CLAUDE.md)"
	case tasks.RoleWorker:
		return "Add the cheap-model worker subagent for the cascade"
	case tasks.RoleEscalate:
		return "Add the strong-model escalation subagent for the cascade"
	}
	return "Add the /" + tasks.CascadeName + " command"
}

func taskWhy(feature string) string {
	if feature == tasks.FeatureScout {
		return "Exploration reads many files into the main conversation. A scout on a small model, limited to read-only tools, returns a short summary instead, which keeps those tokens out of the main context."
	}
	return "Start a task on a cheaper model and move to the stronger one only when your tests fail, so strong-model tokens are spent only where the cheap attempt was not enough."
}

// taskContent returns the new file content, nil when nothing changes, or a
// reason the file must not be touched.
func taskContent(tf tasks.File, old []byte) ([]byte, string) {
	if tf.Role == tasks.RoleSnippet {
		if o := string(old); strings.Contains(o, tasks.SnippetOpen) && !strings.Contains(o, tasks.SnippetClose) {
			return nil, "CLAUDE.md has Trove's opening marker but no closing one, so Trove left it alone rather than risk your own lines. Remove the line " + tasks.SnippetOpen + " by hand and run this again"
		}
		upd := upsertBlock(old, tf.Content)
		if bytes.Equal(upd, old) && old != nil {
			return nil, ""
		}
		return upd, ""
	}
	if len(old) > 0 && !bytes.Contains(old, []byte(tasks.Managed)) {
		return nil, "a file with this name already exists and Trove did not write it. Rename or remove it, or edit it yourself; Trove never overwrites your own agent or command files"
	}
	if bytes.Equal(old, []byte(tf.Content)) {
		return nil, ""
	}
	return []byte(tf.Content), ""
}

// upsertBlock replaces Trove's marked block in src, or appends it. Text outside
// the markers is never changed. A damaged block (an opening marker with no
// close) is left alone and the block appended again would double it, so that
// case appends nothing new: the caller sees no change.
func upsertBlock(src []byte, block string) []byte {
	s := string(src)
	i := strings.Index(s, tasks.SnippetOpen)
	if i < 0 {
		sep := ""
		if len(s) > 0 && !strings.HasSuffix(s, "\n") {
			sep = "\n"
		}
		if len(s) > 0 {
			sep += "\n"
		}
		return []byte(s + sep + block)
	}
	j := strings.Index(s[i:], tasks.SnippetClose)
	if j < 0 {
		return src
	}
	end := i + j + len(tasks.SnippetClose)
	if end < len(s) && s[end] == '\n' {
		end++
	}
	return []byte(s[:i] + block + s[end:])
}

// registerTask records an applied scout or cascade file so measurement can
// compare sessions before and after it. Model aliases and token estimates only:
// never a path, the test command or file content.
func registerTask(db *store.DB, e *Env, f *Fix, changeID string) error {
	if e.Tasks == nil || len(f.Keys) == 0 {
		return nil
	}
	o := e.Tasks.Options.Defaults()
	files, err := PlanTasks(e)
	if err != nil {
		return err
	}
	oh := tasks.OverheadOf(files)
	_, err = db.ExecContext(context.Background(),
		`INSERT OR REPLACE INTO task_installs (change_id, fix_id, feature, role, scout_model, worker_model, escalation_model, overhead_request, overhead_use)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		changeID, f.ID, f.Kind, f.Keys[0], o.ScoutModel, o.WorkerModel, o.EscalationModel, oh.PerRequestTokens, oh.PerUseTokens)
	return err
}
