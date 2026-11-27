package fixer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/usetrove/trove/internal/setup"
	"github.com/usetrove/trove/internal/store"
	"github.com/usetrove/trove/internal/tasks"
)

// Fix kinds for the scout subagent and the cascade workflow (docs/tasks.md).
// They are the same strings as the feature names used as optimizer names.
const (
	TaskScout   = tasks.FeatureScout
	TaskCascade = tasks.FeatureCascade
	// TaskSubagentModel sets a model on the user's own subagent files (plan item 7.3).
	TaskSubagentModel = tasks.FeatureSubagentModel
	// TaskRoutine installs the low-effort test runner and lint fixer subagents and their skills.
	TaskRoutine = tasks.FeatureRoutine
	// TaskExplore overrides the built-in Explore subagent with a low-effort one (separate opt-in).
	TaskExplore = tasks.FeatureExplore
)

// IsTask reports whether kind is one of the tasks features.
func IsTask(kind string) bool {
	return kind == TaskScout || kind == TaskCascade || kind == TaskSubagentModel || kind == TaskRoutine || kind == TaskExplore
}

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
	case tasks.FeatureSubagentModel:
		return nil, e.Tasks.Options.ValidateSubagentModel(cat) // edits the user's own files; renders none
	case tasks.FeatureScout:
		return tasks.ScoutFiles(cat, e.Tasks.Options)
	case tasks.FeatureCascade:
		return tasks.CascadeFiles(cat, e.Tasks.Options)
	case tasks.FeatureRoutine:
		return tasks.RoutineFiles(cat, e.Tasks.Options)
	case tasks.FeatureExplore:
		return tasks.ExploreFiles(cat, e.Tasks.Options)
	}
	return nil, fmt.Errorf("unknown feature %q; use %s, %s, %s or %s", e.Tasks.Feature, tasks.FeatureScout, tasks.FeatureCascade, tasks.FeatureRoutine, tasks.FeatureExplore)
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
	if e.Tasks.Feature == tasks.FeatureSubagentModel {
		return detectSubagentModel(e)
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
		f.Notes = append(f.Notes, envNotes(e)...)
		if blocked := envBlock(e); blocked != "" {
			f.Blocked = blocked
			out = append(out, f)
			continue
		}
		if tf.Role == tasks.RoleExplore {
			if where := userExplore(e); where != "" {
				f.Blocked = "you already have your own subagent named Explore (" + where + "). Trove does not replace it; set model and effort in that file yourself if you want them"
				out = append(out, f)
				continue
			}
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
	case tasks.RoleTestRunner:
		return "Add the low-effort test runner subagent (.claude/agents/" + tasks.TestRunnerName + ".md)"
	case tasks.RoleLintFixer:
		return "Add the low-effort formatter and lint fixer subagent (.claude/agents/" + tasks.LintFixerName + ".md)"
	case tasks.RoleTestSkill:
		return "Add the /" + tasks.TestSkillName + " skill (runs in the test runner)"
	case tasks.RoleLintSkill:
		return "Add the /" + tasks.LintSkillName + " skill (runs in the lint fixer)"
	case tasks.RoleExplore:
		return "Replace the built-in Explore subagent in this project with a low-effort one (.claude/agents/" + tasks.ExploreFile + ".md)"
	}
	return "Add the /" + tasks.CascadeName + " command"
}

func taskWhy(feature string) string {
	switch feature {
	case tasks.FeatureRoutine:
		return "Running tests and fixing formatting are mechanical. In a subagent on a cheaper model at low effort they cost less, and the output stays out of the main conversation, which keeps its own model and effort."
	case tasks.FeatureExplore:
		return "Claude Code's built-in Explore runs on the main conversation's model (Opus when the main model is Fable). A project Explore file replaces it with one on a cheaper model at low effort."
	}
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
	effort := ""
	switch f.Kind {
	case TaskSubagentModel:
		// The model it wrote is recorded as the worker model; it adds no text (overhead 0).
		o = tasks.Options{WorkerModel: e.Tasks.Options.SubagentModel}
	case TaskScout:
		effort = o.ScoutEffort
	case TaskRoutine, TaskExplore:
		// The routine model is recorded as the worker model.
		o = tasks.Options{WorkerModel: o.RoutineModel}
		effort = e.Tasks.Options.Defaults().RoutineEffort
	}
	_, err = db.ExecContext(context.Background(),
		`INSERT OR REPLACE INTO task_installs (change_id, fix_id, feature, role, scout_model, worker_model, escalation_model, overhead_request, overhead_use, effort)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		changeID, f.ID, f.Kind, f.Keys[0], o.ScoutModel, o.WorkerModel, o.EscalationModel, oh.PerRequestTokens, oh.PerUseTokens, effort)
	return err
}

// envBlock is why a routing feature cannot work in this environment, or "".
func envBlock(e *Env) string {
	switch e.Tasks.Feature {
	case tasks.FeatureCascade, tasks.FeatureRoutine, tasks.FeatureExplore:
		if e.getenv("CLAUDE_CODE_SUBAGENT_MODEL_FORCE") != "" {
			return "CLAUDE_CODE_SUBAGENT_MODEL_FORCE is set, so Claude Code ignores the model in every subagent file and this could not use a cheaper model. Unset it first"
		}
	}
	return ""
}

// envNotes are settings that weaken a feature without blocking it. Verified 2026-11-24 on
// code.claude.com/docs/en/sub-agents.md: a subagent's effort "Overrides the session effort level,
// but not the CLAUDE_CODE_EFFORT_LEVEL environment variable".
func envNotes(e *Env) []string {
	switch e.Tasks.Feature {
	case tasks.FeatureScout, tasks.FeatureRoutine, tasks.FeatureExplore:
	default:
		return nil
	}
	var n []string
	if v := e.getenv("CLAUDE_CODE_EFFORT_LEVEL"); v != "" {
		n = append(n, "CLAUDE_CODE_EFFORT_LEVEL is set, and Claude Code lets it win over a subagent's effort field, so these subagents will run at that level instead of low. The cheaper model still applies. Unset it to get the low effort level.")
	}
	n = append(n, "The effort field works only on models that support effort (Haiku 5.5 needs Claude Code v2.1.293 or later); on another model Claude Code ignores it. An organisation's maxEffortLevel caps it, which does not matter for low.")
	return n
}

// userExplore returns where a subagent named Explore that Trove did not write is defined (the
// project's or the user's agents folder), or "". Files are read in memory only.
func userExplore(e *Env) string {
	dirs := []struct{ dir, label string }{{filepath.Join(e.ProjectDir, ".claude", "agents"), "the project's .claude/agents"}}
	if e.ClaudeDir != "" {
		dirs = append(dirs, struct{ dir, label string }{filepath.Join(e.ClaudeDir, "agents"), "your user agents folder"})
	}
	for _, d := range dirs {
		ents, err := os.ReadDir(d.dir)
		if err != nil {
			continue
		}
		for _, ent := range ents {
			if ent.IsDir() || !strings.HasSuffix(strings.ToLower(ent.Name()), ".md") {
				continue
			}
			b, err := e.readFile(filepath.Join(d.dir, ent.Name()))
			if err != nil || bytes.Contains(b, []byte(tasks.Managed)) {
				continue
			}
			if frontmatterName(b) == tasks.ExploreAgent {
				return d.label
			}
		}
	}
	return ""
}

// frontmatterName returns the name field of a file's frontmatter, or "".
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

// TaskChangeIDs lists the recorded change ids of one tasks feature that are still in place in
// e.ProjectDir, newest last, for `trove tasks remove`. It needs the database: the feature of a
// change is only known from task_installs.
func TaskChangeIDs(ctx context.Context, e *Env, feature string) ([]string, error) {
	if e.DB == nil {
		return nil, nil
	}
	rows, err := e.DB.QueryContext(ctx, `SELECT t.change_id FROM task_installs t JOIN fixes f ON f.change_id = t.change_id
		WHERE t.feature = ? AND f.reverted_at IS NULL`, feature)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		want[id] = true
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	cs, err := setup.LoadChanges(e.DataDir)
	if err != nil {
		return nil, err
	}
	proj := norm(resolve(e.ProjectDir)) + "/"
	var out []string
	for _, c := range cs {
		if want[c.ID] && strings.HasPrefix(norm(resolve(c.Path)), proj) {
			out = append(out, c.ID)
		}
	}
	return out, nil
}
