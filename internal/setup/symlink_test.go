package setup

import (
	"os"
	"path/filepath"
	"testing"
)

// A settings file that is a symbolic link (kept in a dotfiles repository, say) must stay a link after Trove
// writes to it, and the file it points to must be the one that changes (F-11).
func TestWritingThroughASymlinkKeepsTheLink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles", "settings.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create a symbolic link here: %v", err)
	}
	if err := writeAtomic(link, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link was replaced by a regular file: %v %v", fi, err)
	}
	if b, _ := os.ReadFile(target); string(b) != "new" {
		t.Fatalf("the file the link points to has %q", b)
	}
	if b, _ := os.ReadFile(link); string(b) != "new" {
		t.Fatalf("reading through the link gives %q", b)
	}
	// No temporary file is left next to either.
	for _, d := range []string{filepath.Dir(target), filepath.Dir(link)} {
		left, _ := filepath.Glob(filepath.Join(d, ".trove-*.tmp"))
		if len(left) != 0 {
			t.Errorf("leftover temporary files in %s: %v", d, left)
		}
	}
	// A plain file still works, and keeps its mode.
	plain := filepath.Join(dir, "plain.json")
	if err := os.WriteFile(plain, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(plain, []byte("b")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(plain); string(b) != "b" {
		t.Fatalf("%q", b)
	}
}
