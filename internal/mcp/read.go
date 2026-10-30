package mcp

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/usetrove/trove/internal/mcp/outline"
	"github.com/usetrove/trove/internal/mcp/root"
)

// Limits for trove_read. A file at or under the small limits is returned whole;
// anything bigger gets an outline plus the part that was asked for.
const (
	smallFileBytes = 8 << 10
	smallFileLines = 200
	maxFileBytes   = 2 << 20
	maxSliceBytes  = 24 << 10
	maxOutlineRows = 150
)

// ReadArgs are the trove_read inputs.
type ReadArgs struct {
	Path   string `json:"path"`
	Symbol string `json:"symbol,omitempty"`
	Start  int    `json:"start,omitempty"`
	End    int    `json:"end,omitempty"`
}

// Read implements trove_read. It only ever opens files inside the project.
func Read(r *root.Root, a ReadArgs) (string, error) {
	f, fi, err := r.OpenRead(a.Path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	if fi.IsDir() {
		return "", fmt.Errorf("%s is a folder. Give a file path, or use trove_search to find files", a.Path)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", a.Path)
	}
	if fi.Size() > maxFileBytes {
		return "", fmt.Errorf("%s is %d bytes, over the %d byte limit. Use trove_search to find the part you need", a.Path, fi.Size(), maxFileBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %w", a.Path, err)
	}
	if len(data) > maxFileBytes {
		return "", fmt.Errorf("%s is over the %d byte limit", a.Path, maxFileBytes)
	}
	if isBinary(data) {
		return "", fmt.Errorf("%s looks like a binary file, so it was not read", a.Path)
	}
	if a.Symbol != "" && (a.Start != 0 || a.End != 0) {
		return "", fmt.Errorf("give either symbol or a line range, not both")
	}
	if a.Start < 0 || a.End < 0 || (a.End != 0 && a.Start > a.End) {
		return "", fmt.Errorf("line range %d-%d is not valid", a.Start, a.End)
	}

	text := string(data)
	lines := splitLines(text)
	n := len(lines)
	small := len(data) <= smallFileBytes && n <= smallFileLines
	asked := a.Symbol != "" || a.Start != 0 || a.End != 0
	if small && !asked {
		return text, nil
	}

	var syms []outline.Symbol
	if a.Symbol != "" || !small {
		syms = outline.Of(a.Path, data)
	}
	start, end := a.Start, a.End
	if a.Symbol != "" {
		s, ok := outline.Find(syms, a.Symbol)
		if !ok {
			return "", fmt.Errorf("no function or type named %q in %s. Try trove_read without symbol to see the outline, or give a line range", a.Symbol, a.Path)
		}
		start, end = s.Start, s.End
	}
	var b strings.Builder
	if !small {
		fmt.Fprintf(&b, "%s: %d lines, %d bytes (large; outline)\n", a.Path, n, len(data))
		writeOutline(&b, syms)
	}
	if !asked {
		if len(syms) == 0 {
			// No outline for this language: show the head so the call is useful.
			end = min(n, 60)
			start = 1
			fmt.Fprintf(&b, "no outline for this file type; first %d lines:\n", end)
		} else {
			b.WriteString("ask for a symbol or line range to read a part\n")
			return b.String(), nil
		}
	}
	if start < 1 {
		start = 1
	}
	if end == 0 || end > n {
		end = n
	}
	if start > n {
		return "", fmt.Errorf("%s has only %d lines", a.Path, n)
	}
	body, shownEnd := joinLimited(lines[start-1:end], maxSliceBytes)
	shownEnd += start - 1
	fmt.Fprintf(&b, "lines %d-%d:\n%s", start, shownEnd, body)
	if shownEnd < end {
		fmt.Fprintf(&b, "\n[%d more lines omitted; ask for lines %d-%d]\n", end-shownEnd, shownEnd+1, end)
	}
	return b.String(), nil
}

func writeOutline(b *strings.Builder, syms []outline.Symbol) {
	for i, s := range syms {
		if i == maxOutlineRows {
			fmt.Fprintf(b, "[%d more symbols omitted]\n", len(syms)-i)
			break
		}
		fmt.Fprintf(b, "%s %s %d-%d\n", s.Kind, s.Name, s.Start, s.End)
	}
}

// splitLines keeps each line's ending so joined lines give the original bytes.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	ls := strings.SplitAfter(s, "\n")
	if ls[len(ls)-1] == "" {
		ls = ls[:len(ls)-1]
	}
	return ls
}

// joinLimited joins whole lines up to limit bytes and says how many it used.
// It always includes at least one line.
func joinLimited(ls []string, limit int) (string, int) {
	var b strings.Builder
	used := 0
	for _, l := range ls {
		if used > 0 && b.Len()+len(l) > limit {
			break
		}
		b.WriteString(l)
		used++
	}
	return b.String(), used
}

// isBinary reports whether data is not text: a NUL byte, or more than 5% bytes
// that are not valid UTF-8, in the first 8 KB.
func isBinary(data []byte) bool {
	head := data
	if len(head) > 8000 {
		head = head[:8000]
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return true
	}
	bad, total := 0, 0
	for i := 0; i < len(head); {
		r, sz := utf8.DecodeRune(head[i:])
		// a rune cut off by the 8 KB limit is not evidence of binary
		if r == utf8.RuneError && sz == 1 && (len(head) >= len(data) || len(head)-i >= utf8.UTFMax) {
			bad++
		}
		total++
		i += sz
	}
	return total > 0 && float64(bad)/float64(total) > 0.05
}
