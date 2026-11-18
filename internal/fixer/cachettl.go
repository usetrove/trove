package fixer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/optimize/suggest"
)

// Cache lifetime fixes: the cache_ttl suggestion (internal/optimize/suggest,
// cachettl.go) turned into an edit of ~/.claude/settings.json. One kind per
// setting, so measurement knows which history rows to look at without storing
// a value.
const (
	CacheTTLMain      = "cache_ttl_main"
	CacheTTLSubagents = "cache_ttl_subagents"
)

// The settings and their values were read on code.claude.com/docs/en/settings-reference
// on 2026-11-13 (docs/techniques.md, "What was verified"): `promptCacheTtl` for the
// main conversation and `subagentPromptCacheTtl` for subagents and Claude Code's
// helper requests, each "5m" or "1h", from v2.1.242. The thresholds are the
// suggestion's own (docs/optimize.md): at least 25% cheaper, at least $1 and at
// least 100 requests.
//
// It is a suggestion, never part of --all: the saving rests on the person
// pausing the way they did in the lookback window.
func detectCacheTTL(ctx context.Context, e *Env) ([]*Fix, error) {
	if e.DB == nil || e.Pricing == nil || e.ClaudeDir == "" {
		return nil, nil
	}
	to := e.now()
	from := to.Add(-time.Duration(e.Cfg.LookbackDays) * 24 * time.Hour)
	advice, err := suggest.CacheTTLAdvice(ctx, e.DB, e.Pricing, from, to)
	if err != nil {
		return nil, err
	}
	settings := filepath.Join(e.ClaudeDir, "settings.json")
	var out []*Fix
	for _, a := range advice {
		old, err := os.ReadFile(settings) //nolint:gosec // an allowed agent config file
		if err != nil {
			old = nil
		}
		kind, who := CacheTTLMain, "your main conversation"
		if a.Subagents {
			kind, who = CacheTTLSubagents, "your subagents and Claude Code's helper requests"
		}
		want := map[string]string{"5m": "5-minute", "1h": "1-hour"}
		predicted := "nearly all cache writes move to the " + want[a.Value] + " lifetime"
		f := &Fix{
			ID: fixID(kind, settings, a.Value), Kind: kind, Agent: "claude_code", Path: settings, Suggestion: true,
			Title: fmt.Sprintf("Use a %s cache lifetime for %s", want[a.Value], who),
			Why: fmt.Sprintf("Across %d requests in %d sessions, %s wrote %.0f%% of its cache at one hour. "+
				"At list prices the %s lifetime would have cost about $%.2f less over the last %d days, counting the pauses of 5 to 60 minutes that only a one-hour entry survives.",
				a.Requests, a.Sessions, who, 100*a.Share1h, want[a.Value], a.NetUSD, e.Cfg.LookbackDays),
			Evidence: []string{
				fmt.Sprintf("%d requests in %d sessions; %.0f%% of written cache tokens at one hour", a.Requests, a.Sessions, 100*a.Share1h),
				"After the change: " + predicted + " (check with `trove report`)",
			},
			Notes: []string{
				fmt.Sprintf("Sets %q to %q. Needs Claude Code 2.1.242 or later; older versions ignore it.", a.Key, a.Value),
				"This is an estimate: it assumes a lost cache entry is rewritten in full and that you keep pausing as you did. Look again after a week. Undo with `trove undo`.",
				"It applies to sessions that start after the change.",
			},
			old: old,
		}
		if a.APIKeyOnly && a.Sessions > 0 {
			usd := a.NetUSD / float64(a.Sessions)
			f.USDPerSession = &usd
		} else {
			f.Notes = append(f.Notes, "Your history does not show that you pay per token, so the dollar figure is a list-price equivalent, not money you would save.")
		}
		if why := ttlSetElsewhere(e, a.Key); why != "" {
			f.Advisory = why
		} else if upd, err := SetJSONString(old, a.Key, a.Value); err != nil {
			f.Advisory = fmt.Sprintf("%s cannot be edited safely (%v). Set %q: %q there yourself.", settings, err, a.Key, a.Value)
		} else {
			f.updated = upd
		}
		out = append(out, f)
	}
	return out, nil
}

// ttlSetElsewhere says why the user setting would not take effect: a project
// setting with the same key wins over it.
func ttlSetElsewhere(e *Env, key string) string {
	if e.ProjectDir == "" {
		return ""
	}
	for _, p := range []string{filepath.Join(e.ProjectDir, ".claude", "settings.local.json"), filepath.Join(e.ProjectDir, ".claude", "settings.json")} {
		b, err := os.ReadFile(p) //nolint:gosec // an allowed agent config file
		if err != nil {
			continue
		}
		doc := map[string]json.RawMessage{}
		if json.Unmarshal(b, &doc) == nil {
			if _, ok := doc[key]; ok {
				return "This project's " + strings.TrimPrefix(filepath.ToSlash(p), filepath.ToSlash(e.ProjectDir)+"/") + " sets " + key + ", which wins over your user settings. Change it there if you want."
			}
		}
	}
	return ""
}

// ttlKindOfKey maps a lifetime setting to its fix kind ("" for other keys).
func ttlKindOfKey(key string) string {
	switch key {
	case "promptCacheTtl":
		return CacheTTLMain
	case "subagentPromptCacheTtl":
		return CacheTTLSubagents
	}
	return ""
}

// ttlFixInPlace reports whether a cache lifetime fix of this kind is applied and
// not undone, so settings_check does not offer to delete what it set.
func (e *Env) ttlFixInPlace(ctx context.Context, kind string) bool {
	if e.DB == nil {
		return false
	}
	var n int
	if err := e.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM fixes WHERE kind = ? AND reverted_at IS NULL`, kind).Scan(&n); err != nil {
		return false
	}
	return n > 0
}
