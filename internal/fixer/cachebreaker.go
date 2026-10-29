package fixer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/optimize"
)

// cache_breaker_fix: lines in context files you control that state something
// that changes between sessions (today's date, a generated-at stamp). Context
// files sit at the start of the prompt, so text that differs from one session to
// the next changes everything cached after it.
//
// A line counts only if the existing volatile-line classifier (the one the
// volatile_prefix_mover optimizer uses) matches it AND it says it holds the
// current date or time, or is a generated/updated stamp. A date that merely
// appears in prose ("decided on 2026-03-01") never changes between sessions and
// is left alone.
var stampWords = regexp.MustCompile(`(?i)\b(last updated|updated at|generated (at|on)|as of|timestamp|current session|session id|build id|now:)\b`)

func isCacheBreaker(line string) bool {
	return optimize.IsVolatileLine(line) && (optimize.HasVolatilePhrase(line) || stampWords.MatchString(line))
}

func detectCacheBreaker(ctx context.Context, e *Env) ([]*Fix, error) {
	var paths []string
	paths = append(paths, contextCandidates(e)...)
	if e.ProjectDir != "" {
		paths = append(paths, filepath.Join(e.ProjectDir, "CLAUDE.local.md"), filepath.Join(e.ProjectDir, "AGENTS.override.md"))
	}
	if e.ClaudeDir != "" {
		paths = append(paths, filepath.Join(e.ClaudeDir, "CLAUDE.md"))
	}
	if e.CodexDir != "" {
		paths = append(paths, filepath.Join(e.CodexDir, "AGENTS.md"))
	}
	var out []*Fix
	for _, path := range paths {
		b, err := os.ReadFile(path) //nolint:gosec // an allowed agent config file
		if err != nil {
			continue
		}
		ls := Lines(b)
		var drop []LineRange
		var evidence []string
		var removed int
		fence := false
		for i, l := range ls {
			t := strings.TrimSpace(l)
			if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
				fence = !fence
				continue
			}
			if fence || !isCacheBreaker(strings.TrimRight(l, "\r\n")) {
				continue
			}
			drop = append(drop, LineRange{i, i + 1})
			removed += len(l)
			evidence = append(evidence, fmt.Sprintf("line %d: %s", i+1, truncate(t, 100)))
		}
		if len(drop) == 0 {
			continue
		}
		// Removing a line from between two blank lines would leave a double gap;
		// take one of the blanks with it.
		isBlank := func(i int) bool { return i < 0 || i < len(ls) && strings.TrimSpace(ls[i]) == "" }
		dropped := map[int]bool{}
		for _, r := range drop {
			dropped[r.Start] = true
		}
		for _, r := range drop {
			if i := r.Start; (isBlank(i-1) || dropped[i-1]) && i+1 < len(ls) && isBlank(i+1) && !dropped[i+1] {
				removed += len(ls[i+1])
				drop = append(drop, LineRange{i + 1, i + 2})
				dropped[i+1] = true
			}
		}
		agent := "claude_code"
		if strings.EqualFold(filepath.Base(path), "AGENTS.md") || strings.EqualFold(filepath.Base(path), "AGENTS.override.md") {
			agent = "codex"
		}
		f := &Fix{
			ID: fixID(CacheBreaker, path), Kind: CacheBreaker, Agent: agent, Path: path,
			Title: fmt.Sprintf("Remove %d changing line(s) from %s", len(drop), filepath.Base(path)),
			Why: "These lines state something that changes (a date or a timestamp) near the start of the prompt. " +
				"When it differs from one session to the next, the cached prompt prefix from the last session cannot be reused after that point. " +
				"Your agent can ask for the date or time when it needs it.",
			Evidence: evidence,
			Notes: []string{
				"If a script or hook rewrites these lines each session, remove it there too, or the line will come back.",
				"If the text is a fixed date that never changes, removing it saves only its own few tokens; check before approving.",
				"The saving counted here is only the lines themselves. What the cache would save is not estimated.",
			},
			old: b, updated: DeleteLines(b, drop),
		}
		per := estTokens(removed)
		f.TokensPerSession, f.USDPerSession = e.schemaTokens(ctx).priceTokens(e, per)
		if n, usd := e.volatileMisses(ctx); n > 0 {
			ev := fmt.Sprintf("your gateway recorded %d cache miss(es) attributed to changing prompt content in the last %d days", n, e.Cfg.LookbackDays)
			if usd > 0 {
				ev += fmt.Sprintf(" (estimated extra cost $%.2f in total)", usd)
			}
			f.Evidence = append(f.Evidence, ev)
		}
		out = append(out, f)
	}
	return out, nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

// volatileMisses counts the gateway's cache misses whose reason is volatile
// content, and their estimated extra cost, over the lookback window.
func (e *Env) volatileMisses(ctx context.Context) (int, float64) {
	if e.DB == nil {
		return 0, 0
	}
	since := e.now().Add(-time.Duration(e.Cfg.LookbackDays) * 24 * time.Hour).UnixMilli()
	rows, err := e.DB.QueryContext(ctx, `SELECT findings FROM requests WHERE ts >= ? AND findings LIKE '%volatile_content%'`, since)
	if err != nil {
		return 0, 0
	}
	defer func() { _ = rows.Close() }()
	var n int
	var usd float64
	for rows.Next() {
		var raw string
		if rows.Scan(&raw) != nil {
			continue
		}
		var fs []struct {
			Detector string         `json:"detector"`
			USD      *float64       `json:"usd"`
			Detail   map[string]any `json:"detail"`
		}
		if jsonUnmarshal(raw, &fs) != nil {
			continue
		}
		for _, f := range fs {
			if f.Detector == "cache_miss" && f.Detail["reason"] == "volatile_content" {
				n++
				if f.USD != nil {
					usd += *f.USD
				}
			}
		}
	}
	return n, usd
}
