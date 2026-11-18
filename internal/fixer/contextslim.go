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
// removals of whole sections, never a rewrite and never new text. The T1 fix
// (context_slim) removes only provable duplicates:
//
//   - every non-blank line of the section also appears in README.md or a file
//     under docs/, which the agent can read on demand; or
//   - the same section body appears earlier in this file.
//
// Each removed line is shown next to the source line it duplicates, so the
// evidence is the matching text itself.
//
// Removing a generic overview or boilerplate section (no rule words, commands or
// code, and no copy elsewhere) is not provably lossless: it changes what the
// agent is told. It is offered separately as context_slim_generic, a T2
// behavioural suggestion that is never part of --all.
//
// Sections with rules or commands that are not duplicated elsewhere are kept
// (project-specific rules and commands are what the file is for), and so is the
// file's title. Sizes are estimates (about 4 bytes per token).

const (
	bytesPerToken  = 4
	maxDocBytes    = 2 << 20 // README and docs read for comparison
	minDupLines    = 4
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
			switch fence {
			case "":
				fence = mark
			case mark:
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

// srcLine is one line of comparable text and where it was found.
type srcLine struct {
	file string // display name
	line int    // 1-based
	norm string
	text string // trimmed original text, shown as evidence
}

// contentLines returns the non-heading lines of ls that have comparable text.
// first is the 0-based index of ls[0] in its file.
func contentLines(file string, ls []string, first int) []srcLine {
	var out []srcLine
	for i, l := range ls {
		raw := strings.TrimRight(l, "\r\n")
		if headingRe.MatchString(raw) {
			continue
		}
		if n := normLine(raw); n != "" {
			out = append(out, srcLine{file: file, line: first + i + 1, norm: n, text: strings.TrimSpace(raw)})
		}
	}
	return out
}

// substantial counts the lines long enough to make a match meaningful.
func substantial(ls []srcLine) int {
	n := 0
	for _, l := range ls {
		if len(l.norm) >= 20 {
			n++
		}
	}
	return n
}

// docIndex maps comparable lines of README.md and docs/**/*.md to their first occurrence.
func docIndex(project string) map[string]srcLine {
	idx := map[string]srcLine{}
	add := func(p string) {
		b, err := os.ReadFile(p) //nolint:gosec // project docs, read-only
		if err != nil || len(b) > maxDocBytes {
			return
		}
		rel, _ := filepath.Rel(project, p)
		for _, l := range contentLines(filepath.ToSlash(rel), Lines(b), 0) {
			if _, ok := idx[l.norm]; !ok {
				idx[l.norm] = l
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

// proofLines pairs each line of a section with the source line it duplicates.
func proofLines(title string, body, src []srcLine) []string {
	out := make([]string, 0, len(body))
	for i, l := range body {
		out = append(out, fmt.Sprintf("%q line %d = %s line %d: %q", title, l.line, src[i].file, src[i].line, src[i].text))
	}
	return out
}

// removal is one section a fix deletes, with the evidence for it.
type removal struct {
	r        LineRange
	bytes    int
	evidence []string
}

func overlaps(drop []removal, s section) bool {
	for _, d := range drop {
		if s.start < d.r.End && d.r.Start < s.end {
			return true
		}
	}
	return false
}

func sectionBytes(ls []string, s section) int {
	n := 0
	for _, l := range ls[s.start:s.end] {
		n += len(l)
	}
	return n
}

func detectContextSlim(ctx context.Context, e *Env) ([]*Fix, error) {
	var out []*Fix
	var docs map[string]srcLine
	for _, path := range contextCandidates(e) {
		b, err := os.ReadFile(path) //nolint:gosec // an allowed agent config file
		if err != nil || estTokens(len(b)) < int64(e.Cfg.ContextMinTokens) {
			continue
		}
		if docs == nil {
			docs = docIndex(e.ProjectDir)
		}
		name := filepath.Base(path)
		ls := Lines(b)
		secs := sections(ls)

		// T1: provable duplicates only.
		var dups []removal
		seen := map[string][]srcLine{} // section body key -> the earlier copy's lines
		for _, s := range secs {
			if s.level == 1 || overlaps(dups, s) {
				continue // the title stays; nested sections of a removed one are already gone
			}
			bodyLs := ls[s.start+1 : s.end]
			if hasImport(bodyLs) {
				continue // imports are deliberate
			}
			body := contentLines(name, bodyLs, s.start+1)
			if len(body) == 0 {
				continue
			}
			var ev []string
			if substantial(body) >= minDupLines {
				src := make([]srcLine, 0, len(body))
				for _, l := range body {
					d, ok := docs[l.norm]
					if !ok {
						break
					}
					src = append(src, d)
				}
				if len(src) == len(body) {
					ev = append([]string{fmt.Sprintf("%q (lines %d-%d, ~%d tokens): every line also appears in %s",
						s.title, s.start+1, s.end, estTokens(sectionBytes(ls, s)), sourceFiles(src))}, proofLines(s.title, body, src)...)
				}
			}
			if ev == nil && substantial(body) >= minRepeatLines {
				norms := make([]string, len(body))
				for i, l := range body {
					norms[i] = l.norm
				}
				key := strings.Join(norms, "\n")
				if earlier, ok := seen[key]; ok {
					ev = append([]string{fmt.Sprintf("%q (lines %d-%d, ~%d tokens): the same section appears earlier in %s",
						s.title, s.start+1, s.end, estTokens(sectionBytes(ls, s)), name)}, proofLines(s.title, body, earlier)...)
				} else {
					seen[key] = body
				}
			}
			if ev != nil {
				dups = append(dups, removal{LineRange{s.start, s.end}, sectionBytes(ls, s), ev})
			}
		}
		if f := contextFix(ctx, e, ContextSlim, path, b, dups); f != nil {
			out = append(out, f)
		}

		// T2: generic sections that duplicate nothing. Offered on their own, by id only.
		var generic []removal
		for _, s := range secs {
			if s.level == 1 || overlaps(dups, s) || overlaps(generic, s) {
				continue
			}
			bodyLs := ls[s.start+1 : s.end]
			text := strings.Join(bodyLs, "")
			if hasImport(bodyLs) || !genericHeadings[strings.ToLower(strings.TrimRight(s.title, ":"))] ||
				ruleWords.MatchString(text) || commandHint.MatchString(text) {
				continue
			}
			n := sectionBytes(ls, s)
			generic = append(generic, removal{LineRange{s.start, s.end}, n, []string{fmt.Sprintf(
				"%q (lines %d-%d, ~%d tokens): generic overview/boilerplate heading with no rules, commands or code in it, and no copy elsewhere",
				s.title, s.start+1, s.end, estTokens(n))}})
		}
		if f := contextFix(ctx, e, ContextSlimGeneric, path, b, generic); f != nil {
			out = append(out, f)
		}
	}
	return out, nil
}

func hasImport(body []string) bool {
	text := strings.Join(body, "")
	return strings.Contains(text, "\n@") || strings.HasPrefix(text, "@")
}

// sourceFiles names the files the matching lines came from, in first-seen order.
func sourceFiles(src []srcLine) string {
	var names []string
	seen := map[string]bool{}
	for _, s := range src {
		if !seen[s.file] {
			seen[s.file] = true
			names = append(names, s.file)
		}
	}
	return strings.Join(names, ", ")
}

// contextFix builds the fix that deletes rs from the file at path, or nil when rs is empty.
func contextFix(ctx context.Context, e *Env, kind, path string, b []byte, rs []removal) *Fix {
	if len(rs) == 0 {
		return nil
	}
	var drop []LineRange
	var evidence []string
	removed := 0
	for _, r := range rs {
		drop = append(drop, r.r)
		evidence = append(evidence, r.evidence...)
		removed += r.bytes
	}
	upd := DeleteLines(b, drop)
	name := filepath.Base(path)
	agent := "claude_code"
	if strings.EqualFold(name, "AGENTS.md") {
		agent = "codex"
	}
	f := &Fix{
		ID: fixID(kind, path), Kind: kind, Agent: agent, Path: path,
		Title:    fmt.Sprintf("Trim %s: remove %d duplicate section(s), about %d tokens", name, len(drop), estTokens(removed)),
		Evidence: evidence,
		old:      b, updated: upd,
	}
	if kind == ContextSlim {
		f.Why = fmt.Sprintf("%s is about %d tokens and is sent with every request. These sections are copies: every line also appears in the README, docs/ or earlier in the file, and the evidence below shows each matching line. "+
			"The agent can still read the originals on demand.", name, estTokens(len(b)))
		f.Notes = []string{
			"Trove only deletes whole sections whose every line it found elsewhere; it writes no new text. Your project-specific rules and commands stay.",
		}
	} else {
		f.Title = fmt.Sprintf("Trim %s: remove %d generic section(s), about %d tokens", name, len(drop), estTokens(removed))
		f.Suggestion = true
		f.Why = fmt.Sprintf("%s is about %d tokens and is sent with every request. These sections hold only a generic overview or boilerplate. "+
			"Nothing proves they are copies, so removing them changes what the agent is told: a behavioural (T2) change you apply by id, never with --all.", name, estTokens(len(b)))
		f.Notes = []string{
			"Trove only deletes whole sections; it writes no new text. `trove undo <id>` restores the file byte for byte.",
		}
	}
	f.Notes = append(f.Notes, "Token counts are estimates (about 4 bytes per token).")
	// The saving is per request: the file is part of every request's prompt.
	per := estTokens(len(b)) - estTokens(len(upd))
	schema := e.schemaTokens(ctx)
	schema.setResent(f, per, "estimated", "the removed sections")
	f.TokensPerSession, f.USDPerSession = schema.priceTokens(e, per)
	f.Notes = append(f.Notes, "The session figure multiplies the per-request tokens by the measured average requests per session, or counts one request if Trove has no history.")
	return f
}
