package root

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fixture builds a project with a file inside and a secret outside it.
func fixture(t *testing.T) (proj, outside string) {
	t.Helper()
	base := t.TempDir()
	proj = filepath.Join(base, "proj")
	outside = filepath.Join(base, "outside")
	for _, d := range []string{filepath.Join(proj, "sub"), outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	must(t, os.WriteFile(filepath.Join(proj, "a.txt"), []byte("hi"), 0o644))
	must(t, os.WriteFile(filepath.Join(proj, "sub", "b.txt"), []byte("hi"), 0o644))
	must(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644))
	return proj, outside
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestResolveAllowed(t *testing.T) {
	proj, _ := fixture(t)
	r, err := New(proj)
	must(t, err)
	for _, rel := range []string{"a.txt", "sub/b.txt", "./sub/../a.txt", "sub"} {
		if _, err := r.Resolve(rel); err != nil {
			t.Errorf("%q should be allowed: %v", rel, err)
		}
	}
}

func TestResolveTraversalRefused(t *testing.T) {
	proj, outside := fixture(t)
	r, _ := New(proj)
	abs := filepath.Join(outside, "secret.txt")
	for _, rel := range []string{
		"../outside/secret.txt", "..", "sub/../../outside/secret.txt",
		abs, "/etc/passwd", `\windows\win.ini`, `C:\Windows\win.ini`, "a.txt\x00.png",
	} {
		if _, err := r.Resolve(rel); err == nil {
			t.Errorf("%q must be refused", rel)
		}
	}
	if _, err := r.Resolve("../outside/secret.txt"); !errors.Is(err, ErrOutside) {
		t.Errorf("want ErrOutside, got %v", err)
	}
}

func TestSymlinkEscapeRefused(t *testing.T) {
	proj, outside := fixture(t)
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(proj, "link.txt")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	must(t, os.Symlink(outside, filepath.Join(proj, "linkdir")))
	must(t, os.Symlink("a.txt", filepath.Join(proj, "inner.txt")))
	r, _ := New(proj)
	for _, rel := range []string{"link.txt", "linkdir/secret.txt", "linkdir"} {
		if _, err := r.Resolve(rel); !errors.Is(err, ErrOutside) {
			t.Errorf("%q: want ErrOutside, got %v", rel, err)
		}
		if f, _, err := r.OpenRead(rel); err == nil {
			_ = f.Close()
			t.Errorf("%q: OpenRead must fail", rel)
		}
	}
	if _, err := r.Resolve("inner.txt"); err != nil {
		t.Errorf("a symlink that stays inside is fine: %v", err)
	}
}

func TestMissingFile(t *testing.T) {
	proj, _ := fixture(t)
	r, _ := New(proj)
	if _, err := r.Resolve("nope.txt"); err == nil || errors.Is(err, ErrOutside) {
		t.Fatalf("want not-exist error, got %v", err)
	}
}

func TestOpenReadAndRel(t *testing.T) {
	proj, _ := fixture(t)
	r, _ := New(proj)
	f, fi, err := r.OpenRead("sub/b.txt")
	must(t, err)
	defer func() { _ = f.Close() }()
	if fi.Size() != 2 {
		t.Fatal("wrong size")
	}
	target, _ := r.Resolve("sub/b.txt")
	if got := r.Rel(target); got != "sub/b.txt" {
		t.Fatalf("rel %q", got)
	}
}
