package fixer

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/mcp"
	"github.com/usetrove/trove/internal/mcp/install"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/store"
)

// MCPSnippet adds the Trove MCP tool-preference snippet (`trove mcp snippet`) to
// the project's CLAUDE.md. It changes what the agent is told, so it is T2: a
// suggestion applied by id only, with explicit consent, never part of --all.
const MCPSnippet = "mcp_snippet"

// snippetRel is the context file the snippet goes into. CLAUDE.md in the project
// root is the project memory file Claude Code loads (docs/fixer.md).
const snippetRel = "CLAUDE.md"

// detectMCPSnippet offers the snippet when Trove's MCP server is registered with
// an agent and the project's CLAUDE.md does not already carry it. Without the
// tools the snippet would be text the agent cannot act on, so nothing is offered.
func detectMCPSnippet(_ context.Context, e *Env) ([]*Fix, error) {
	if e.ProjectDir == "" || e.DataDir == "" || !install.Registered(e.DataDir) {
		return nil, nil
	}
	path := filepath.Join(e.ProjectDir, snippetRel)
	old, err := os.ReadFile(path) //nolint:gosec // the project's own context file
	switch {
	case errors.Is(err, os.ErrNotExist):
		old = nil
	case err != nil:
		return nil, fmt.Errorf("cannot read %s: %v", snippetRel, err)
	}
	if bytes.Contains(old, []byte(mcp.Snippet)) {
		return nil, nil
	}
	per := int64(mcp.SnippetTokens())
	f := &Fix{
		ID: fixID(MCPSnippet, pathHash(path)), Kind: MCPSnippet, Agent: "claude_code", Path: path, Suggestion: true,
		Title:                    "Tell the agent when to prefer Trove's MCP tools",
		Why:                      "Without a hint, agents tend to stick to their built-in Read, Grep and Bash even when Trove's tools are registered. This adds a few lines to CLAUDE.md saying when to use them.",
		Evidence:                 []string{"Trove's MCP server is registered (`trove mcp status`)", snippetRel + " does not carry the snippet yet"},
		OverheadTokensPerRequest: per,
		OverheadLabel:            "estimated",
		Notes: []string{
			fmt.Sprintf("Overhead: about %d tokens (estimated) added to every request, because CLAUDE.md is sent with each one; no saving is claimed for it.", per),
			"A behavioural (T2) change: it alters what the agent is told, so it is applied only by id with your consent, never with --all.",
			"After it is applied, `trove fix` shows the share of file reads and searches that used Trove's tools before and after it, and its overhead.",
		},
	}
	if e.DB != nil {
		if a, err := measure.AdoptionFor(context.Background(), &store.DB{DB: e.DB}, lookback(e), config.Default().Measure); err == nil && a.Rate != nil {
			f.Evidence = append(f.Evidence, fmt.Sprintf("Trove tools used for %.0f%% of %d file reads and searches in the last %d days (estimated, Claude Code history, all projects)", a.Rate.Value, a.Rate.N, e.Cfg.LookbackDays))
		}
	}
	if u := e.projectUsage(context.Background()); u.matched && u.sessions > 0 {
		f.RequestsPerSession, f.RequestsLabel = u.requestsPerSession(), "measured"
	}
	upd := append([]byte{}, old...)
	if len(upd) > 0 {
		if upd[len(upd)-1] != '\n' {
			upd = append(upd, '\n')
		}
		upd = append(upd, '\n')
	}
	f.old, f.updated = old, append(upd, mcp.Snippet+"\n"...)
	return []*Fix{f}, nil
}

func lookback(e *Env) measure.Window {
	to := e.now()
	return measure.Window{From: to.AddDate(0, 0, -e.Cfg.LookbackDays), To: to}
}

// SnippetUse is the measured effect of an applied mcp_snippet fix.
type SnippetUse struct {
	AppliedAt time.Time `json:"applied_at"`
	// Before and After are the share of Claude Code's file reads and searches that
	// used a Trove tool, in the lookback window before the snippet and since. They
	// come from agent session logs (label "estimated"), across all projects,
	// because history rows carry no project. Nil when there were no such calls.
	Before *measure.Figure `json:"adoption_before,omitempty"`
	After  *measure.Figure `json:"adoption_after,omitempty"`
	// TroveCallsAfter is how many Trove tool calls the agent made since.
	TroveCallsAfter int `json:"trove_calls_after"`
	// OverheadTokensPerRequest is the snippet's size in tokens (estimated from bytes).
	OverheadTokensPerRequest int64  `json:"overhead_tokens_per_request"`
	OverheadLabel            string `json:"overhead_label"`
	// RequestsPerSession is the project's measured average; OverheadPerSession is
	// the product (estimated, because the per-request figure is).
	RequestsPerSession float64 `json:"requests_per_session,omitempty"`
	OverheadPerSession int64   `json:"overhead_tokens_per_session,omitempty"`
}

// MCPSnippetUse reads adoption before and after the project's applied snippet,
// and its overhead. It returns nil when no snippet fix is in place or there is
// no database. Only counts are read.
func MCPSnippetUse(ctx context.Context, e *Env) (*SnippetUse, error) {
	if e.DB == nil || e.ProjectDir == "" {
		return nil, nil
	}
	var at sql.NullInt64
	if err := e.DB.QueryRowContext(ctx, `SELECT MAX(applied_at) FROM fixes WHERE kind = ? AND path_hash = ? AND reverted_at IS NULL`,
		MCPSnippet, pathHash(filepath.Join(e.ProjectDir, snippetRel))).Scan(&at); err != nil {
		return nil, err
	}
	if !at.Valid {
		return nil, nil
	}
	applied := time.UnixMilli(at.Int64)
	db := &store.DB{DB: e.DB}
	m := config.Default().Measure
	before, err := measure.AdoptionFor(ctx, db, measure.Window{From: applied.AddDate(0, 0, -e.Cfg.LookbackDays), To: applied}, m)
	if err != nil {
		return nil, err
	}
	after, err := measure.AdoptionFor(ctx, db, measure.Window{From: applied, To: e.now()}, m)
	if err != nil {
		return nil, err
	}
	s := &SnippetUse{
		AppliedAt: applied, Before: before.Rate, After: after.Rate,
		TroveCallsAfter:          after.TroveRead + after.TroveSearch + after.TroveRun + after.TroveFetch,
		OverheadTokensPerRequest: int64(mcp.SnippetTokens()), OverheadLabel: "estimated",
	}
	if u := e.projectUsage(ctx); u.matched && u.sessions > 0 {
		s.RequestsPerSession = u.requestsPerSession()
		s.OverheadPerSession = int64(s.RequestsPerSession*float64(s.OverheadTokensPerRequest) + 0.5)
	}
	return s, nil
}
