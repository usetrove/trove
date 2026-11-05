package fixer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/usetrove/trove/internal/settings"
)

// SettingsProfile applies a per-task-type profile of a harness's built-in token
// controls (see docs/settings.md). It is a suggestion fix: it changes behavior
// rather than just size, so it is never part of --all, and it only exists when
// the caller asks for a task through Env.Settings.
const SettingsProfile = "settings_profile"

// SettingsRequest asks Detect to plan a settings profile. It holds a task name
// and a switch, nothing else, so it can be stored with a queued fix.
type SettingsRequest struct {
	Task          string `json:"task"`
	AllowUnproven bool   `json:"allow_unproven,omitempty"`
}

// readFile reads through Env.ReadFile when set (tests), else from disk.
func (e *Env) readFile(p string) ([]byte, error) {
	if e.ReadFile != nil {
		return e.ReadFile(p)
	}
	return os.ReadFile(p) //nolint:gosec // an allowed agent config file, or a managed file read-only
}

// PlanSettings plans the settings profile in e.Settings without writing
// anything. The CLI shows it, and detectSettings turns it into fixes.
func PlanSettings(e *Env) (*settings.Plan, *settings.Catalog, error) {
	if e.Settings == nil {
		return nil, nil, errors.New("no settings task was requested")
	}
	cat, err := settings.Builtin()
	if err != nil {
		return nil, nil, fmt.Errorf("the settings catalog could not be loaded: %w", err)
	}
	profiles, err := settings.BuiltinProfiles(cat)
	if err != nil {
		return nil, nil, fmt.Errorf("the settings profiles could not be loaded: %w", err)
	}
	plan, err := settings.BuildPlan(cat, profiles, settings.Inputs{
		Task: e.Settings.Task, AllowUnproven: e.Settings.AllowUnproven,
		ProjectDir: e.ProjectDir, ClaudeDir: e.ClaudeDir, CodexDir: e.CodexDir,
		Getenv: e.Getenv, ReadFile: e.readFile, Version: e.SettingsVersion,
	})
	return plan, cat, err
}

func detectSettings(_ context.Context, e *Env) ([]*Fix, error) {
	if e.Settings == nil {
		return nil, nil
	}
	plan, cat, err := PlanSettings(e)
	if err != nil {
		return nil, err
	}
	var out []*Fix
	for _, hp := range plan.Harnesses {
		if hp.Path == "" || len(hp.Changes)+len(hp.Blocked) == 0 {
			continue
		}
		if f := settingsFix(e, plan, hp, cat); f != nil {
			out = append(out, f)
		}
	}
	return out, nil
}

func settingsFix(e *Env, plan *settings.Plan, hp *settings.HarnessPlan, cat *settings.Catalog) *Fix {
	old, err := e.readFile(hp.Path)
	if err != nil {
		old = nil
	}
	agent := hp.Harness
	f := &Fix{
		ID:   fixID(SettingsProfile, hp.Path, plan.Task, fmt.Sprint(e.Settings.AllowUnproven)),
		Kind: SettingsProfile, Agent: agent, Path: hp.Path, Suggestion: true,
		Task: plan.Task,
		old:  old,
	}
	var keys []string
	for _, c := range hp.Changes {
		keys = append(keys, c.Key)
	}
	f.Keys = keys
	f.Title = fmt.Sprintf("Apply the %q settings profile to %s", plan.Task, agentName(agent))
	f.Why = plan.Description
	for _, c := range hp.Changes {
		f.Evidence = append(f.Evidence, fmt.Sprintf("%s = %s: %s (evidence: %s)", c.Key, valueText(c.Value), c.Why, c.Evidence))
	}
	f.Notes = append(f.Notes,
		"No saving is estimated yet: Trove has no measured result for these values. After you apply them, `trove report` compares your sessions before and after, and labels the result estimated until there are enough sessions to call it measured.",
		"Applied only between sessions, backed up, and reversed with `trove undo`.")
	for _, b := range hp.Blocked {
		f.Notes = append(f.Notes, fmt.Sprintf("Not changed: %s — %s", b.Key, b.Reason))
	}
	for _, s := range hp.Skipped {
		f.Notes = append(f.Notes, fmt.Sprintf("Left out: %s — %s", s.Value.Key, s.Reason))
	}
	f.Notes = append(f.Notes, hp.Notes...)
	for _, c := range hp.Commands {
		f.Notes = append(f.Notes, "Run: "+c)
	}

	if len(hp.Changes) == 0 {
		var why []string
		for _, b := range hp.Blocked {
			why = append(why, b.Key+": "+b.Reason)
		}
		f.Blocked = "nothing can be changed. " + strings.Join(why, " ")
		return f
	}
	upd, err := applySettingsChanges(hp, cat, old)
	if err != nil {
		f.Advisory = fmt.Sprintf("%s cannot be edited safely (%v). Make the change there yourself.", hp.Path, err)
		return f
	}
	if string(upd) == string(old) && old != nil {
		return nil // already in place
	}
	f.updated = upd
	return f
}

func agentName(a string) string {
	if a == settings.Codex {
		return "Codex"
	}
	return "Claude Code"
}

func valueText(c settings.Value) string {
	if len(c.List) > 0 {
		return "[" + strings.Join(c.List, ", ") + "]"
	}
	return c.Value
}

// applySettingsChanges edits old (nil if the file is new) to carry the changes.
func applySettingsChanges(hp *settings.HarnessPlan, cat *settings.Catalog, old []byte) ([]byte, error) {
	cur := old
	if hp.Harness == settings.Codex && len(cur) == 0 {
		cur = []byte("# Written by Trove: a Codex profile. Use it with `codex --profile " + strings.TrimSuffix(baseName(hp.Path), ".config.toml") + "`.\n# Undo with `trove undo`.\n")
	}
	var err error
	for _, c := range hp.Changes {
		s, ok := cat.Lookup(hp.Harness, c.Key)
		if !ok {
			return nil, fmt.Errorf("%s is not in the settings catalog", c.Key)
		}
		switch s.Target {
		case settings.TargetSettingsEnv:
			cur, err = SetJSONEnvString(cur, c.Key, c.Value.Value)
		case settings.TargetSettingsJSON:
			if s.Type == "string_list" {
				for _, el := range c.List {
					if cur, err = AddToJSONStringArray(cur, c.Key, el); err != nil {
						break
					}
				}
			} else {
				cur, err = SetJSONString(cur, c.Key, c.Value.Value)
			}
		case settings.TargetCodexProfile:
			cur, err = SetTOMLTopString(cur, c.Key, c.Value.Value)
		default:
			err = fmt.Errorf("%s cannot be written", c.Key)
		}
		if err != nil {
			return nil, err
		}
	}
	return cur, nil
}

func baseName(p string) string {
	p = strings.ReplaceAll(p, `\`, "/")
	return p[strings.LastIndex(p, "/")+1:]
}
