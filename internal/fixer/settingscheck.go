package fixer

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"time"
)

// settings_check: a documented Claude Code setting that you already have, and
// that raises what your sessions cost. Nothing is guessed about your work: each
// entry is a setting that is present in one of your own settings files, and the
// proposal is to delete it, which puts Claude Code back on its default.
//
// Every one of these is a suggestion, never part of --all, because you may have
// set it on purpose (for example to keep prompts out of a cache). The text says
// so. No saving figure is shown: Trove cannot know how many sessions would be
// affected, and a wrong number is worse than none.
//
// Facts, read from official pages on 2026-11-13 (docs/techniques.md, "What was
// verified"):
//   - DISABLE_PROMPT_CACHING* set to 1 disables prompt caching (code.claude.com
//     /docs/en/env-vars, /prompt-caching). A cache read costs 0.1x the base input
//     price (platform.claude.com/docs/en/build-with-claude/prompt-caching).
//   - ENABLE_TOOL_SEARCH=false loads every MCP tool definition upfront; unset
//     defers them (env-vars).
//   - A one-hour cache write costs 2x the base input price and a five-minute one
//     1.25x (prompt-caching). ENABLE_PROMPT_CACHING_1H, CLAUDE_CODE_PROMPT_CACHE_TTL,
//     CLAUDE_CODE_SUBAGENT_PROMPT_CACHE_TTL, promptCacheTtl and
//     subagentPromptCacheTtl can ask for the one-hour lifetime (env-vars, settings-reference).
//
// Not covered: managed settings (Trove never edits them), and a setting made in
// your shell or by your organization, which Trove cannot see.

type costlySetting struct {
	key    string // short id part
	inEnv  bool   // true: a variable in the "env" object; false: a top-level key
	name   string
	badVal string // the value that is flagged
	title  string
	why    string
	// needs1h means the fix is proposed only if history shows tokens written at
	// the one-hour rate, so Trove never claims a cost it cannot see.
	needs1h bool
}

const (
	noCacheWhy = "This turns prompt caching off. Cache reads cost 0.1x the normal input price (less on some newer models), so without it every request pays the full price for the same prompt again. You may have set it on purpose; if not, deleting it puts Claude Code back on its default."
	ttl1hWhy   = "This asks Claude Code to write cache entries with a one-hour lifetime, which the API bills at 2x the base input price (a five-minute write is 1.25x). It only pays off if you come back to the same session between five minutes and an hour later. Deleting it puts Claude Code back on its default for your login."
)

var costlySettings = []costlySetting{
	{key: "nocache", inEnv: true, name: "DISABLE_PROMPT_CACHING", badVal: "1", title: "Turn prompt caching back on (remove DISABLE_PROMPT_CACHING)", why: noCacheWhy},
	{key: "nocache-haiku", inEnv: true, name: "DISABLE_PROMPT_CACHING_HAIKU", badVal: "1", title: "Turn prompt caching back on for Haiku (remove DISABLE_PROMPT_CACHING_HAIKU)", why: noCacheWhy},
	{key: "nocache-sonnet", inEnv: true, name: "DISABLE_PROMPT_CACHING_SONNET", badVal: "1", title: "Turn prompt caching back on for Sonnet (remove DISABLE_PROMPT_CACHING_SONNET)", why: noCacheWhy},
	{key: "nocache-opus", inEnv: true, name: "DISABLE_PROMPT_CACHING_OPUS", badVal: "1", title: "Turn prompt caching back on for Opus (remove DISABLE_PROMPT_CACHING_OPUS)", why: noCacheWhy},
	{key: "nocache-fable", inEnv: true, name: "DISABLE_PROMPT_CACHING_FABLE", badVal: "1", title: "Turn prompt caching back on for Fable (remove DISABLE_PROMPT_CACHING_FABLE)", why: noCacheWhy},
	{key: "toolsearch", inEnv: true, name: "ENABLE_TOOL_SEARCH", badVal: "false", title: "Let Claude Code defer MCP tool definitions (remove ENABLE_TOOL_SEARCH=false)",
		why: "ENABLE_TOOL_SEARCH=false loads every MCP tool definition into every request. Unset, Claude Code defers them and loads a tool's definition only when it is needed. If you have no MCP servers this changes nothing; if you do, it is on purpose only when a tool must always be present."},
	{key: "ttl1h-env", inEnv: true, name: "ENABLE_PROMPT_CACHING_1H", badVal: "1", title: "Go back to the default cache lifetime (remove ENABLE_PROMPT_CACHING_1H)", why: ttl1hWhy, needs1h: true},
	{key: "ttl1h-main", inEnv: true, name: "CLAUDE_CODE_PROMPT_CACHE_TTL", badVal: "1h", title: "Go back to the default cache lifetime (remove CLAUDE_CODE_PROMPT_CACHE_TTL)", why: ttl1hWhy, needs1h: true},
	{key: "ttl1h-sub", inEnv: true, name: "CLAUDE_CODE_SUBAGENT_PROMPT_CACHE_TTL", badVal: "1h", title: "Go back to the default cache lifetime for subagents (remove CLAUDE_CODE_SUBAGENT_PROMPT_CACHE_TTL)", why: ttl1hWhy, needs1h: true},
	{key: "ttl1h-key", name: "promptCacheTtl", badVal: "1h", title: "Go back to the default cache lifetime (remove promptCacheTtl)", why: ttl1hWhy, needs1h: true},
	{key: "ttl1h-subkey", name: "subagentPromptCacheTtl", badVal: "1h", title: "Go back to the default cache lifetime for subagents (remove subagentPromptCacheTtl)", why: ttl1hWhy, needs1h: true},
}

// settingsFiles are the files this check reads, in the order Claude Code
// prefers them (local, project, user). Managed files are never read here.
func settingsFiles(e *Env) []string {
	var out []string
	if e.ProjectDir != "" {
		out = append(out, filepath.Join(e.ProjectDir, ".claude", "settings.local.json"), filepath.Join(e.ProjectDir, ".claude", "settings.json"))
	}
	if e.ClaudeDir != "" {
		out = append(out, filepath.Join(e.ClaudeDir, "settings.json"))
	}
	return out
}

func detectSettingsCheck(ctx context.Context, e *Env) ([]*Fix, error) {
	var out []*Fix
	var written1h int64
	var haveHistory bool
	for _, p := range settingsFiles(e) {
		raw, err := e.readFile(p)
		if err != nil || len(raw) == 0 {
			continue
		}
		var doc map[string]json.RawMessage
		if json.Unmarshal(raw, &doc) != nil {
			continue // not strict JSON: Trove edits nothing it cannot parse
		}
		env := map[string]json.RawMessage{}
		if r, ok := doc["env"]; ok {
			_ = json.Unmarshal(r, &env)
		}
		for _, c := range costlySettings {
			var val string
			var has bool
			src := doc
			if c.inEnv {
				src = env
			}
			if r, ok := src[c.name]; ok && json.Unmarshal(r, &val) == nil {
				has = true
			}
			if !has || val != c.badVal {
				continue
			}
			if c.needs1h {
				if !haveHistory {
					written1h, haveHistory = e.cacheWrites1h(ctx), true
				}
				if written1h <= 0 {
					continue // nothing shows a one-hour write was paid for
				}
			}
			out = append(out, e.settingsCheckFix(c, p, raw, written1h))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (e *Env) settingsCheckFix(c costlySetting, path string, raw []byte, written1h int64) *Fix {
	where := "env." + c.name
	if !c.inEnv {
		where = c.name
	}
	f := &Fix{
		ID: fixID(SettingsCheck, path, c.name), Kind: SettingsCheck, Agent: "claude_code", Path: path, Suggestion: true,
		Title: c.title, Why: c.why,
		Evidence: []string{fmt.Sprintf("%s is set to %q in %s", where, c.badVal, displayName(e, path))},
		Notes: []string{
			"Deleting the setting is the whole change; nothing else in the file is touched. Undo with `trove undo`.",
			"A setting in a higher-priority file, your shell, or your organization's managed settings can still apply; Trove cannot see those.",
		},
		old: raw,
	}
	if c.needs1h {
		f.Evidence = append(f.Evidence, fmt.Sprintf("%d tokens were written to the cache at the one-hour rate in your Claude Code history over the last %d days", written1h, e.Cfg.LookbackDays))
	}
	if filepath.Base(path) == "settings.json" && filepath.Base(filepath.Dir(path)) == ".claude" && e.ProjectDir != "" && filepath.Dir(filepath.Dir(path)) == filepath.Clean(e.ProjectDir) {
		f.Notes = append(f.Notes, "This file is shared with everyone who uses the project, so the change affects them too.")
	}
	var upd []byte
	var err error
	if c.inEnv {
		upd, err = DeleteJSONEnvKey(raw, c.name)
	} else {
		upd, err = DeleteJSONKey(raw, c.name)
	}
	if err != nil {
		f.Advisory = fmt.Sprintf("%s cannot be edited safely (%v). Remove %s there yourself.", displayName(e, path), err, where)
		return f
	}
	f.updated = upd
	return f
}

// cacheWrites1h is the number of tokens in the Claude Code history that were
// written to the cache at the one-hour rate over the look-back window. 0 when
// there is no history, so the one-hour suggestions are not made without it.
func (e *Env) cacheWrites1h(ctx context.Context) int64 {
	if e.DB == nil {
		return 0
	}
	since := e.now().Add(-time.Duration(e.Cfg.LookbackDays) * 24 * time.Hour).UnixMilli()
	var n int64
	if err := e.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(cache_write_1h), 0) FROM history_requests WHERE ts >= ? AND source = 'claude_code'`, since).Scan(&n); err != nil {
		return 0
	}
	return n
}
