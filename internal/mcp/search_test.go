package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/mcp/root"
)

func searchProject(t *testing.T) (*root.Root, string) {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "auth/login.go", "package auth\n\nfunc Login(user string) error {\n\treturn nil\n}\n\n// call Login here\n")
	write(t, dir, "auth/login_test.go", "package auth\n\nfunc TestLogin(t *testing.T) {\n\tLogin(\"a\")\n}\n")
	write(t, dir, "web/app.ts", "export function render() {\n  login()\n}\n")
	write(t, dir, "docs/a.md", "see Login docs\n")
	write(t, dir, "docs/b.md", "see Login docs\n")
	write(t, dir, "node_modules/dep/index.js", "Login Login Login\n")
	write(t, dir, ".git/config", "Login\n")
	write(t, dir, "ignored/x.txt", "Login\n")
	write(t, dir, "bin.dat", "Login\x00\x01")
	write(t, dir, ".gitignore", "ignored/\n*.log\n")
	write(t, dir, "run.log", "Login\n")
	r, err := root.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r, dir
}

func TestSearchRankingAndSkips(t *testing.T) {
	r, _ := searchProject(t)
	got, err := Search(r, SearchArgs{Query: "login"})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if !strings.HasPrefix(lines[0], "auth/login.go:3:") {
		t.Errorf("the definition in a file named login should rank first, got:\n%s", got)
	}
	for _, bad := range []string{"node_modules", ".git/", "ignored/", "run.log", "bin.dat"} {
		if strings.Contains(got, bad) {
			t.Errorf("%s must not be searched:\n%s", bad, got)
		}
	}
}

func TestSearchDedupe(t *testing.T) {
	r, _ := searchProject(t)
	got, _ := Search(r, SearchArgs{Query: "see Login docs"})
	if strings.Count(got, "see Login docs") != 1 || !strings.Contains(got, "(+1 identical)") {
		t.Errorf("identical lines should collapse:\n%s", got)
	}
}

func TestSearchGlob(t *testing.T) {
	r, _ := searchProject(t)
	tests := []struct {
		glob, want, notWant string
	}{
		{"*.go", "auth/login.go", "web/app.ts"},
		{"**/*.ts", "web/app.ts", "auth/"},
		{"auth/*.go", "auth/login.go", "web/"},
		{"docs/**", "docs/a.md", "auth/"},
	}
	for _, tc := range tests {
		got, err := Search(r, SearchArgs{Query: "login", Glob: tc.glob})
		if err != nil || !strings.Contains(got, tc.want) || strings.Contains(got, tc.notWant) {
			t.Errorf("glob %q: want %q not %q, got %v\n%s", tc.glob, tc.want, tc.notWant, err, got)
		}
	}
}

func TestSearchCapAndOmittedCount(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 100; i++ {
		write(t, dir, fmt.Sprintf("f%03d.txt", i), fmt.Sprintf("needle number %d\n", i))
	}
	r, _ := root.New(dir)
	got, err := Search(r, SearchArgs{Query: "needle"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > searchMaxBytes+200 {
		t.Errorf("output %d bytes is over the cap", len(got))
	}
	if !strings.Contains(got, "40 shown, 60 more omitted") {
		t.Errorf("want omitted count, got:\n%s", got[max(0, len(got)-120):])
	}
	// Per-file cap.
	write(t, dir, "many.txt", strings.Repeat("needle x\n", 3)+"needle a\nneedle b\nneedle c\nneedle d\n")
	got, _ = Search(r, SearchArgs{Query: "needle", Glob: "many.txt"})
	if strings.Count(got, "many.txt:") != searchPerFile {
		t.Errorf("want %d rows for one file:\n%s", searchPerFile, got)
	}
}

func TestSearchDeterministic(t *testing.T) {
	r, _ := searchProject(t)
	a, _ := Search(r, SearchArgs{Query: "login"})
	for i := 0; i < 5; i++ {
		if b, _ := Search(r, SearchArgs{Query: "login"}); a != b {
			t.Fatal("search output must be byte-identical for the same project")
		}
	}
}

func TestSearchNoMatchAndBadInput(t *testing.T) {
	r, _ := searchProject(t)
	got, err := Search(r, SearchArgs{Query: "zzzzqqq"})
	if err != nil || !strings.HasPrefix(got, "no matches") {
		t.Errorf("%q %v", got, err)
	}
	if _, err := Search(r, SearchArgs{Query: "  "}); err == nil {
		t.Error("empty query must error")
	}
}

func TestSearchDoesNotFollowSymlinks(t *testing.T) {
	r, dir := searchProject(t)
	outside := filepath.Join(t.TempDir(), "secret")
	_ = os.MkdirAll(outside, 0o755)
	_ = os.WriteFile(filepath.Join(outside, "s.txt"), []byte("Login secret\n"), 0o644)
	if err := os.Symlink(outside, filepath.Join(dir, "linkdir")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	got, _ := Search(r, SearchArgs{Query: "secret"})
	if strings.Contains(got, "s.txt") {
		t.Fatalf("search followed a symlink out of the project:\n%s", got)
	}
}

func TestSearchSkipsSecretFiles(t *testing.T) {
	r, dir := searchProject(t)
	write(t, dir, ".env", "Login=hunter2\n")
	write(t, dir, "deploy/key.pem", "Login private\n")
	got, _ := Search(r, SearchArgs{Query: "hunter2"})
	if strings.Contains(got, "Login=hunter2") || strings.Contains(got, ".env:") {
		t.Errorf("search returned a secret file:\n%s", got)
	}
	got, _ = Search(r, SearchArgs{Query: "private"})
	if strings.Contains(got, "key.pem") {
		t.Errorf("search returned a key file:\n%s", got)
	}
}
