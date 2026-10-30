package mcp

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/usetrove/trove/internal/mcp/root"
)

// Limits for trove_search.
const (
	searchMaxFileBytes = 1 << 20
	searchMaxFiles     = 20000
	searchMaxResults   = 40
	searchPerFile      = 5
	searchMaxBytes     = 6 << 10
	searchLineBytes    = 160
)

// skipDirs are never searched: they hold dependencies, build output or VCS data.
var skipDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true, "node_modules": true, "vendor": true,
	"dist": true, "build": true, "target": true, "__pycache__": true, ".venv": true,
	"venv": true, ".idea": true, ".next": true, ".trove": true,
}

// SearchArgs are the trove_search inputs.
type SearchArgs struct {
	Query string `json:"query"`
	Glob  string `json:"glob,omitempty"`
}

type hit struct {
	path  string
	line  int
	text  string
	score int
	dups  int
}

// Search implements trove_search: a pure-Go, case-insensitive text search of the
// project, ranked and deduplicated, with a count of what was left out.
func Search(r *root.Root, a SearchArgs) (string, error) {
	q := strings.TrimSpace(a.Query)
	if q == "" {
		return "", errors.New("query is empty. Give the text to look for")
	}
	var globRe *regexp.Regexp
	if a.Glob != "" {
		var err error
		if globRe, err = globToRegexp(a.Glob); err != nil {
			return "", fmt.Errorf("glob %q is not valid: %v", a.Glob, err)
		}
	}
	ql := strings.ToLower(q)
	wordRe := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(q) + `\b`)
	ignore := loadIgnore(r.Dir())

	var hits []hit
	filesScanned, truncatedWalk := 0, false
	_ = filepath.WalkDir(r.Dir(), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel := r.Rel(p)
		if d.IsDir() {
			if p != r.Dir() && (skipDirs[d.Name()] || ignore.match(rel, true)) {
				return filepath.SkipDir
			}
			return nil
		}
		// Only regular files that are not symlinks: a link could lead outside.
		if !d.Type().IsRegular() || ignore.match(rel, false) || root.Sensitive(rel) {
			return nil
		}
		if globRe != nil && !globMatches(globRe, a.Glob, rel) {
			return nil
		}
		if filesScanned >= searchMaxFiles {
			truncatedWalk = true
			return filepath.SkipAll
		}
		fi, err := d.Info()
		if err != nil || fi.Size() > searchMaxFileBytes {
			return nil
		}
		filesScanned++
		pathBonus := 0
		if strings.Contains(strings.ToLower(rel), ql) {
			pathBonus = 3
		}
		hits = append(hits, scanFile(p, rel, ql, wordRe, pathBonus)...)
		return nil
	})

	if len(hits) == 0 {
		return fmt.Sprintf("no matches for %q in %d files", q, filesScanned), nil
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		if hits[i].path != hits[j].path {
			return hits[i].path < hits[j].path
		}
		return hits[i].line < hits[j].line
	})

	// Deduplicate identical lines across files, then cap per file and overall.
	seen := map[string]int{}
	perFile := map[string]int{}
	var kept []hit
	omitted := 0
	for _, h := range hits {
		if i, ok := seen[h.text]; ok {
			kept[i].dups++
			omitted++
			continue
		}
		if perFile[h.path] >= searchPerFile || len(kept) >= searchMaxResults {
			omitted++
			continue
		}
		perFile[h.path]++
		seen[h.text] = len(kept)
		kept = append(kept, h)
	}
	var b strings.Builder
	shown := 0
	for _, h := range kept {
		row := fmt.Sprintf("%s:%d: %s", h.path, h.line, h.text)
		if h.dups > 0 {
			row += fmt.Sprintf("  (+%d identical)", h.dups)
		}
		row += "\n"
		if b.Len()+len(row) > searchMaxBytes && shown > 0 {
			omitted += len(kept) - shown
			break
		}
		b.WriteString(row)
		shown++
	}
	fmt.Fprintf(&b, "%d shown", shown)
	if omitted > 0 {
		fmt.Fprintf(&b, ", %d more omitted; narrow the query or glob", omitted)
	}
	if truncatedWalk {
		fmt.Fprintf(&b, ", stopped after %d files", searchMaxFiles)
	}
	b.WriteString("\n")
	return b.String(), nil
}

var defRe = regexp.MustCompile(`^\s*(?:export\s+|pub\s+|public\s+|private\s+|static\s+|async\s+)*(?:func|def|class|type|interface|struct|enum|fn|trait|impl|function|const|var|let)\b`)

func scanFile(p, rel, ql string, wordRe *regexp.Regexp, pathBonus int) []hit {
	f, err := os.Open(p)
	if err != nil {
		return nil
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 256<<10)
	var out []hit
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if n == 1 && strings.IndexByte(line, 0) >= 0 {
			return nil // binary
		}
		if !strings.Contains(strings.ToLower(line), ql) {
			continue
		}
		score := 1 + pathBonus
		if defRe.MatchString(line) {
			score += 2
		}
		if wordRe.MatchString(line) {
			score++
		}
		text := strings.TrimSpace(line)
		if len(text) > searchLineBytes {
			text = text[:searchLineBytes] + "…"
		}
		out = append(out, hit{path: rel, line: n, text: text, score: score})
	}
	return out
}

// globToRegexp translates a glob with ** into a regexp over slash paths.
func globToRegexp(g string) (*regexp.Regexp, error) {
	g = filepath.ToSlash(g)
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(g); i++ {
		switch c := g[i]; c {
		case '*':
			if i+1 < len(g) && g[i+1] == '*' {
				i++
				if i+1 < len(g) && g[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// globMatches matches the whole path, or just the file name when the glob has
// no slash (so "*.go" finds Go files at any depth).
func globMatches(re *regexp.Regexp, glob, rel string) bool {
	if !strings.Contains(glob, "/") {
		return re.MatchString(path.Base(rel))
	}
	return re.MatchString(rel)
}

// ignoreSet is a small subset of .gitignore: plain names and globs, optionally
// ending in "/" for folders. Negations and anchored paths are not supported;
// the built-in skip list covers the common cases.
type ignoreSet struct{ pats []ignorePat }

type ignorePat struct {
	re      *regexp.Regexp
	dirOnly bool
	hasPath bool
}

func loadIgnore(dir string) ignoreSet {
	b, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		return ignoreSet{}
	}
	var s ignoreSet
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(strings.TrimSuffix(l, "\r"))
		if l == "" || strings.HasPrefix(l, "#") || strings.HasPrefix(l, "!") {
			continue
		}
		p := ignorePat{dirOnly: strings.HasSuffix(l, "/")}
		l = strings.Trim(l, "/")
		p.hasPath = strings.Contains(l, "/")
		re, err := globToRegexp(l)
		if err != nil {
			continue
		}
		p.re = re
		s.pats = append(s.pats, p)
	}
	return s
}

func (s ignoreSet) match(rel string, isDir bool) bool {
	for _, p := range s.pats {
		if p.dirOnly && !isDir {
			continue
		}
		target := rel
		if !p.hasPath {
			target = path.Base(rel)
		}
		if p.re.MatchString(target) {
			return true
		}
	}
	return false
}
