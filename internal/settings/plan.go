package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Inputs is everything Plan needs. Nothing here is written; Plan only reads.
type Inputs struct {
	Task          string
	AllowUnproven bool
	ProjectDir    string // required for Claude Code: its settings are written per project
	ClaudeDir     string
	CodexDir      string
	GOOS          string
	Getenv        func(string) string
	ReadFile      func(string) ([]byte, error)
	ReadDir       func(string) ([]os.DirEntry, error)
	// Version returns the installed harness version text ("" if unknown).
	Version func(harness string) string
	// ManagedClaudeDir and ManagedCodexDir override the OS defaults (tests).
	ManagedClaudeDir string
	ManagedCodexDir  string
}

// Change is one value Trove would write.
type Change struct {
	Value
	Target Target `json:"target"`
	Risk   Risk   `json:"risk"`
}

// BlockedChange is a value Trove would write but cannot, with the reason.
type BlockedChange struct {
	Change
	Reason string `json:"reason"`
}

// HarnessPlan is the plan for one harness.
type HarnessPlan struct {
	Harness     string          `json:"harness"`
	Version     string          `json:"version,omitempty"`
	Path        string          `json:"path,omitempty"` // the one file Trove would write
	Unavailable string          `json:"unavailable,omitempty"`
	Changes     []Change        `json:"changes,omitempty"`
	Blocked     []BlockedChange `json:"blocked,omitempty"`
	Skipped     []SkippedValue  `json:"skipped,omitempty"`
	Notes       []string        `json:"notes,omitempty"`
	Commands    []string        `json:"commands,omitempty"`
}

// Plan is the plan for a task across harnesses.
type Plan struct {
	Task        string         `json:"task"`
	Description string         `json:"description"`
	Harnesses   []*HarnessPlan `json:"harnesses"`
}

func (in *Inputs) defaults() {
	if in.GOOS == "" {
		in.GOOS = goos()
	}
	if in.Getenv == nil {
		in.Getenv = os.Getenv
	}
	if in.ReadFile == nil {
		in.ReadFile = os.ReadFile
	}
	if in.ReadDir == nil {
		in.ReadDir = os.ReadDir
	}
	if in.Version == nil {
		in.Version = func(string) string { return "" }
	}
	if in.ManagedClaudeDir == "" {
		in.ManagedClaudeDir = defaultManagedClaude(in.GOOS)
	}
	if in.ManagedCodexDir == "" {
		in.ManagedCodexDir = defaultManagedCodex(in.GOOS, in.CodexDir)
	}
}

// defaultManagedClaude is where Claude Code reads file-based managed settings
// (code.claude.com/docs/en/managed-settings, read 2026-11-05).
func defaultManagedClaude(os string) string {
	switch os {
	case "darwin":
		return "/Library/Application Support/ClaudeCode"
	case "windows":
		return `C:\Program Files\ClaudeCode`
	}
	return "/etc/claude-code"
}

// defaultManagedCodex is where Codex reads managed_config.toml
// (learn.chatgpt.com/docs/enterprise/managed-configuration, read 2026-11-05;
// that page was only partly available as raw text).
func defaultManagedCodex(os, codexDir string) string {
	if os == "windows" {
		return codexDir
	}
	return "/etc/codex"
}

// ProfileFileName is the Codex profile file Trove writes for a task, and the
// name to pass to `codex --profile`.
func ProfileName(task string) string { return "trove-" + task }

// BuildPlan plans one task. It never writes and never fails because a harness
// is missing: that harness is simply marked unavailable.
func BuildPlan(c *Catalog, ps *ProfileSet, in Inputs) (*Plan, error) {
	in.defaults()
	pr, ok := ps.Get(in.Task)
	if !ok {
		return nil, fmt.Errorf("%q is not a task type. Use one of: %s", in.Task, strings.Join(ps.Tasks(), ", "))
	}
	p := &Plan{Task: pr.Task, Description: pr.Description}
	for _, h := range []string{ClaudeCode, Codex} {
		hp, err := planHarness(c, pr, h, in)
		if err != nil {
			return nil, err
		}
		p.Harnesses = append(p.Harnesses, hp)
	}
	return p, nil
}

func planHarness(c *Catalog, pr Profile, h string, in Inputs) (*HarnessPlan, error) {
	hp := &HarnessPlan{Harness: h, Version: strings.TrimSpace(in.Version(h))}
	file, ok := c.File(h)
	if !ok {
		hp.Unavailable = "no settings catalog for " + h
		return hp, nil
	}
	switch h {
	case ClaudeCode:
		if in.ProjectDir == "" {
			hp.Unavailable = "Claude Code settings are written per project; run this from the project, or pass --project"
			return hp, nil
		}
		hp.Path = filepath.Join(in.ProjectDir, ".claude", "settings.json")
	case Codex:
		if in.CodexDir == "" {
			hp.Unavailable = "the Codex folder was not found"
			return hp, nil
		}
		hp.Path = filepath.Join(in.CodexDir, ProfileName(pr.Task)+".config.toml")
	}

	resolved := map[string]Resolved{}
	for _, r := range file.Resolve(hp.Version) {
		resolved[r.Key] = r
	}
	sel := pr.Select(c, h, in.AllowUnproven)
	hp.Skipped = append(hp.Skipped, sel.Skipped...)
	if hp.Version == "" {
		hp.Notes = append(hp.Notes, h+" was not found or its version could not be read, so Trove cannot tell which settings are valid. Nothing is planned until it can.")
	}

	var managed managedView
	switch h {
	case ClaudeCode:
		managed = claudeManaged(in, hp)
	case Codex:
		managed = codexManaged(in, hp)
	}
	higher := higherLayer(h, in)

	for _, v := range sel.Use {
		s, _ := c.Lookup(h, v.Key)
		r := resolved[v.Key]
		ch := Change{Value: v, Target: s.Target, Risk: s.Risk}
		switch {
		case !r.Usable():
			reason := r.Reason
			if reason == "" {
				reason = "Trove may not write this setting"
			}
			hp.Skipped = append(hp.Skipped, SkippedValue{v, reason})
		case managed.sets(s):
			hp.Blocked = append(hp.Blocked, BlockedChange{ch, "your organization's managed settings (" + managed.where(s) + ") already set this, and managed settings cannot be overridden. Ask your administrator if you want it changed."})
		case higher.sets(s):
			hp.Blocked = append(hp.Blocked, BlockedChange{ch, higher.where(s) + " sets this and takes priority over the file Trove writes."})
		default:
			if s.Target == TargetSettingsEnv && in.Getenv(s.Key) != "" {
				hp.Notes = append(hp.Notes, s.Key+" is also set in your shell. The docs do not say which one wins, so this change may not take effect until you unset the shell variable.")
			}
			hp.Changes = append(hp.Changes, ch)
		}
	}
	if h == Codex && len(hp.Changes) > 0 {
		hp.Commands = append(hp.Commands, "codex --profile "+ProfileName(pr.Task))
		hp.Notes = append(hp.Notes, "Codex profiles only apply when you start it with --profile; your default Codex settings are not changed.")
	}
	if h == ClaudeCode && len(hp.Changes) > 0 {
		hp.Notes = append(hp.Notes, "Claude Code reads these at the start of a session; open a new session in the project to use them.")
	}
	return hp, nil
}

// layer answers "does this layer already set key?" for the settings Trove plans.
type layer interface {
	sets(Setting) bool
	where(Setting) string
}

type managedView = layer

type fileLayer struct {
	files []layerFile
}

type layerFile struct {
	name string
	json map[string]json.RawMessage // Claude Code
	toml map[string]any             // Codex
}

func (l fileLayer) find(s Setting) string {
	for _, f := range l.files {
		switch s.Target {
		case TargetSettingsJSON:
			if _, ok := f.json[s.Key]; ok {
				return f.name
			}
		case TargetSettingsEnv:
			var env map[string]json.RawMessage
			if json.Unmarshal(f.json["env"], &env) == nil {
				if _, ok := env[s.Key]; ok {
					return f.name
				}
			}
		case TargetCodexProfile:
			if _, ok := f.toml[s.Key]; ok {
				return f.name
			}
		}
	}
	return ""
}

func (l fileLayer) sets(s Setting) bool {
	// Lists merge across layers (the docs say arrays merge), so a list setting
	// in a lower layer does not block adding to it.
	if s.Type == "string_list" {
		return false
	}
	return l.find(s) != ""
}
func (l fileLayer) where(s Setting) string { return l.find(s) }

func claudeManaged(in Inputs, hp *HarnessPlan) layer {
	var l fileLayer
	dir := in.ManagedClaudeDir
	found := false
	paths := []string{filepath.Join(dir, "managed-settings.json")}
	if ents, err := in.ReadDir(filepath.Join(dir, "managed-settings.d")); err == nil {
		var names []string
		for _, e := range ents {
			if strings.HasSuffix(e.Name(), ".json") {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, n := range names {
			paths = append(paths, filepath.Join(dir, "managed-settings.d", n))
		}
	}
	for _, p := range paths {
		b, err := in.ReadFile(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		found = true
		if err != nil {
			hp.Notes = append(hp.Notes, "A managed settings file exists at "+p+" but Trove could not read it, so it cannot tell what it sets. Check /status in Claude Code.")
			continue
		}
		doc := map[string]json.RawMessage{}
		if json.Unmarshal(b, &doc) != nil {
			hp.Notes = append(hp.Notes, "The managed settings file "+p+" is not valid JSON, so Trove cannot tell what it sets.")
			continue
		}
		l.files = append(l.files, layerFile{name: filepath.Base(p), json: doc})
	}
	if !found {
		hp.Notes = append(hp.Notes, "No file-based managed settings were found. Trove cannot see server-managed or MDM/registry policy; if your organization manages Claude Code, check /status for a managed source.")
	}
	return l
}

func codexManaged(in Inputs, hp *HarnessPlan) layer {
	var l fileLayer
	p := filepath.Join(in.ManagedCodexDir, "managed_config.toml")
	b, err := in.ReadFile(p)
	switch {
	case errors.Is(err, os.ErrNotExist):
		hp.Notes = append(hp.Notes, "No managed_config.toml was found. Trove cannot see requirements.toml limits or cloud-managed policy; Codex may still enforce them.")
	case err != nil:
		hp.Notes = append(hp.Notes, "A managed_config.toml exists at "+p+" but Trove could not read it, so it cannot tell what it sets.")
	default:
		var doc map[string]any
		if toml.Unmarshal(b, &doc) != nil {
			hp.Notes = append(hp.Notes, "managed_config.toml is not valid TOML, so Trove cannot tell what it sets.")
		} else {
			l.files = append(l.files, layerFile{name: "managed_config.toml", toml: doc})
		}
	}
	return l
}

// higherLayer finds files that outrank the one Trove writes.
func higherLayer(h string, in Inputs) layer {
	var l fileLayer
	switch h {
	case ClaudeCode:
		// .claude/settings.local.json outranks .claude/settings.json.
		p := filepath.Join(in.ProjectDir, ".claude", "settings.local.json")
		if b, err := in.ReadFile(p); err == nil {
			doc := map[string]json.RawMessage{}
			if json.Unmarshal(b, &doc) == nil {
				l.files = append(l.files, layerFile{name: ".claude/settings.local.json", json: doc})
			}
		}
	case Codex:
		// A trusted project's .codex/config.toml outranks a profile file
		// (learn.chatgpt.com config-advanced: profile is below project config).
		if in.ProjectDir != "" {
			p := filepath.Join(in.ProjectDir, ".codex", "config.toml")
			if b, err := in.ReadFile(p); err == nil {
				var doc map[string]any
				if toml.Unmarshal(b, &doc) == nil {
					l.files = append(l.files, layerFile{name: "this project's .codex/config.toml", toml: doc})
				}
			}
		}
	}
	return l
}

// InstalledVersion asks the harness for its version with a short timeout. It
// runs a local program with fixed arguments and no shell; it makes no network
// call of its own. Returns "" if the program is missing or prints no version.
func InstalledVersion(ctx context.Context, harness string, run func(ctx context.Context, name string, args ...string) ([]byte, error)) string {
	name := map[string]string{ClaudeCode: "claude", Codex: "codex"}[harness]
	if name == "" {
		return ""
	}
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).Output() //nolint:gosec // fixed program names and arguments
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := run(ctx, name, "--version")
	if err != nil {
		return ""
	}
	if _, err := ParseVersion(string(out)); err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
