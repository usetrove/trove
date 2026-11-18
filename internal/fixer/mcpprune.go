package fixer

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
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
		if per, ok := schema.server(name); ok {
			schema.setResent(f, per, "measured", "tool definitions")
			f.TokensPerSession, f.USDPerSession = schema.priceTokens(e, per)
		} else {
			schema.setRequests(f)
			f.Notes = append(f.Notes, fmt.Sprintf(noGatewayNote, "this server's tool definitions"))
		}
		out = append(out, f)
	}
	return out, nil
}

// ---- schema weight --------------------------------------------------------

// schemaStats is what Trove measured about fixed overhead: the tokens each MCP
// server's tool definitions add to a request, which the analyzer apportions from
// the provider-reported prompt total of each gateway request (calibrated
// estimates only), and the average number of requests per session. Without
// gateway data there is no honest per-server figure, so there is none.
type schemaStats struct {
	perRequest  map[string]float64 // normalized server -> mean tokens per request
	mcpTotal    float64            // mean tokens of all MCP tool definitions per request
	gatewayReqs int                // calibrated gateway requests the figures come from
	reqsPerSess float64
	reqsSource  string
	provider    string
	model       string
}

func (e *Env) schemaTokens(ctx context.Context) *schemaStats {
	s := &schemaStats{perRequest: map[string]float64{}}
	if r := e.projectUsage(ctx).requestsPerSession(); r > 0 {
		s.reqsPerSess, s.reqsSource = r, "this project's Claude Code history"
	}
	if e.DB == nil {
		return s
	}
	since := e.now().Add(-time.Duration(e.Cfg.LookbackDays) * 24 * time.Hour).UnixMilli()
	rows, err := e.DB.QueryContext(ctx, `SELECT provider, model, segment_token_estimates FROM requests
		WHERE ts >= ? AND segment_token_estimates LIKE '%"tool_servers"%' ORDER BY ts DESC LIMIT 500`, since)
	if err == nil {
		sum := map[string]float64{}
		var total float64
		for rows.Next() {
			var provider, model, raw string
			if rows.Scan(&provider, &model, &raw) != nil {
				continue
			}
			var est struct {
				Calibrated  bool             `json:"calibrated"`
				ToolServers map[string]int64 `json:"tool_servers"`
			}
			if json.Unmarshal([]byte(raw), &est) != nil || !est.Calibrated || len(est.ToolServers) == 0 {
				continue // uncalibrated figures are not backed by a provider total
			}
			s.gatewayReqs++
			for sv, t := range est.ToolServers {
				sum[normalizeServer(sv)] += float64(t)
				total += float64(t)
			}
			if s.model == "" {
				s.provider, s.model = provider, model
			}
		}
		_ = rows.Close()
		if s.gatewayReqs > 0 {
			for sv, t := range sum {
				s.perRequest[sv] = t / float64(s.gatewayReqs)
			}
			s.mcpTotal = total / float64(s.gatewayReqs)
		}
	}
	if s.model == "" {
		_ = e.DB.QueryRowContext(ctx, `SELECT provider, model FROM requests WHERE ts >= ? AND model != '' ORDER BY ts DESC LIMIT 1`, since).Scan(&s.provider, &s.model)
	}
	if s.reqsPerSess == 0 {
		var avg float64
		if e.DB.QueryRowContext(ctx, `SELECT COALESCE(AVG(request_count), 0) FROM sessions WHERE started_at >= ? AND request_count > 1`, since).Scan(&avg) == nil && avg > 0 {
			s.reqsPerSess, s.reqsSource = avg, "your gateway sessions"
		}
	}
	return s
}

const noGatewayNote = "Trove has no gateway data on %s yet, so it cannot say how many tokens it re-sends on every request. Run your agent through `trove gateway` for a few sessions to measure it."

// server returns the measured tokens a server's tool definitions add to every
// request, or false when the gateway has not seen them.
func (s *schemaStats) server(name string) (int64, bool) {
	per, ok := s.perRequest[normalizeServer(name)]
	if !ok || per < 0.5 {
		return 0, false
	}
	return int64(per + 0.5), true
}

// setResent records on f the tokens it stops re-sending with every request, with
// their label, and the measured requests per session, and states both as
// evidence. It sets no saving: callers decide whether a saving is honest.
func (s *schemaStats) setResent(f *Fix, perRequest int64, label, what string) {
	f.ResentTokensPerRequest, f.ResentLabel = perRequest, label
	src := "estimated at about 4 bytes per token"
	if label == "measured" {
		src = fmt.Sprintf("measured: apportioned from the provider-reported prompt totals of %d gateway request(s)", s.gatewayReqs)
	}
	line := fmt.Sprintf("%d tokens of %s re-sent on every request (%s)", perRequest, what, src)
	if s.setRequests(f) {
		line += fmt.Sprintf(", times %.1f requests per session (measured from %s)", f.RequestsPerSession, s.reqsSource)
	}
	f.Evidence = append(f.Evidence, line)
}

// setRequests records the measured requests per session on f, if there is a figure.
func (s *schemaStats) setRequests(f *Fix) bool {
	if s.reqsPerSess <= 0 {
		return false
	}
	f.RequestsPerSession, f.RequestsLabel = math.Round(s.reqsPerSess*10)/10, "measured"
	return true
}

// priceTokens turns tokens removed from every request into tokens and dollars
// per session. It uses the measured average requests per session (counting one
// request if there is no such data) and prices the first request as a cache
// write and the rest as cache reads. Dollars are nil unless the model is known
// and priced and there are at least two requests per session.
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
