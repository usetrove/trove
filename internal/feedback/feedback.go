// Package feedback builds the diagnostics a beta user can attach to a GitHub
// issue. It holds versions, agent names, counts and error classes only: never
// prompts, code, file paths, tool output or credentials. Nothing here sends
// anything; the CLI shows the text to the user first.
package feedback

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// Repo is the GitHub repository issues are filed in.
const Repo = "usetrove/trove"

// MaxURLLen keeps the prefilled link inside what browsers and GitHub accept.
const MaxURLLen = 7000

// Diagnostics is everything that goes into the issue body.
type Diagnostics struct {
	TroveVersion   string         `json:"trove_version"`
	Commit         string         `json:"commit"`
	GoVersion      string         `json:"go_version"`
	OS             string         `json:"os"`
	Arch           string         `json:"arch"`
	PricingData    string         `json:"pricing_data_version"`
	AgentsDetected []string       `json:"agents_detected"`
	AgentsSeen     []string       `json:"agents_seen_in_traffic"` // "name version"
	Requests       int64          `json:"requests"`
	Sessions       int64          `json:"sessions"`
	FixesApplied   int64          `json:"fixes_applied"`
	FixesReverted  int64          `json:"fixes_reverted"`
	GuardrailTrips int64          `json:"guardrail_trips"`
	ErrorClasses   map[string]int `json:"error_classes"`
}

// Collect reads the counts from the database. A nil db (no data yet) gives zero
// counts. Each query is independent, so one missing table never hides the rest.
func Collect(ctx context.Context, db *sql.DB, d *Diagnostics) {
	d.ErrorClasses = map[string]int{}
	if db == nil {
		return
	}
	count := func(dst *int64, q string) { _ = db.QueryRowContext(ctx, q).Scan(dst) }
	count(&d.Requests, `SELECT COUNT(*) FROM requests`)
	count(&d.Sessions, `SELECT COUNT(*) FROM sessions`)
	count(&d.FixesApplied, `SELECT COUNT(*) FROM fixes`)
	count(&d.FixesReverted, `SELECT COUNT(*) FROM fixes WHERE reverted_at IS NOT NULL`)
	count(&d.GuardrailTrips, `SELECT COUNT(*) FROM guardrail_events`)

	if rows, err := db.QueryContext(ctx, `SELECT error_class, COUNT(*) FROM requests WHERE error_class <> '' GROUP BY error_class`); err == nil {
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var k string
			var n int
			if rows.Scan(&k, &n) == nil {
				d.ErrorClasses[k] = n
			}
		}
	}
	if rows, err := db.QueryContext(ctx, `SELECT DISTINCT agent, agent_version FROM requests WHERE agent <> '' ORDER BY agent, agent_version LIMIT 20`); err == nil {
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var a, v string
			if rows.Scan(&a, &v) == nil {
				d.AgentsSeen = append(d.AgentsSeen, strings.TrimSpace(a+" "+v))
			}
		}
	}
}

// Body renders the issue body. It is deterministic: maps are sorted.
func Body(d Diagnostics, note string) string {
	var b strings.Builder
	if n := strings.TrimSpace(note); n != "" {
		b.WriteString(n + "\n\n")
	} else {
		b.WriteString("<!-- Describe what happened and what you expected. -->\n\n")
	}
	b.WriteString("### Diagnostics (no prompts, code, paths or keys)\n\n")
	fmt.Fprintf(&b, "- trove: %s (%s), pricing data %s\n", d.TroveVersion, d.Commit, d.PricingData)
	fmt.Fprintf(&b, "- built with: %s, running on %s/%s\n", d.GoVersion, d.OS, d.Arch)
	fmt.Fprintf(&b, "- agents detected: %s\n", listOrNone(d.AgentsDetected))
	fmt.Fprintf(&b, "- agents seen in gateway traffic: %s\n", listOrNone(d.AgentsSeen))
	fmt.Fprintf(&b, "- recorded requests: %d, sessions: %d\n", d.Requests, d.Sessions)
	fmt.Fprintf(&b, "- fixes applied: %d, reverted: %d, guardrail trips: %d\n", d.FixesApplied, d.FixesReverted, d.GuardrailTrips)
	keys := make([]string, 0, len(d.ErrorClasses))
	for k := range d.ErrorClasses {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		b.WriteString("- error classes: none\n")
	} else {
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = fmt.Sprintf("%s=%d", k, d.ErrorClasses[k])
		}
		fmt.Fprintf(&b, "- error classes: %s\n", strings.Join(parts, ", "))
	}
	return b.String()
}

func listOrNone(s []string) string {
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s, ", ")
}

// IssueURL builds the prefilled new-issue link. If the body would make the link
// too long it is cut, with a visible marker, so the link still works.
func IssueURL(title, body string) (link string, truncated bool) {
	build := func(b string) string {
		q := url.Values{}
		q.Set("title", title)
		q.Set("body", b)
		q.Set("labels", "beta-feedback")
		return "https://github.com/" + Repo + "/issues/new?" + q.Encode()
	}
	link = build(body)
	const marker = "\n\n[diagnostics shortened to fit a link; run `trove feedback --json` for the full set]"
	for len(link) > MaxURLLen && len(body) > 0 {
		truncated = true
		body = strings.TrimRight(strings.ToValidUTF8(body[:len(body)*3/4], ""), "\n ")
		link = build(body + marker)
	}
	return link, truncated
}
