package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/mcp/outline"
	"github.com/usetrove/trove/internal/mcp/root"
)

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// bigGo is a Go file with 60 functions and one type, well over the small limits.
func bigGo() string {
	var b strings.Builder
	b.WriteString("package big\n\ntype Widget struct {\n\tName string\n}\n\n")
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&b, "// Fn%d does work number %d.\nfunc Fn%d(x int) int {\n\t// padding padding padding padding padding\n\ty := x + %d\n\treturn y * 2\n}\n\n", i, i, i, i)
	}
	b.WriteString("func (w *Widget) Hello() string {\n\treturn \"hi \" + w.Name\n}\n")
	return b.String()
}

func newProject(t *testing.T) (*root.Root, string) {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "small.txt", "hello\nworld\n")
	write(t, dir, "big.go", bigGo())
	write(t, dir, "mod.py", "import os\n\nclass A:\n    def f(self):\n        return 1\n\n    def g(self):\n        return 2\n\ndef top():\n    return 3\n"+strings.Repeat("# filler line to make this file large enough\n", 250))
	write(t, dir, "notes.xyz", strings.Repeat("plain text line without a known language\n", 1000))
	r, err := root.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r, dir
}

func TestReadSmallFileIsRaw(t *testing.T) {
	r, dir := newProject(t)
	got, err := Read(r, ReadArgs{Path: "small.txt"})
	raw, _ := os.ReadFile(filepath.Join(dir, "small.txt"))
	if err != nil || got != string(raw) {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestReadLargeGoOutlineAndSize(t *testing.T) {
	r, dir := newProject(t)
	raw, _ := os.ReadFile(filepath.Join(dir, "big.go"))
	got, err := Read(r, ReadArgs{Path: "big.go"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"(large; outline)", "type Widget", "func Fn0 ", "func Fn59 ", "func Widget.Hello "} {
		if !strings.Contains(got, want) {
			t.Errorf("outline missing %q:\n%s", want, got)
		}
	}
	if len(got)*3 > len(raw) {
		t.Errorf("outline is %d bytes for a %d byte file; want under a third", len(got), len(raw))
	}
}

func TestReadSymbolMatchesRawLines(t *testing.T) {
	r, dir := newProject(t)
	raw, _ := os.ReadFile(filepath.Join(dir, "big.go"))
	syms := outline.Of("big.go", raw)
	s, ok := outline.Find(syms, "Fn7")
	if !ok {
		t.Fatal("Fn7 not in outline")
	}
	got, err := Read(r, ReadArgs{Path: "big.go", Symbol: "Fn7"})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join(strings.SplitAfter(string(raw), "\n")[s.Start-1:s.End], "")
	if !strings.HasSuffix(got, want) || !strings.Contains(got, fmt.Sprintf("lines %d-%d:", s.Start, s.End)) {
		t.Errorf("symbol body differs from the file's lines:\n%s", got)
	}
}

func TestReadRange(t *testing.T) {
	r, dir := newProject(t)
	raw, _ := os.ReadFile(filepath.Join(dir, "big.go"))
	got, err := Read(r, ReadArgs{Path: "big.go", Start: 10, End: 12})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join(strings.SplitAfter(string(raw), "\n")[9:12], "")
	if !strings.HasSuffix(got, want) {
		t.Errorf("range mismatch:\n%s", got)
	}
	small, err := Read(r, ReadArgs{Path: "small.txt", Start: 2})
	if err != nil || !strings.HasSuffix(small, "world\n") || strings.Contains(small, "hello") {
		t.Errorf("small-file range: %q %v", small, err)
	}
}

func TestReadPythonAndUnknownLanguage(t *testing.T) {
	r, _ := newProject(t)
	got, err := Read(r, ReadArgs{Path: "mod.py"})
	if err != nil || !strings.Contains(got, "class A 3-8") || !strings.Contains(got, "def top ") {
		t.Fatalf("python outline: %v\n%s", err, got)
	}
	got, err = Read(r, ReadArgs{Path: "notes.xyz"})
	if err != nil || !strings.Contains(got, "no outline for this file type") || strings.Count(got, "\n") > 70 {
		t.Fatalf("unknown language: %v\n%d lines", err, strings.Count(got, "\n"))
	}
}

func TestReadSliceIsCapped(t *testing.T) {
	r, _ := newProject(t)
	got, err := Read(r, ReadArgs{Path: "notes.xyz", Start: 1, End: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > maxSliceBytes+300 || !strings.Contains(got, "more lines omitted; ask for lines") {
		t.Errorf("slice not capped (%d bytes)", len(got))
	}
}

func TestReadRefusals(t *testing.T) {
	r, dir := newProject(t)
	outside := filepath.Join(filepath.Dir(dir), "secret-"+filepath.Base(dir)+".txt")
	_ = os.WriteFile(outside, []byte("secret"), 0o644)
	t.Cleanup(func() { _ = os.Remove(outside) })
	write(t, dir, "huge.txt", strings.Repeat("x", maxFileBytes+1))
	write(t, dir, "bin.dat", "abc\x00def")
	write(t, dir, "bin2.dat", strings.Repeat("\xff\xfe\xfd\xfc", 500))
	tests := []struct {
		name string
		args ReadArgs
		want string
	}{
		{"traversal", ReadArgs{Path: "../" + filepath.Base(outside)}, "outside the project"},
		{"absolute", ReadArgs{Path: outside}, "outside the project"},
		{"missing", ReadArgs{Path: "nope.go"}, "does not exist"},
		{"directory", ReadArgs{Path: "."}, "is a folder"},
		{"oversized", ReadArgs{Path: "huge.txt"}, "over the"},
		{"binary nul", ReadArgs{Path: "bin.dat"}, "binary"},
		{"binary invalid utf8", ReadArgs{Path: "bin2.dat"}, "binary"},
		{"unknown symbol", ReadArgs{Path: "big.go", Symbol: "Nope"}, "no function or type named"},
		{"symbol and range", ReadArgs{Path: "big.go", Symbol: "Fn1", Start: 1}, "either symbol or"},
		{"bad range", ReadArgs{Path: "big.go", Start: 9, End: 3}, "not valid"},
		{"range past end", ReadArgs{Path: "big.go", Start: 99999}, "only"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Read(r, tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %q / %v", tc.want, got, err)
			}
			if strings.Contains(got, "secret") {
				t.Fatal("leaked outside content")
			}
		})
	}
}

func TestReadSymlinkEscapeRefused(t *testing.T) {
	r, dir := newProject(t)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	_ = os.WriteFile(outside, []byte("secret"), 0o644)
	if err := os.Symlink(outside, filepath.Join(dir, "link.txt")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	got, err := Read(r, ReadArgs{Path: "link.txt"})
	if err == nil || strings.Contains(got, "secret") {
		t.Fatalf("symlink escape must be refused, got %q %v", got, err)
	}
}

// An agent's own deny rules (say, on .env) do not apply to MCP tools, so the
// tools must refuse secret-bearing files themselves, including through a link.
func TestReadRefusesSecretFiles(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ".env", "API_KEY=hunter2\n")
	write(t, dir, "deploy/server.pem", "-----BEGIN PRIVATE KEY-----\n")
	write(t, dir, ".env.example", "API_KEY=changeme\n")
	if err := os.Symlink(filepath.Join(dir, ".env"), filepath.Join(dir, "notes.txt")); err != nil {
		t.Logf("no symlinks here (%v); skipping the link case", err)
	}
	r, err := root.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{".env", "deploy/server.pem", "notes.txt"} {
		if _, err := os.Lstat(filepath.Join(dir, p)); err != nil {
			continue
		}
		got, err := Read(r, ReadArgs{Path: p})
		if err == nil || strings.Contains(got, "hunter2") || strings.Contains(got, "PRIVATE KEY") {
			t.Errorf("%s: read a secret file: %q, %v", p, got, err)
		}
	}
	if got, err := Read(r, ReadArgs{Path: ".env.example"}); err != nil || !strings.Contains(got, "changeme") {
		t.Errorf("templates should stay readable: %q, %v", got, err)
	}
}
