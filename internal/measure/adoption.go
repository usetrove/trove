package measure

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/store"
)

// Tool kinds the adoption figures tell apart.
const (
	ToolOther       = ""
	ToolTroveRead   = "trove_read"
	ToolTroveSearch = "trove_search"
	ToolTroveRun    = "trove_run"
	ToolTroveFetch  = "trove_fetch_full"
	ToolBuiltinRead = "builtin_read"
	ToolBuiltinFind = "builtin_find"
	ToolBuiltinBash = "builtin_bash"
)

// ClassifyTool names the kind of a tool call from the tool's name. Claude Code
// shows MCP tools as mcp__<server>__<tool> (verified against
// code.claude.com/docs/en/mcp, 2026-10-29); other agents may add their own
// prefix, so any name that ends in a Trove tool name after a separator and
// whose server is named trove counts. The built-in names Read, Grep, Glob and Bash are
// Claude Code's, verified against code.claude.com/docs/en/tools-reference
// (2026-10-29).
func ClassifyTool(name string) string {
	for _, t := range []string{ToolTroveRead, ToolTroveSearch, ToolTroveRun, ToolTroveFetch} {
		if name == t {
			return t
		}
		for _, sep := range []string{"__", ".", "/", ":"} {
			if prefix, ok := strings.CutSuffix(name, sep+t); ok && serverIsTrove(prefix) {
				return t
			}
		}
	}
	switch name {
	case "Read":
		return ToolBuiltinRead
	case "Grep", "Glob":
		return ToolBuiltinFind
	case "Bash":
		return ToolBuiltinBash
	}
	return ToolOther
}

// serverIsTrove reports whether the part of a tool name before the tool itself
// ends in a server called "trove" (mcp__trove, trove, ...), so another server's
// tool with a similar name is not counted.
func serverIsTrove(prefix string) bool {
	last := prefix
	for _, sep := range []string{"__", ".", "/", ":"} {
		if i := strings.LastIndex(last, sep); i >= 0 {
			last = last[i+len(sep):]
		}
	}
	return last == "trove"
}

// MinFetchBackSample is how many trimmed outputs the fetch-back rate needs
// before it is shown as a number.
const MinFetchBackSample = 20

// Adoption says whether agents use the Trove MCP tools and whether trimming is
// too aggressive. Two sources, never mixed in one number:
//
//   - Use of the tools versus the built-in ones comes from agents' own session
//     logs, so it is estimated. Only Claude Code's built-in tool names are
//     verified, so the comparison is made for Claude Code only; Codex sessions
//     contribute a count of Trove calls.
//   - The fetch-back rate comes from requests the gateway saw (it is the only
//     place trimming happens), so it is measured.
type Adoption struct {
	// Use of the tools in agent session logs (estimated).
	ClaudeSessions int `json:"claude_sessions"`
	TroveRead      int `json:"trove_read"`
	TroveSearch    int `json:"trove_search"`
	TroveRun       int `json:"trove_run"`
	TroveFetch     int `json:"trove_fetch_full"`
	BuiltinRead    int `json:"builtin_read"`
	BuiltinFind    int `json:"builtin_grep_glob"`
	// BuiltinBash is context only: most Bash calls are not test or build runs, so
	// it is not part of the rate.
	BuiltinBash int `json:"builtin_bash"`
	// CodexTroveCalls counts Trove calls in Codex sessions.
	CodexTroveCalls int `json:"codex_trove_calls"`
	// Rate is the share of Claude Code's file reads and searches that used a
	// Trove tool (a percent). Nil when there were no such calls.
	Rate *Figure `json:"adoption_rate,omitempty"`

	// Fetch-back, from gateway requests (measured).
	GatewaySessions int `json:"gateway_sessions"`
	Trimmed         int `json:"trimmed_outputs"`
	Fetches         int `json:"fetches"`
	// FetchBack is fetches per 100 trimmed outputs. Nil until MinFetchBackSample
	// outputs were trimmed.
	FetchBack *Figure `json:"fetch_back_rate,omitempty"`
	// TooAggressive is true when FetchBack reaches the guardrail's re-fetch limit.
	TooAggressive bool `json:"trimming_too_aggressive"`
}

// Any reports whether there is anything to show.
func (a Adoption) Any() bool {
	return a.TroveRead+a.TroveSearch+a.TroveRun+a.TroveFetch+a.CodexTroveCalls+a.Trimmed > 0
}

// AdoptionFor reads tool-call counts from history and gateway signals in w.
// Names and counts only are read; nothing is written.
func AdoptionFor(ctx context.Context, db *store.DB, w Window, m config.Measure) (Adoption, error) {
	var a Adoption
	rows, err := db.QueryContext(ctx, `SELECT source, session_key, tool_names FROM history_requests
		WHERE ts >= ? AND ts < ? AND tool_names IS NOT NULL`, w.From.UnixMilli(), w.To.UnixMilli())
	if err != nil {
		return a, err
	}
	claude := map[string]bool{}
	for rows.Next() {
		var source, key, names string
		if err := rows.Scan(&source, &key, &names); err != nil {
			_ = rows.Close()
			return a, err
		}
		var counts map[string]int
		if json.Unmarshal([]byte(names), &counts) != nil {
			continue
		}
		for n, c := range counts {
			k := ClassifyTool(n)
			if source != "claude_code" {
				if k == ToolTroveRead || k == ToolTroveSearch || k == ToolTroveRun || k == ToolTroveFetch {
					a.CodexTroveCalls += c
				}
				continue
			}
			claude[key] = true
			switch k {
			case ToolTroveRead:
				a.TroveRead += c
			case ToolTroveSearch:
				a.TroveSearch += c
			case ToolTroveRun:
				a.TroveRun += c
			case ToolTroveFetch:
				a.TroveFetch += c
			case ToolBuiltinRead:
				a.BuiltinRead += c
			case ToolBuiltinFind:
				a.BuiltinFind += c
			case ToolBuiltinBash:
				a.BuiltinBash += c
			}
		}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return a, err
	}
	a.ClaudeSessions = len(claude)
	if used, all := a.TroveRead+a.TroveSearch, a.TroveRead+a.TroveSearch+a.BuiltinRead+a.BuiltinFind; all > 0 {
		a.Rate = &Figure{Value: 100 * float64(used) / float64(all), Label: Estimated, N: all}
	}

	// A session's last request carries its whole history, so its signals describe
	// the session.
	srows, err := db.QueryContext(ctx, `SELECT session_key, signals FROM requests
		WHERE ts >= ? AND ts < ? AND signals IS NOT NULL ORDER BY ts, id`, w.From.UnixMilli(), w.To.UnixMilli())
	if err != nil {
		return a, err
	}
	last := map[string]Signals{}
	for srows.Next() {
		var key, js string
		if err := srows.Scan(&key, &js); err != nil {
			_ = srows.Close()
			return a, err
		}
		var s Signals
		if json.Unmarshal([]byte(js), &s) == nil {
			last[key] = s
		}
	}
	_ = srows.Close()
	if err := srows.Err(); err != nil {
		return a, err
	}
	a.GatewaySessions = len(last)
	for _, s := range last {
		a.Trimmed += s.Trimmed
		a.Fetches += s.TroveFetches
	}
	if a.Trimmed >= MinFetchBackSample {
		v := 100 * float64(a.Fetches) / float64(a.Trimmed)
		a.FetchBack = &Figure{Value: v, Label: Measured, N: a.Trimmed}
		a.TooAggressive = v >= float64(m.RefetchRatePercent)
	}
	return a, nil
}
