package setup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPlanStatusLineAddsOnlyItsKeyAndUndoIsExact(t *testing.T) {
	claude, data := t.TempDir(), t.TempDir()
	path := filepath.Join(claude, "settings.json")
	orig := "{\n  \"model\": \"opus\"\n}\n"
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := PlanStatusLine(claude, "trove status-line --format claude")
	if err != nil || e.NoOp {
		t.Fatalf("plan: %v noop=%v", err, e != nil && e.NoOp)
	}
	if got, _ := os.ReadFile(path); string(got) != orig {
		t.Fatal("planning must not write anything")
	}
	if !strings.Contains(e.Show, `"type":"command"`) || !strings.Contains(string(e.Updated()), `"model": "opus"`) {
		t.Errorf("show %q, updated %s", e.Show, e.Updated())
	}
	if _, err := Apply(data, []*Edit{e}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := Undo(data); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != orig {
		t.Errorf("undo did not restore the file exactly:\n%s", got)
	}
}

func TestPlanStatusLineNeverReplaces(t *testing.T) {
	claude := t.TempDir()
	path := filepath.Join(claude, "settings.json")
	if err := os.WriteFile(path, []byte(`{"statusLine":{"type":"command","command":"~/mine.sh"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := PlanStatusLine(claude, "trove status-line --format claude")
	var exists *StatusLineExistsError
	if !errors.As(err, &exists) || exists.Current != "~/mine.sh" {
		t.Fatalf("err = %v", err)
	}
	e, err := PlanStatusLine(claude, "~/mine.sh")
	if err != nil || !e.NoOp {
		t.Errorf("an identical line is a no-op: %v", err)
	}
}

func TestUndoStatusLineLeavesYourEditsAlone(t *testing.T) {
	claude, data := t.TempDir(), t.TempDir()
	path := filepath.Join(claude, "settings.json")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e, _ := PlanStatusLine(claude, "trove status-line")
	if _, err := Apply(data, []*Edit{e}, time.Now()); err != nil {
		t.Fatal(err)
	}
	// The user then adds a key of their own.
	cur, _ := os.ReadFile(path)
	edited := strings.Replace(string(cur), "{", "{\n  \"theme\": \"dark\",", 1)
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Undo(data)
	if err != nil || len(res) != 1 || res[0].Action != "removed_our_lines" {
		t.Fatalf("undo = %+v, %v", res, err)
	}
	got, _ := os.ReadFile(path)
	if strings.Contains(string(got), "statusLine") || !strings.Contains(string(got), "theme") {
		t.Errorf("only our key should be gone:\n%s", got)
	}
}
