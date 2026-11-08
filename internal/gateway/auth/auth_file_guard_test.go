package auth

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Trove never reads Codex's auth.json, which holds ChatGPT login tokens (CLAUDE.md
// rule 3). This test fails if any non-test Go file in internal/ or cmd/ has a line
// that both names auth.json and opens or reads a file. It catches the literal
// cases only; a path built at run time is not seen, so review still matters.
func TestNoGoCodeReadsAuthJSON(t *testing.T) {
	root := filepath.Join("..", "..", "..") // this package is internal/gateway/auth
	var violations []string
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := os.Open(path) //nolint:gosec // the repository's own source files
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			sc := bufio.NewScanner(f)
			for line := 1; sc.Scan(); line++ {
				text := sc.Text()
				if strings.Contains(text, "auth.json") && (strings.Contains(text, "ReadFile(") ||
					strings.Contains(text, "Open(") || strings.Contains(text, "OpenFile(")) {
					violations = append(violations, path+":"+strconv.Itoa(line))
				}
			}
			return sc.Err()
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(violations) > 0 {
		t.Fatalf("code reads a Codex auth.json (ChatGPT login tokens), which Trove must never do: %v", violations)
	}
}
