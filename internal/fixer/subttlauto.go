package fixer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/logs"
	"github.com/usetrove/trove/internal/logs/cchistory"
	"github.com/usetrove/trove/internal/logs/cxhistory"
	"github.com/usetrove/trove/internal/setup"
)

// The subagent cache lifetime as Autopilot sets it (docs/autopilot.md, "Settings policy"). These are the
// file-level steps only; the decision is internal/autopilot's.
//
// Precedence, read on code.claude.com/docs/en/prompt-caching on 2026-11-24 (docs/techniques.md, S10): Claude Code takes the first
// match of FORCE_PROMPT_CACHING_5M=1, then CLAUDE_CODE_SUBAGENT_PROMPT_CACHE_TTL, then
// subagentPromptCacheTtl, then a subagent's `experimental` frontmatter `cacheTtl` (v2.1.248 or later), then
// ENABLE_PROMPT_CACHING_1H=1, then the default. The setting therefore wins over a subagent's own frontmatter,
// so a frontmatter value the user wrote counts as their choice too.

// SubTTLKey is the setting Autopilot writes.
const SubTTLKey = "subagentPromptCacheTtl"

// subTTLEnv are the variables that decide the subagent lifetime before, or instead of, the setting.
var subTTLEnv = []string{"FORCE_PROMPT_CACHING_5M", "CLAUDE_CODE_SUBAGENT_PROMPT_CACHE_TTL", "ENABLE_PROMPT_CACHING_1H",
	"DISABLE_PROMPT_CACHING", "DISABLE_PROMPT_CACHING_HAIKU", "DISABLE_PROMPT_CACHING_SONNET", "DISABLE_PROMPT_CACHING_OPUS", "DISABLE_PROMPT_CACHING_FABLE"}

// SubTTLUserChoice says why Autopilot must leave the subagent lifetime alone: the user chose it somewhere
// Trove can see. ownFixID is the fix Autopilot itself applied ("" if none); its value is not the user's.
// It returns "" when nothing the user set decides the lifetime. Values are read in memory, never stored.
// The text is stored as a reason in autopilot.json, so it names variables and kinds of file, never a path
// or a file name.
func SubTTLUserChoice(ctx context.Context, e *Env, ownFixID string) string {
	for _, k := range subTTLEnv {
		if strings.TrimSpace(e.getenv(k)) != "" {
			return "you set " + k + " in your environment"
		}
	}
	userFile := ""
	if e.ClaudeDir != "" {
		userFile = filepath.Join(e.ClaudeDir, "settings.json")
	}
	kind := func(p string) string {
		switch {
		case p == userFile:
			return "your Claude Code user settings"
		case filepath.Base(p) == "settings.local.json":
			return "this project's local Claude Code settings"
		}
		return "this project's Claude Code settings"
	}
	for _, p := range settingsFiles(e) {
		raw, err := e.readFile(p)
		if err != nil || len(raw) == 0 {
			continue
		}
		var doc map[string]json.RawMessage
		if json.Unmarshal(raw, &doc) != nil {
			return kind(p) + " are not plain JSON, so Trove cannot tell what they set"
		}
		env := map[string]json.RawMessage{}
		if r, ok := doc["env"]; ok {
			_ = json.Unmarshal(r, &env)
		}
		for _, k := range subTTLEnv {
			if _, ok := env[k]; ok {
				return "you set env." + k + " in " + kind(p)
			}
		}
		if _, ok := doc[SubTTLKey]; ok {
			if p == userFile && ownFixID != "" {
				if own, err := FixInPlace(ctx, e, ownFixID); err == nil && own {
					continue // Autopilot's own value
				}
			}
			return "you set " + SubTTLKey + " in " + kind(p)
		}
	}
	if frontmatterTTL(e) != "" {
		return "one of your subagents sets its own cacheTtl, and the setting would override it"
	}
	return ""
}

// frontmatterTTL returns the name of the first subagent file in the user's or project's agents folder whose
// frontmatter has a cacheTtl, or "". Plugin subagents are not read.
func frontmatterTTL(e *Env) string {
	var dirs []string
	if e.ClaudeDir != "" {
		dirs = append(dirs, filepath.Join(e.ClaudeDir, "agents"))
	}
	if e.ProjectDir != "" {
		dirs = append(dirs, filepath.Join(e.ProjectDir, ".claude", "agents"))
	}
	for _, d := range dirs {
		matches, _ := filepath.Glob(filepath.Join(d, "*.md"))
		for _, m := range matches {
			raw, err := e.readFile(m)
			if err == nil && frontmatterHas(raw, "cacheTtl") {
				return filepath.Base(m)
			}
		}
	}
	return ""
}

// frontmatterHas reports whether the YAML frontmatter at the top of a markdown file has key at any depth.
func frontmatterHas(raw []byte, key string) bool {
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	first := true
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if first {
			if strings.TrimSpace(line) != "---" {
				return false
			}
			first = false
			continue
		}
		if strings.TrimSpace(line) == "---" {
			return false
		}
		if strings.HasPrefix(strings.TrimSpace(line), key+":") {
			return true
		}
	}
	return false
}

// FixInPlace reports whether the fix with this id is recorded as applied and not undone. An error means
// Trove cannot tell, which a caller must never read as "the user undid it".
func FixInPlace(ctx context.Context, e *Env, fixID string) (bool, error) {
	if e.DB == nil {
		return false, fmt.Errorf("no database")
	}
	var n int
	if err := e.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM fixes WHERE fix_id = ? AND reverted_at IS NULL`, fixID).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// OtherSessionActive reports whether an agent session other than the one ending (sessionID, the raw id
// from the hook, held in memory only) wrote its transcript within the fixer's activity window. A settings
// change is made only when none did, so it reaches no running session (PRINCIPLES.md rule 5). The docs
// do not say whether Claude Code re-reads the cache lifetime settings mid-session (checked 2026-11-24).
func OtherSessionActive(ctx context.Context, e *Env, sessionID string) (bool, string) {
	window := time.Duration(e.Cfg.ActivityMinutes) * time.Minute
	if window <= 0 {
		window = 30 * time.Minute
	}
	home := func() (string, error) { return e.Home, nil }
	for _, s := range []logs.Source{&cchistory.Source{Getenv: e.Getenv, Home: home}, &cxhistory.Source{Getenv: e.Getenv, Home: home}} {
		files, _ := s.Discover(ctx, nil)
		for _, f := range files {
			if sessionID != "" && strings.Contains(filepath.ToSlash(f.Path), sessionID) {
				continue // the session that is ending, and its subagents
			}
			if ago := e.now().Sub(time.UnixMilli(f.MTime)); ago < window {
				return true, fmt.Sprintf("another agent session was active %d minute(s) ago", int(ago.Minutes()))
			}
		}
	}
	return false, ""
}

// subTTLFixID is Autopilot's own id for the change, distinct from the one `trove fix` gives, so a fix the
// user applied by hand is never taken for Autopilot's.
func subTTLFixID(path, value string) string {
	return fixID(CacheTTLSubagents, path, value, "autopilot")
}

// SetSubagentTTL writes subagentPromptCacheTtl = value into the user's Claude Code settings as a
// cache_ttl_subagents fix (backed up, recorded, reversible with `trove undo`) and returns its fix id.
// The caller has already checked SubTTLUserChoice and OtherSessionActive.
func SetSubagentTTL(_ context.Context, e *Env, value, why string, usdPerSession *float64) (string, error) {
	if e.ClaudeDir == "" {
		return "", fmt.Errorf("cannot find the Claude Code settings folder")
	}
	path := filepath.Join(e.ClaudeDir, "settings.json")
	old, err := os.ReadFile(path) //nolint:gosec // an allowed agent config file
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	upd, err := SetJSONString(old, SubTTLKey, value)
	if err != nil {
		return "", fmt.Errorf("%s cannot be edited safely: %v", displayName(e, path), err)
	}
	f := &Fix{ID: subTTLFixID(path, value), Kind: CacheTTLSubagents, Agent: "claude_code", Path: path, Suggestion: true,
		Title: fmt.Sprintf("Autopilot: %s = %q", SubTTLKey, value), Why: why, USDPerSession: usdPerSession, old: old, updated: upd}
	edit := setup.NewFixEdit(f.Kind, f.ID, f.Path, UnifiedDiff(displayName(e, path), old, upd), f.Title, old, upd)
	changes, err := setup.Apply(e.DataDir, []*setup.Edit{edit}, e.now())
	if err != nil {
		return "", err
	}
	if len(changes) == 1 {
		if err := register(e, f, changes[0]); err != nil {
			return f.ID, fmt.Errorf("the setting was written but not recorded for measurement: %v", err)
		}
	}
	return f.ID, nil
}

// RevertSubagentTTL undoes Autopilot's own change. When the file was edited since (Claude Code itself
// writes to it), the byte-exact undo leaves it alone; then only the key is deleted, as a new recorded
// change, and only while it still holds the value Autopilot wrote.
func RevertSubagentTTL(_ context.Context, e *Env, fixID, value string) error {
	res, err := setup.UndoSelected(e.DataDir, []string{fixID})
	if err != nil {
		return err
	}
	done := false
	for _, r := range res {
		if r.FixID == fixID && r.Action != "left_alone" {
			done = true
		}
	}
	if done {
		return MarkReverted(e.DataDir, res, e.now())
	}
	path := filepath.Join(e.ClaudeDir, "settings.json")
	old, err := os.ReadFile(path) //nolint:gosec // an allowed agent config file
	if err != nil {
		return err
	}
	var doc map[string]json.RawMessage
	var cur string
	if json.Unmarshal(old, &doc) != nil || json.Unmarshal(doc[SubTTLKey], &cur) != nil || cur != value {
		// Someone changed the value since: it is theirs now. Stop counting Autopilot's.
		return MarkReverted(e.DataDir, []setup.UndoResult{{FixID: fixID, Action: "already_gone"}}, e.now())
	}
	upd, err := DeleteJSONKey(old, SubTTLKey)
	if err != nil {
		return fmt.Errorf("%s cannot be edited safely: %v. Remove %s there yourself", displayName(e, path), err, SubTTLKey)
	}
	id := fixID + "-revert"
	edit := setup.NewFixEdit(SettingsCheck, id, path, UnifiedDiff(displayName(e, path), old, upd), "Autopilot: remove "+SubTTLKey, old, upd)
	if _, err := setup.Apply(e.DataDir, []*setup.Edit{edit}, e.now()); err != nil {
		return err
	}
	return MarkReverted(e.DataDir, []setup.UndoResult{{FixID: fixID, Action: "removed_our_lines"}}, e.now())
}
