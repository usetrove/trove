package fixer

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/logs"
	"github.com/usetrove/trove/internal/logs/cchistory"
)

// usageScan is what a live read of one project's Claude Code history found:
// how many sessions there were and how often each MCP server's tools were
// called. It is held in memory for one run and never stored.
type usageScan struct {
	done     bool
	matched  bool // the project's history folder exists
	sessions int
	requests int            // model requests in those sessions, subagents included
	servers  map[string]int // normalized server name -> calls
	// commands groups Bash commands by commandKey. The text is kept here, in
	// memory, only so a retyped command can be offered; it is never stored.
	commands map[string]*retyped
}

// requestsPerSession is the measured average number of requests per session in
// the project's history, or 0 when there is no history.
func (u *usageScan) requestsPerSession() float64 {
	if u.sessions == 0 {
		return 0
	}
	return float64(u.requests) / float64(u.sessions)
}

// encodeProject is how Claude Code names a project's folder under
// <config>/projects: every character that is not a letter or digit becomes '-'.
// This matches what the history reader sees on real installs, but Anthropic does
// not document it; when no folder matches, the scan reports no history and no
// fix is proposed (see docs/fixer.md).
func encodeProject(path string) string {
	var b strings.Builder
	for _, r := range path {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// normalizeServer is how Claude Code writes a server's name inside a tool name
// (mcp__<server>__<tool>): characters other than letters, digits, '_' and '-'
// become '_'.
func normalizeServer(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// serverOfTool returns the MCP server in a tool name like mcp__github__create_issue.
func serverOfTool(tool string) (string, bool) {
	rest, ok := strings.CutPrefix(tool, "mcp__")
	if !ok {
		return "", false
	}
	server, _, ok := strings.Cut(rest, "__")
	return server, ok && server != ""
}

// projectUsage reads the project's Claude Code history within the lookback
// window. Tool names, session ids and Bash command text are looked at, in
// memory; nothing is kept.
func (e *Env) projectUsage(ctx context.Context) *usageScan {
	if e.usage != nil {
		return e.usage
	}
	u := &usageScan{done: true, servers: map[string]int{}, commands: map[string]*retyped{}}
	e.usage = u
	if e.ProjectDir == "" || e.ClaudeDir == "" {
		return u
	}
	root := filepath.Join(e.ClaudeDir, "projects", encodeProject(e.ProjectDir))
	src := &cchistory.Source{Getenv: e.Getenv, Home: func() (string, error) { return e.Home, nil }}
	files, _ := src.Discover(ctx, []string{root})
	if len(files) == 0 {
		return u
	}
	u.matched = true
	since := e.now().Add(-time.Duration(e.Cfg.LookbackDays) * 24 * time.Hour)
	sessions := map[string]bool{}
	for _, f := range files {
		if time.UnixMilli(f.MTime).Before(since) {
			continue
		}
		_, _, _ = src.Parse(ctx, f, logs.Checkpoint{}, func(ev logs.Event) {
			if ev.TS < since.UnixMilli() {
				return
			}
			sessions[ev.SessionKey] = true
			u.requests++
			for _, t := range ev.Tools {
				if s, ok := serverOfTool(t.Name); ok {
					u.servers[s]++
				}
				if t.Command != "" {
					k := commandKey(t.Command)
					r := u.commands[k]
					if r == nil {
						r = &retyped{cmd: t.Command, sessions: map[string]bool{}}
						u.commands[k] = r
					}
					r.count++
					r.sessions[ev.SessionKey] = true
				}
			}
		})
	}
	u.sessions = len(sessions)
	return u
}
