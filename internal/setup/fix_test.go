package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixEdit(t *testing.T, kind, id, path, newBody string) *Edit {
	t.Helper()
	old, err := readOptional(path)
	if err != nil {
		t.Fatal(err)
	}
	return NewFixEdit(kind, id, path, "", "test", old, []byte(newBody))
}

func TestFixApplyAndExactUndoByID(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	a, b := filepath.Join(home, "A.md"), filepath.Join(home, "B.md")
	origA := "line 1\r\nline 2 no trailing newline"
	origB := "b\n"
	write(t, a, origA)
	write(t, b, origB)

	cs, err := Apply(dir, []*Edit{fixEdit(t, "context_slim", "fix-a", a, "line 1\r\n"), fixEdit(t, "cache_breaker_fix", "fix-b", b, "")}, t0)
	if err != nil || len(cs) != 2 {
		t.Fatalf("apply: %v %v", cs, err)
	}
	if cs[0].FixID != "fix-a" || !IsFix(cs[0].Agent) {
		t.Fatalf("change not marked as a fix: %+v", cs[0])
	}
	if cs[0].ID == cs[1].ID {
		t.Fatalf("change ids must be unique")
	}

	res, err := UndoSelected(dir, []string{"fix-a"})
	if err != nil || len(res) != 1 || res[0].Action != "restored" {
		t.Fatalf("undo fix-a: %+v %v", res, err)
	}
	if read(t, a) != origA {
		t.Fatalf("A not byte-identical: %q", read(t, a))
	}
	if read(t, b) != "" {
		t.Fatalf("B must be untouched by undoing A")
	}
	left, _ := LoadChanges(dir)
	if len(left) != 1 || left[0].FixID != "fix-b" {
		t.Fatalf("manifest after partial undo: %+v", left)
	}

	if _, err := UndoSelected(dir, nil); err != nil {
		t.Fatal(err)
	}
	if read(t, b) != origB {
		t.Fatalf("B not byte-identical")
	}
	if left, _ := LoadChanges(dir); len(left) != 0 {
		t.Fatalf("manifest should be empty: %+v", left)
	}
}

func TestFixUndoRefusesEditedFileAndKeepsRecord(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	p := filepath.Join(home, "CLAUDE.md")
	write(t, p, "keep\nremove me\n")
	if _, err := Apply(dir, []*Edit{fixEdit(t, "context_slim", "fix-x", p, "keep\n")}, t0); err != nil {
		t.Fatal(err)
	}
	write(t, p, "keep\nmy own addition\n")
	res, err := UndoSelected(dir, []string{"fix-x"})
	if err != nil || len(res) != 1 || res[0].Action != "left_alone" {
		t.Fatalf("want left_alone, got %+v %v", res, err)
	}
	if read(t, p) != "keep\nmy own addition\n" {
		t.Fatal("an edited file must not be touched")
	}
	if left, _ := LoadChanges(dir); len(left) != 1 {
		t.Fatalf("the record must stay so the backup can still be found: %+v", left)
	}
}

func TestUndoUnknownIDChangesNothing(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	p := filepath.Join(home, "A.md")
	write(t, p, "x\n")
	if _, err := Apply(dir, []*Edit{fixEdit(t, "context_slim", "fix-a", p, "")}, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := UndoSelected(dir, []string{"nope"}); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("want an error naming the id, got %v", err)
	}
	if read(t, p) != "" {
		t.Fatal("a bad id must not undo anything")
	}
}

func TestApplyRefusesWhenFileChangedSincePlanned(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	p := filepath.Join(home, "A.md")
	write(t, p, "planned\n")
	e := fixEdit(t, "context_slim", "fix-a", p, "")
	write(t, p, "someone edited this\n")
	if _, err := Apply(dir, []*Edit{e}, t0); err == nil || !strings.Contains(err.Error(), "changed after") {
		t.Fatalf("want a staleness error, got %v", err)
	}
	if read(t, p) != "someone edited this\n" {
		t.Fatal("file must be untouched")
	}
	if _, err := os.Stat(filepath.Join(dir, "backups")); err == nil {
		entries, _ := os.ReadDir(filepath.Join(dir, "backups"))
		if len(entries) != 0 {
			t.Fatalf("no backup should be written for a refused edit: %v", entries)
		}
	}
}

func TestSameInstantFixesGetDistinctIDs(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	p1, p2 := filepath.Join(home, "1.md"), filepath.Join(home, "2.md")
	write(t, p1, "1\n")
	write(t, p2, "2\n")
	now := time.Date(2026, 10, 29, 1, 2, 3, 4, time.UTC)
	cs, err := Apply(dir, []*Edit{fixEdit(t, "context_slim", "a", p1, ""), fixEdit(t, "context_slim", "b", p2, "")}, now)
	if err != nil || cs[0].ID == cs[1].ID {
		t.Fatalf("ids: %+v %v", cs, err)
	}
}
