package fixer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// context_slim: oversized context files (CLAUDE.md, AGENTS.md) get targeted
// removals of whole sections, never a rewrite and never new text. A section is
// proposed for removal only when one of these deterministic checks holds:
//
//   - duplicate: most of its substantive lines also appear in README.md or a
//     file under docs/, which the agent can read on demand;
//   - generic: its heading is a generic overview or boilerplate heading and it
//     holds no rule words (never, must, always, ...), commands or code;
//   - repeated: the same section body appears twice in the file.
//
// Sections with rules or commands that are not duplicated elsewhere are kept
// (project-specific rules and commands are what the file is for), and so is the
// file's title. Sizes are estimates (about 4 bytes per token).

const (
	bytesPerToken  = 4
	maxDocBytes    = 2 << 20 // README and docs read for comparison
	minDupLines    = 4
	dupFraction    = 0.7
	minRepeatLines = 3
)

var headingRe = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)

// genericHeadings are overview and boilerplate headings, matched whole.
var genericHeadings = map[string]bool{
	"overview": true, "project overview": true, "about": true, "about this project": true, "introduction": true,
	"intro": true, "background": true, "table of contents": true, "contents": true,
	"license": true, "contributing": true, "acknowledgements": true, "acknowledgments": true,
}

var ruleWords = regexp.MustCompile(`(?i)\b(never|must|always|do not|don't|dont|should not|shouldn't|required|forbidden|non-negotiable|only)\b`)
var commandHint = regexp.MustCompile("(?m)^\\s*(\\$ |`{3}|(make|npm|pnpm|yarn|go|cargo|pytest|python|pip|docker|git|bash|sh) )")

type section struct {
	level      int
	title      string
	start, end int // line range [start, end), includes nested sub-sections
}

// sections splits markdown into heading-led sections, ignoring '#' inside code fences.
func sections(ls []string) []section {
	var secs []section
	var stack []int // indexes into secs of open sections
	fence := ""
	for i, l := range ls {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			mark := t[:3]
			switch {
			case fence == "":
				fence = mark
			case fence == mark:
				fence = ""
			}
			continue
		}
		if fence != "" {
			continue
		}
		m := headingRe.FindStringSubmatch(strings.TrimRight(l, "\r\n"))
		if m == nil {
			continue
		}
		lvl := len(m[1])
		for len(stack) > 0 && secs[stack[len(stack)-1]].level >= lvl {
			secs[stack[len(stack)-1]].end = i
			stack = stack[:len(stack)-1]
		}
		secs = append(secs, section{level: lvl, title: m[2], start: i, end: len(ls)})
		stack = append(stack, len(secs)-1)
	}
	return secs
}

// normLine reduces a line to comparable text: lowercase letters and digits only.
func normLine(l string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(l) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// substantive returns the normalized lines of a section body worth comparing.
func substantive(ls []string) []string {
	var out []string
	for _, l := range ls {
		if headingRe.MatchString(strings.TrimRight(l, "\r\n")) {
			continue
		}
		if n := normLine(l); len(n) >= 20 {
			out = append(out, n)
		}
	}
	return out
}

// docIndex maps normalized lines of README.md and docs/**/*.md to the file.
func docIndex(project string) map[string]string {
	idx := map[string]string{}
	add := func(p string) {
		b, err := os.ReadFile(p) //nolint:gosec // project docs, read-only
		if err != nil || len(b) > maxDocBytes {
			return
		}
		rel, _ := filepath.Rel(project, p)
		for _, n := range substantive(Lines(b)) {
			if _, ok := idx[n]; !ok {
				idx[n] = filepath.ToSlash(rel)
			}
		}
	}
	add(filepath.Join(project, "README.md"))
	_ = filepath.WalkDir(filepath.Join(project, "docs"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(strings.ToLower(p), ".md") {
			add(p)
		}
		return nil
	})
	return idx
}

func contextCandidates(e *Env) []string {
	var out []string
	for _, rel := range []string{"CLAUDE.md", ".claude/CLAUDE.md", "AGENTS.md"} {
		if e.ProjectDir != "" {
			out = append(out, filepath.Join(e.ProjectDir, filepath.FromSlash(rel)))
		}
	}
	return out
}

func estTokens(n int) int64 { return int64((n + bytesPerToken - 1) / bytesPerToken) }

func detectContextSlim(ctx context.Context, e *Env) ([]*Fix, error) {
	var out []*Fix
	var docs map[string]string
	for _, path := range contextCandidates(e) {
		b, err := os.ReadFile(path) //nolint:gosec // an allowed agent config file
		if err != nil || estTokens(len(b)) < int64(e.Cfg.ContextMinTokens) {
			continue
		}
		if docs == nil {
			docs = docIndex(e.ProjectDir)
		}
		ls := Lines(b)
		secs := sections(ls)
		var drop []LineRange
		var evidence []string
		var removed int
		covered := func(s section) bool {
			for _, r := range drop {
				if s.start >= r.Start && s.end <= r.End {
					return true
				}
			}
			return false
		}
		seen := map[string]bool{}
		for _, s := range secs {
			if s.level == 1 || covered(s) {
				continue // the title stays; nested sections of a removed one are already gone
			}
			body := ls[s.start+1 : s.end]
			text := strings.Join(body, "")
			if strings.Contains(text, "\n@") || strings.HasPrefix(text, "@") {
				continue // imports are deliberate
			}
			sub := substantive(body)
			reason := ""
			switch {
			case len(sub) >= minDupLines:
				hit, from := 0, ""
				for _, n := range sub {
					if f, ok := docs[n]; ok {
						hit++
						from = f
					}
				}
				if float64(hit) >= dupFraction*float64(len(sub)) {
					reason = fmt.Sprintf("%d of its %d lines also appear in %s", hit, len(sub), from)
				}
			}
			if reason == "" && genericHeadings[strings.ToLower(strings.TrimRight(s.title, ":"))] &&
				!ruleWords.MatchString(text) && !commandHint.MatchString(text) {
				reason = "generic overview/boilerplate heading with no rules or commands in it"
			}
			if reason == "" && len(sub) >= minRepeatLines {
				key := strings.Join(sub, "|")
				if seen[key] {
					reason = "the same section appears earlier in this file"
				}
				seen[key] = true
			}
			if reason == "" {
				continue
			}
			n := 0
			for _, l := range ls[s.start:s.end] {
				n += len(l)
			}
			removed += n
			drop = append(drop, LineRange{s.start, s.end})
			evidence = append(evidence, fmt.Sprintf("%q (lines %d-%d, ~%d tokens): %s", s.title, s.start+1, s.end, estTokens(n), reason))
		}
		if len(drop) == 0 {
			continue
		}
		upd := DeleteLines(b, drop)
		agent := "claude_code"
		if strings.EqualFold(filepath.Base(path), "AGENTS.md") {
			agent = "codex"
		}
		f := &Fix{
			ID: fixID(ContextSlim, path), Kind: ContextSlim, Agent: agent, Path: path,
			Title: fmt.Sprintf("Trim %s: remove %d section(s), about %d tokens", filepath.Base(path), len(drop), estTokens(removed)),
			Why: fmt.Sprintf("%s is about %d tokens and is sent with every request. These sections repeat what the agent can read in the README or docs, or say nothing specific to this project. "+
				"Short files that hold only what the agent cannot discover on its own tend to work better and cost less.", filepath.Base(path), estTokens(len(b))),
			Evidence: evidence,
			Notes: []string{
				"Trove only deletes whole sections; it writes no new text. Your project-specific rules and commands stay.",
				"Token counts are estimates (about 4 bytes per token).",
			},
			old: b, updated: upd,
		}
		// The saving is per request: the file is part of every request's prompt.
		per := estTokens(len(b)) - estTokens(len(upd))
		f.TokensPerSession, f.USDPerSession = e.schemaTokens(ctx).priceTokens(e, per)
		f.Notes = append(f.Notes, fmt.Sprintf("About %d tokens fewer in every request; the session figure multiplies by the average requests per session from your gateway data, or counts one request if there is none.", per))
		out = append(out, f)
	}
	return out, nil
}
