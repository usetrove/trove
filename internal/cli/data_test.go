package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/store"
)

// dataFixture fills a data folder the way real use does: a database with a row, hook
// state, the recovery store, backups of an agent file (which can hold keys), setup
// state and a consent record.
func dataFixture(t *testing.T, h *harness) string {
	t.Helper()
	dir, err := h.app.ResolveDataDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO hook_events (ts, session_key, hook, kind, bytes, tokens) VALUES (1700000000000, 'abc123', 'pre_tool_read', 'advised', 10, 3)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	files := map[string]string{
		"config.toml":             "retention_days = 30\n",
		"changes.json":            "[]",
		"backups/claude.json":     `{"apiKey":"SECRET-IN-A-BACKUP"}`,
		"recovery/aa/out.txt":     "RAW-COMMAND-OUTPUT",
		"hooks/config.json":       `{"hook_repeat_read":"shadow"}`,
		"hooks/capabilities.json": `{"verified":true}`,
		"hooks/events.jsonl":      `{"kind":"blocked"}`,
		"hooks/abc.log":           "journal of content hashes",
		"hooks.log":               "a redacted line\n",
		"contribute.json":         `{"enabled":true,"install_id":"0123456789abcdef0123456789abcdef"}`,
		"notes-from-the-user":     "not Trove's",
	}
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func walkAll(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			rel, _ := filepath.Rel(root, p)
			out[filepath.ToSlash(rel)] = string(b)
		}
		return nil
	})
	return out
}

func TestDataExportCopiesTablesAndOwnedFilesButNeverBackups(t *testing.T) {
	h := newHarness(t, nil)
	dir := dataFixture(t, h)
	out := filepath.Join(t.TempDir(), "export")
	if code := h.run("data", "export", "--out", out); code != 0 {
		t.Fatalf("exit %d: %s %s", code, h.out, h.err)
	}
	got := walkAll(t, out)
	if !strings.Contains(got["db/hook_events.csv"], "abc123") || !strings.Contains(got["db/hook_events.csv"], "pre_tool_read") {
		t.Errorf("the database row is not in db/hook_events.csv:\n%s", got["db/hook_events.csv"])
	}
	for _, tbl := range []string{"requests", "sessions", "receipts", "repo_tuning"} {
		if _, ok := got["db/"+tbl+".csv"]; !ok {
			t.Errorf("table %s was not exported", tbl)
		}
	}
	for _, want := range []string{"files/config.toml", "files/contribute.json", "files/hooks/config.json", "files/hooks.log", "manifest.json"} {
		if _, ok := got[want]; !ok {
			t.Errorf("%s missing from the export; have %v", want, keys(got))
		}
	}
	for name, body := range got {
		if strings.Contains(body, "SECRET-IN-A-BACKUP") || strings.HasPrefix(name, "files/backups") {
			t.Errorf("backups must never be exported, found %s", name)
		}
		if strings.Contains(body, "RAW-COMMAND-OUTPUT") || strings.HasPrefix(name, "files/recovery") {
			t.Errorf("the recovery store must not be exported without --include-recovery, found %s", name)
		}
		if strings.HasPrefix(name, "files/notes-from-the-user") {
			t.Errorf("a file that is not Trove's was exported: %s", name)
		}
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(out, "db", "hook_events.csv"))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("export files must be 0600, got %v (%v)", fi, err)
		}
	}
	// The source is untouched.
	if _, err := os.Stat(filepath.Join(dir, "trove.db")); err != nil {
		t.Errorf("export must not remove data: %v", err)
	}

	// With the flag, and refusing to overwrite.
	out2 := filepath.Join(t.TempDir(), "export2")
	if code := h.run("data", "export", "--out", out2, "--include-recovery"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if got2 := walkAll(t, out2); got2["files/recovery/aa/out.txt"] != "RAW-COMMAND-OUTPUT" {
		t.Errorf("--include-recovery did not copy the recovery store: %v", keys(got2))
	}
	if code := h.run("data", "export", "--out", out); code == 0 {
		t.Error("exporting into a non-empty folder must be refused")
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestDataDeleteRemovesUsageDataKeepsUndoStateAndPrintsTheSharedDataHint(t *testing.T) {
	h := newHarness(t, nil)
	dir := dataFixture(t, h)

	if code := h.run("data", "delete", "--dry-run"); code != 0 {
		t.Fatalf("dry run: exit %d: %s", code, h.err)
	}
	if _, err := os.Stat(filepath.Join(dir, "trove.db")); err != nil {
		t.Fatal("--dry-run deleted the database")
	}
	if !strings.Contains(h.out.String(), "curl -X DELETE") || !strings.Contains(h.out.String(), "0123456789abcdef0123456789abcdef") {
		t.Errorf("the plan must show how to ask the receiver to delete shared data:\n%s", h.out)
	}

	h.out.Reset()
	if code := h.run("data", "delete"); code == 0 {
		t.Error("without --yes and without a terminal nothing may be deleted")
	}
	if _, err := os.Stat(filepath.Join(dir, "trove.db")); err != nil {
		t.Fatal("an unconfirmed delete removed the database")
	}

	h.out.Reset()
	if code := h.run("data", "delete", "--yes"); code != 0 {
		t.Fatalf("exit %d: %s %s", code, h.out, h.err)
	}
	left := walkAll(t, dir)
	for name := range left {
		for _, gone := range []string{"trove.db", "hooks/events.jsonl", "hooks/abc.log", "hooks.log", "recovery/", "contribute.json"} {
			if strings.HasPrefix(name, gone) {
				t.Errorf("%s survived `trove data delete`", name)
			}
		}
	}
	// hooks/config.json is the optimizer-state snapshot the hooks read at every session start: deleting it would
	// quietly switch the hooks back to defaults.
	for _, kept := range []string{"config.toml", "changes.json", "backups/claude.json", "notes-from-the-user", "hooks/config.json", "hooks/capabilities.json"} {
		if _, ok := left[kept]; !ok {
			t.Errorf("%s must survive: `trove undo` and uninstall need it, or it is not Trove's", kept)
		}
	}
	if !strings.Contains(h.out.String(), "curl -X DELETE") {
		t.Errorf("the hint about shared data must still be printed after deleting the local copy:\n%s", h.out)
	}
	// Nothing is left to delete.
	h.out.Reset()
	if code := h.run("data", "delete", "--yes"); code != 0 || !strings.Contains(h.out.String(), "Nothing to delete") {
		t.Errorf("second run: exit %d, out %q", code, h.out)
	}
}

func TestDataDeleteJSON(t *testing.T) {
	h := newHarness(t, nil)
	dataFixture(t, h)
	if code := h.run("data", "delete", "--yes", "--json"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	var rep dataDeleteReport
	if err := json.Unmarshal(h.out.Bytes(), &rep); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, h.out)
	}
	if !rep.Applied || len(rep.Deleted) == 0 || rep.ContributedData == "" {
		t.Errorf("unexpected report: %+v", rep)
	}
}

func TestDataDeleteLeavesAForeignDatabase(t *testing.T) {
	h := newHarness(t, nil)
	dir, _ := h.app.ResolveDataDir()
	_ = os.MkdirAll(dir, 0o700)
	if err := os.WriteFile(filepath.Join(dir, "trove.db"), []byte("this is another program's file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := h.run("data", "delete", "--yes"); code != 0 {
		t.Fatalf("exit %d: %s %s", code, h.out, h.err)
	}
	if _, err := os.Stat(filepath.Join(dir, "trove.db")); err != nil {
		t.Error("a database that is not Trove's must never be deleted (see memory: the real ~/.trove is another product)")
	}
}
