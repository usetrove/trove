// Command checkclaims keeps launch drafts honest. Every figure in docs/launch
// must sit inside a slot of the form
//
//	[MEASURED: what it is — source: path/to/results/file]
//	[ESTIMATED: what it is — source: path/to/results/file]
//
// In draft mode a slot whose source still has a <placeholder> is allowed. With
// --publish every slot's source must be an existing file, so a draft cannot be
// published while it holds an invented or unresolved number.
//
// Usage: go run ./scripts/checkclaims [--publish] [--root DIR] [FILE...]
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	slotRe = regexp.MustCompile(`\[(MEASURED|ESTIMATED):([^\]]*)\]`)
	// A figure that reads as a claim: money, percentages, multipliers, token counts.
	claimRe = regexp.MustCompile(`\$\d|\d\s?%|\d(\.\d+)?\s?x\b|\d\s?[kKmM]?\s?tokens?\b`)
)

type problem struct {
	file string
	line int
	msg  string
}

// check returns the problems found in one document.
func check(file, text, root string, publish bool) []problem {
	var ps []problem
	inFence := false
	for i, line := range strings.Split(text, "\n") {
		n := i + 1
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		for _, m := range slotRe.FindAllStringSubmatch(line, -1) {
			body := m[2]
			idx := strings.LastIndex(body, "source:")
			if idx < 0 {
				ps = append(ps, problem{file, n, "slot has no `source:`: " + m[0]})
				continue
			}
			src := strings.TrimSpace(body[idx+len("source:"):])
			unresolved := strings.ContainsAny(src, "<>") || src == "" || strings.EqualFold(src, "pending")
			switch {
			case unresolved && publish:
				ps = append(ps, problem{file, n, "unresolved source in publish mode: " + m[0]})
			case unresolved:
			default:
				if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(src))); err != nil {
					ps = append(ps, problem{file, n, "source file does not exist: " + src})
				}
			}
		}
		bare := slotRe.ReplaceAllString(line, "")
		if loc := claimRe.FindString(bare); loc != "" {
			ps = append(ps, problem{file, n, fmt.Sprintf("figure %q is outside a MEASURED/ESTIMATED slot", strings.TrimSpace(loc))})
		}
	}
	return ps
}

func main() {
	publish := flag.Bool("publish", false, "fail on any slot whose source is not an existing file")
	root := flag.String("root", ".", "repository root that slot sources are relative to")
	flag.Parse()
	files := flag.Args()
	if len(files) == 0 {
		var err error
		if files, err = filepath.Glob(filepath.Join(*root, "docs", "launch", "*.md")); err != nil || len(files) == 0 {
			fmt.Fprintln(os.Stderr, "checkclaims: no files given and none found in docs/launch. Pass file paths.")
			os.Exit(2)
		}
	}
	bad := 0
	for _, f := range files {
		b, err := os.ReadFile(f) //nolint:gosec // developer-supplied draft path
		if err != nil {
			fmt.Fprintf(os.Stderr, "checkclaims: cannot read %s: %v\n", f, err)
			os.Exit(2)
		}
		for _, p := range check(f, string(b), *root, *publish) {
			fmt.Printf("%s:%d: %s\n", p.file, p.line, p.msg)
			bad++
		}
	}
	if bad > 0 {
		fmt.Fprintf(os.Stderr, "checkclaims: %d problem(s). Put each figure in a slot with a real source, or remove it.\n", bad)
		os.Exit(1)
	}
	fmt.Printf("checkclaims: %d file(s) ok\n", len(files))
}
