package fixer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/usetrove/trove/internal/pricing"
)

// mcp_prune: MCP servers listed in the project's .mcp.json that none of the
// project's recent Claude Code sessions called. The proposal turns each off for
// you only, by adding its name to disabledMcpjsonServers in
// .claude/settings.local.json (documented: code.claude.com/docs/en/mcp,
// "Disabling a server"). The shared .mcp.json is never edited, so teammates are
// not affected, and removing the line brings the server back.
//
// Not covered, and why: servers in ~/.claude.json (user and local scope) are not
// read, because that file also holds account and OAuth state that Trove must
// not handle; Codex servers, because the Codex history format is unverified and
// no tool-name convention for MCP calls is documented.
func detectMCPPrune(ctx context.Context, e *Env) ([]*Fix, error) {
	if e.ProjectDir == "" {
		return nil, nil
	}
	mcpPath := filepath.Join(e.ProjectDir, ".mcp.json")
	raw, err := os.ReadFile(mcpPath) //nolint:gosec // an allowed agent config file
	if err != nil {
		return nil, nil // no project MCP file: nothing to prune
	}
	var cfg struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	if json.Unmarshal(raw, &cfg) != nil || len(cfg.Servers) == 0 {
		return nil, nil
	}
	use := e.projectUsage(ctx)
	if !use.matched || use.sessions < e.Cfg.MinSessions {
		return nil, nil // not enough history to say anything is unused
	}

	disabled := map[string]bool{}
	for _, p := range []string{
		filepath.Join(e.ProjectDir, ".claude", "settings.json"),
		filepath.Join(e.ProjectDir, ".claude", "settings.local.json"),
		filepath.Join(e.ClaudeDir, "settings.json"),
	} {
		if b, err := os.ReadFile(p); err == nil { //nolint:gosec // an allowed agent config file
			for _, s := range JSONStringArray(b, "disabledMcpjsonServers") {
				disabled[s] = true
			}
		}
	}

	localPath := filepath.Join(e.ProjectDir, ".claude", "settings.local.json")
	oldLocal, _ := os.ReadFile(localPath) //nolint:gosec // an allowed agent config file
	if _, statErr := os.Stat(localPath); statErr != nil {
		oldLocal = nil
	}

	names := make([]string, 0, len(cfg.Servers))
	for n := range cfg.Servers {
		names = append(names, n)
	}
	sort.Strings(names)

	schema := e.schemaTokens(ctx)
	var out []*Fix
	for _, name := range names {
		if disabled[name] || use.servers[normalizeServer(name)] > 0 {
			continue
		}
		f := &Fix{
			ID: fixID(MCPPrune, localPath, name), Kind: MCPPrune, Agent: "claude_code", Path: localPath,
			Title: fmt.Sprintf("Turn off the %q MCP server in this project", name),
			Why: fmt.Sprintf("%q is configured in .mcp.json, but none of this project's %d sessions in the last %d days called any of its tools. "+
				"Each session still sends its tool definitions.", name, use.sessions, e.Cfg.LookbackDays),
			Evidence: []string{
				fmt.Sprintf("%d recent session(s) in this project read from Claude Code's own history; 0 calls to mcp__%s__*", use.sessions, normalizeServer(name)),
			},
			Notes: []string{
				"Only you are affected: the change goes in .claude/settings.local.json and .mcp.json is not edited. Add that file to .gitignore if it is not ignored already.",
				"Undo with `trove undo`, or delete the name from disabledMcpjsonServers to turn the server back on.",
			},
			old: oldLocal,
		}
		upd, err := AddToJSONStringArray(oldLocal, "disabledMcpjsonServers", name)
		if err != nil {
			f.Advisory = fmt.Sprintf("%s cannot be edited safely (%v). Add %q to \"disabledMcpjsonServers\" there yourself.", filepath.Join(".claude", "settings.local.json"), err, name)
		} else {
			f.updated = upd
		}
		if t, usd, note := schema.cost(e, name); note == "" {
			f.TokensPerSession, f.USDPerSession = t, usd
			f.Evidence = append(f.Evidence, fmt.Sprintf("~%d tokens of tool definitions per session (estimated from your gateway traffic; may be lower if Claude Code defers tool definitions with tool search)", t))
		} else {
			f.Notes = append(f.Notes, note)
		}
		out = append(out, f)
	}
	return out, nil
}

// ---- schema weight --------------------------------------------------------

// schemaStats is Trove's estimate of the prompt tokens each MCP server's tool
// definitions take, taken from the gateway's own unused_tool_schemas findings.
// Without gateway data there is no honest figure, so there is none.
type schemaStats struct {
	perRequest   map[string]float64 // normalized server -> mean tokens per request
	reqsPerSess  float64
	provider     string
	model        string
	haveGateway  bool
	gatewayError string
}

func (e *Env) schemaTokens(ctx context.Context) *schemaStats {
	s := &schemaStats{perRequest: map[string]float64{}}
	if e.DB == nil {
		return s
	}
	since := e.now().Add(-time.Duration(e.Cfg.LookbackDays) * 24 * time.Hour).UnixMilli()
	rows, err := e.DB.QueryContext(ctx, `SELECT provider, model, findings FROM requests
		WHERE ts >= ? AND findings LIKE '%unused_tool_schemas%' ORDER BY ts DESC LIMIT 500`, since)
	if err != nil {
		s.gatewayError = err.Error()
		return s
	}
	defer func() { _ = rows.Close() }()
	sum := map[string]float64{}
	n := map[string]int{}
	for rows.Next() {
		var provider, model, raw string
		if rows.Scan(&provider, &model, &raw) != nil {
			continue
		}
		var fs []struct {
			Detector string         `json:"detector"`
			Tokens   int64          `json:"tokens"`
			Detail   map[string]any `json:"detail"`
		}
		if json.Unmarshal([]byte(raw), &fs) != nil {
			continue
		}
		for _, f := range fs {
			if f.Detector != "unused_tool_schemas" {
				continue
			}
			names, _ := f.Detail["tool_names"].([]any)
			if len(names) == 0 || f.Tokens <= 0 {
				continue
			}
			byServer := map[string]int{}
			for _, nm := range names {
				if sv, ok := serverOfTool(fmt.Sprint(nm)); ok {
					byServer[sv]++
				}
			}
			for sv, c := range byServer {
				sum[sv] += float64(f.Tokens) * float64(c) / float64(len(names))
				n[sv]++
			}
			if s.model == "" {
				s.provider, s.model = provider, model
			}
			s.haveGateway = true
		}
	}
	for sv, t := range sum {
		s.perRequest[sv] = t / float64(n[sv])
	}
	if s.model == "" {
		_ = e.DB.QueryRowContext(ctx, `SELECT provider, model FROM requests WHERE ts >= ? AND model != '' ORDER BY ts DESC LIMIT 1`, since).Scan(&s.provider, &s.model)
	}
	var avg float64
	if e.DB.QueryRowContext(ctx, `SELECT COALESCE(AVG(request_count), 0) FROM sessions WHERE started_at >= ? AND request_count > 1`, since).Scan(&avg) == nil {
		s.reqsPerSess = avg
	}
	return s
}

// cost returns the estimated tokens and dollars per session for a server's tool
// definitions, or a note saying why there is no figure.
func (s *schemaStats) cost(e *Env, server string) (int64, *float64, string) {
	per, ok := s.perRequest[normalizeServer(server)]
	if !ok || s.reqsPerSess < 2 {
		return 0, nil, "Trove has no gateway data on this server's tool definitions yet, so it cannot say how many tokens turning it off would save. Run your agent through `trove gateway` for a few sessions to get an estimate."
	}
	tokens, usd := s.priceTokens(e, int64(per+0.5))
	return tokens, usd, ""
}

// priceTokens turns tokens removed from every request into tokens and dollars
// per session. It uses the average requests per session the gateway recorded
// (counting one request if there is no such data) and prices the first request
// as a cache write and the rest as cache reads. Dollars are nil unless the model
// is known and priced.
func (s *schemaStats) priceTokens(e *Env, perRequest int64) (int64, *float64) {
	reqs := int64(s.reqsPerSess + 0.5)
	if reqs < 1 {
		reqs = 1
	}
	tokens := perRequest * reqs
	if e.Pricing == nil || s.model == "" || s.provider == "" || s.reqsPerSess < 2 {
		return tokens, nil
	}
	c := e.Pricing.Cost(pricing.Request{Provider: s.provider, Model: s.model, Time: e.now(), Usage: pricing.Usage{
		CacheWrite: map[string]int64{"5m": perRequest}, CacheRead: perRequest * (reqs - 1)}})
	if c.Status != pricing.Priced {
		return tokens, nil
	}
	usd := c.Total.Float64()
	return tokens, &usd
}
