// Package tasks defines Trove's two delegation optimizations for Claude Code:
// a read-only scout subagent on a small model, and a cascade workflow that
// starts a task on a cheap model and escalates to a strong one only when the
// project's tests fail (see docs/tasks.md).
//
// The package only renders files. It is a pure, deterministic function of its
// Options, so the same options always give byte-identical output. Writing goes
// through internal/fixer (diff, approval, backup, undo). No optimization logic
// lives in the rendered files beyond instructions to the agent.
package tasks

import (
	"embed"
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Names of the files Trove writes. All start with "trove-" so the Fixer can
// allow exactly these and never touch a file the user made.
const (
	ScoutName    = "trove-scout"
	WorkerName   = "trove-worker"
	EscalateName = "trove-escalate"
	CascadeName  = "trove-cascade"
)

// Roles of the rendered files.
const (
	RoleScout    = "scout"
	RoleWorker   = "worker"
	RoleEscalate = "escalate"
	RoleCascade  = "cascade"
	RoleSnippet  = "snippet"
)

// Feature names, used as optimizer names in config and experiments.
const (
	FeatureScout   = "task_scout"
	FeatureCascade = "task_cascade"
)

// SnippetOpen and SnippetClose wrap the context-file block so it can be found
// and replaced without touching the user's own text.
const (
	SnippetOpen  = "<!-- trove:scout -->"
	SnippetClose = "<!-- /trove:scout -->"
)

// Managed marks a file Trove wrote, so a later run may update it and never
// overwrites a file of the same name that the user wrote.
const Managed = "<!-- trove:managed -->"

//go:embed catalog/*.toml
var catalogFS embed.FS

// Options are the user-tunable values. Zero fields take the defaults.
type Options struct {
	ScoutModel      string `json:"scout_model"`      // model alias for the scout (default haiku)
	WorkerModel     string `json:"worker_model"`     // cheap model for the first attempt (default haiku)
	EscalationModel string `json:"escalation_model"` // strong model for the retry (default opus)
	TestCmd         string `json:"test_cmd"`         // the project's test command; no default
	MaxTurns        int    `json:"max_turns"`        // worker turn limit before it must stop (default 25)
}

// Defaults fills empty fields.
func (o Options) Defaults() Options {
	if o.ScoutModel == "" {
		o.ScoutModel = "haiku"
	}
	if o.WorkerModel == "" {
		o.WorkerModel = "haiku"
	}
	if o.EscalationModel == "" {
		o.EscalationModel = "opus"
	}
	if o.MaxTurns == 0 {
		o.MaxTurns = 25
	}
	return o
}

var fullModelID = regexp.MustCompile(`^claude-[a-z0-9.-]{3,60}$`)

// Validate checks the options against the catalog (which model values are
// documented) and rejects a test command that could break the rendered file.
// needTest is true for the cascade, which cannot run without one.
func (o Options) Validate(c *Catalog, needTest bool) error {
	o = o.Defaults()
	for _, m := range []struct{ field, v string }{
		{"scout model", o.ScoutModel}, {"worker model", o.WorkerModel}, {"escalation model", o.EscalationModel},
	} {
		if !c.ModelOK(m.v) {
			return fmt.Errorf("the %s %q is not one Claude Code documents (%s, or a full model ID such as claude-opus-5-5). Change it under [tasks] in config.toml", m.field, m.v, strings.Join(c.ModelAliases(), ", "))
		}
	}
	if o.MaxTurns < 1 || o.MaxTurns > 200 {
		return fmt.Errorf("max_turns is %d; use a number from 1 to 200 (default 25)", o.MaxTurns)
	}
	if needTest {
		t := strings.TrimSpace(o.TestCmd)
		switch {
		case t == "":
			return fmt.Errorf("the cascade needs your project's test command. Set test_cmd under [tasks] in config.toml, for example test_cmd = \"go test ./...\"")
		case strings.ContainsAny(t, "\r\n`") || len(t) > 200:
			return fmt.Errorf("test_cmd must be one line of at most 200 characters without backticks")
		}
	}
	return nil
}

// File is one rendered file. Path is relative to the project folder.
type File struct {
	Role    string `json:"role"`
	Path    string `json:"path"`
	Content string `json:"-"`
	// AlwaysTokens is the estimated size of the text that is in the main
	// conversation's context on every request whether or not the file is used
	// (a subagent's description, the context snippet). BodyTokens is loaded only
	// when the file is used. Both are estimates (rule 15).
	AlwaysTokens int `json:"always_tokens"`
	BodyTokens   int `json:"body_tokens"`
}

// EstimateTokens estimates tokens in text at 3.5 bytes per token, the same
// conversion internal/external uses. It is an estimate, never measured.
func EstimateTokens(text string) int { return int(math.Ceil(float64(len(text)) / 3.5)) }

func agentFile(role, name, desc, tools, model string, maxTurns int, body string) File {
	var b strings.Builder
	b.WriteString("---\nname: " + name + "\ndescription: " + desc + "\ntools: " + tools + "\nmodel: " + model + "\n")
	if maxTurns > 0 {
		fmt.Fprintf(&b, "maxTurns: %d\n", maxTurns)
	}
	body = Managed + "\n" + body
	b.WriteString("---\n\n" + body)
	return File{Role: role, Path: ".claude/agents/" + name + ".md", Content: b.String(),
		AlwaysTokens: EstimateTokens(desc), BodyTokens: EstimateTokens(body)}
}

// ScoutFiles renders the scout subagent and the context-file snippet.
func ScoutFiles(c *Catalog, o Options) ([]File, error) {
	o = o.Defaults()
	if err := o.Validate(c, false); err != nil {
		return nil, err
	}
	scout := agentFile(RoleScout, ScoutName,
		"Read-only explorer. Use proactively to search the codebase, find where something is defined or used, or summarize long logs, instead of reading many files yourself. Returns a short summary.",
		strings.Join(c.ReadOnlyTools, ", "), o.ScoutModel, 20,
		"You explore code and logs for another agent. You can only read, search and list files.\n\n"+
			"Rules:\n"+
			"- Answer the question you were given and nothing else.\n"+
			"- Reply in at most 15 lines: file paths with line numbers, and one sentence each.\n"+
			"- Never paste long file contents or whole logs. Quote at most 3 lines when it matters.\n"+
			"- If you did not find it, say so and list where you looked.\n")
	snip := SnippetOpen + "\n" +
		"## Exploring\n" +
		"For open-ended exploration (finding where code lives, tracing usage, summarizing long logs), delegate to the `" + ScoutName + "` subagent and work from its short summary. Read files yourself only once you know which ones matter.\n" +
		SnippetClose + "\n"
	return []File{scout, {Role: RoleSnippet, Path: "CLAUDE.md", Content: snip, AlwaysTokens: EstimateTokens(snip)}}, nil
}

// CascadeFiles renders the worker and escalation subagents and the command.
func CascadeFiles(c *Catalog, o Options) ([]File, error) {
	o = o.Defaults()
	if err := o.Validate(c, true); err != nil {
		return nil, err
	}
	test := strings.TrimSpace(o.TestCmd)
	worker := agentFile(RoleWorker, WorkerName,
		"First-attempt implementer for /"+CascadeName+". Used only by that command.",
		"Read, Grep, Glob, Edit, Write, Bash", o.WorkerModel, o.MaxTurns,
		"You implement the task you are given in this project, then stop. You do not decide whether it worked; the caller runs the tests.\n\n"+
			"When you finish, reply with: what you changed (file paths), and anything you were unsure about. Keep it under 10 lines.\n")
	esc := agentFile(RoleEscalate, EscalateName,
		"Stronger second attempt for /"+CascadeName+". Used only by that command, only after tests failed.",
		"Read, Grep, Glob, Edit, Write, Bash", o.EscalationModel, 0,
		"A cheaper model tried this task and the tests still fail. You get a short summary of what was tried and the failing test names, not the full history.\n\n"+
			"Fix the task properly. Re-read the code you need; do not assume the earlier attempt was right. When done, reply with what you changed in under 10 lines.\n")
	body := "Run this task with the cheapest model that can do it, and escalate only if the tests prove it failed.\n\n" +
		"Task: $ARGUMENTS\n\n" +
		"Steps:\n" +
		"1. Delegate the task to the `" + WorkerName + "` subagent (model " + o.WorkerModel + ").\n" +
		"2. Run the project's tests yourself with exactly: `" + test + "`. Do not trust the subagent's report.\n" +
		"3. If the tests pass, you are done. Go to step 6 with \"" + o.WorkerModel + "\".\n" +
		"4. If they fail, or the subagent stopped without finishing (it is limited to " + fmt.Sprint(o.MaxTurns) + " turns), write a summary of at most 10 lines: what was tried, the names of the failing tests, and the first error line of each. Do not include code or the conversation.\n" +
		"5. Delegate to the `" + EscalateName + "` subagent (model " + o.EscalationModel + ") with that summary and the original task, then run the same test command again.\n" +
		"6. Finish by telling the user in one sentence which model finished the task and why: either \"" + o.WorkerModel + " finished it; tests passed on the first attempt\", or \"escalated to " + o.EscalationModel + " because: <failing tests>\", and whether tests passed at the end. Never escalate without saying so.\n"
	cmd := "---\ndescription: Run a task on a cheap model; escalate to a stronger one only if tests fail\nargument-hint: [task]\n---\n\n" + Managed + "\n" + body
	files := []File{worker, esc, {Role: RoleCascade, Path: ".claude/commands/" + CascadeName + ".md", Content: cmd,
		AlwaysTokens: EstimateTokens("Run a task on a cheap model; escalate to a stronger one only if tests fail"),
		BodyTokens:   EstimateTokens(body)}}
	return files, nil
}

// Overhead is what a feature adds to every request (always-loaded text) and per
// use (bodies). Estimated, charged against measured savings (rule 15).
type Overhead struct {
	PerRequestTokens int `json:"per_request_tokens"`
	PerUseTokens     int `json:"per_use_tokens"`
}

// OverheadOf sums the files.
func OverheadOf(fs []File) Overhead {
	var o Overhead
	for _, f := range fs {
		o.PerRequestTokens += f.AlwaysTokens
		o.PerUseTokens += f.BodyTokens
	}
	return o
}

// ---- catalog -----------------------------------------------------------------

// Catalog is the versioned, verified description of the file formats Trove writes.
type Catalog struct {
	Version       int      `toml:"version"`
	Harness       string   `toml:"harness"`
	CheckedOn     string   `toml:"checked_on"`
	Fields        []Field  `toml:"field"`
	CommandFields []Field  `toml:"command_field"`
	ReadOnly      readOnly `toml:"read_only"`
	ReadOnlyTools []string `toml:"-"`
}

type readOnly struct {
	Tools []string `toml:"tools"`
}

// Field is one frontmatter field.
type Field struct {
	Name     string   `toml:"name"`
	Required bool     `toml:"required"`
	Values   []string `toml:"values"`
	Since    string   `toml:"since"`
	Notes    string   `toml:"notes"`
	Verified string   `toml:"verified"`
}

// CatalogVersion is the schema version of the catalog file.
const CatalogVersion = 1

// Builtin loads and validates the embedded catalog.
func Builtin() (*Catalog, error) {
	b, err := catalogFS.ReadFile("catalog/claude_code.toml")
	if err != nil {
		return nil, err
	}
	var c Catalog
	if err := toml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("tasks catalog: %w", err)
	}
	if c.Version != CatalogVersion {
		return nil, fmt.Errorf("tasks catalog is version %d; this Trove reads version %d. Upgrade Trove", c.Version, CatalogVersion)
	}
	c.ReadOnlyTools = c.ReadOnly.Tools
	for _, bad := range c.ReadOnlyTools {
		if bad == "Bash" || bad == "Edit" || bad == "Write" {
			return nil, fmt.Errorf("tasks catalog lists %s as read-only, which it is not", bad)
		}
	}
	// The same gate as internal/settings: only fields read verbatim are written.
	for _, f := range append(append([]Field{}, c.Fields...), c.CommandFields...) {
		if f.Verified != "verbatim" {
			return nil, fmt.Errorf("tasks catalog field %q is not verified verbatim, so Trove must not write it", f.Name)
		}
	}
	return &c, nil
}

// Field returns the named agent field.
func (c *Catalog) Field(name string) (Field, bool) {
	for _, f := range c.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return Field{}, false
}

// ModelAliases lists the documented model values.
func (c *Catalog) ModelAliases() []string {
	f, _ := c.Field("model")
	return f.Values
}

// ModelOK reports whether v is a documented alias or looks like a full model ID.
func (c *Catalog) ModelOK(v string) bool {
	for _, a := range c.ModelAliases() {
		if a == v {
			return true
		}
	}
	return fullModelID.MatchString(v)
}
