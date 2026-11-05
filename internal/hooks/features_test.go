package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/store"
)

const featSentinel = "SENTINEL_HOOK_c0ffee"

// withCwd points a fixture's working directory at a real folder.
func (r *rig) withCwd(in string) string {
	j, _ := json.Marshal(r.files)
	return strings.Replace(in, `"/home/u/proj"`, string(j), 1)
}

func TestHooksRecordSessionFeaturesAndWriteThemAtSessionEnd(t *testing.T) {
	noClock(t)
	r := newRig(t, nil)
	r.start()
	if err := os.WriteFile(filepath.Join(r.files, "go.mod"), []byte("module "+featSentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	a := r.file(featSentinel+".go", "package a\n")
	b := r.file("b.go", "package b\n")
	read := func(p string) {
		if _, code := r.run(EventPostRead, r.withCwd(r.input("claude_post_read.json", "{{FILE}}", p))); code != 0 {
			t.Fatalf("post read: code %d", code)
		}
	}
	read(a)
	read(a) // a repeat
	read(b)
	edit := fmt.Sprintf(`{"session_id":%q,"cwd":%q,"hook_event_name":"PostToolUse","tool_name":"Edit","tool_input":{"file_path":%q,"old_string":%q}}`,
		sessionID, r.files, a, featSentinel)
	if out, code := r.run(EventPostEdit, edit); out != "" || code != 0 {
		t.Fatalf("post edit: out=%q code=%d", out, code)
	}
	failing := "ok  \tpkg\nerror: " + featSentinel + "\n"
	for i := 0; i < 2; i++ { // the same failing command twice: one retry
		if _, code := r.post("claude_post_bash.json", failing, false); code != 0 {
			t.Fatalf("post output: code %d", code)
		}
	}
	if _, code := r.run(EventReset, `{"session_id":"`+sessionID+`","hook_event_name":"PreCompact","cwd":"`+filepath.ToSlash(r.files)+`"}`); code != 0 {
		t.Fatalf("compact: code %d", code)
	}
	if _, code := r.run(EventSessionEnd, r.withCwd(r.input("claude_session_end.json"))); code != 0 {
		t.Fatalf("session end: code %d; log: %s", code, r.hookLog())
	}
	if _, err := os.Stat(featPath(r.dir, r.key())); !os.IsNotExist(err) {
		t.Errorf("the feature journal should be gone after the session ends: %v", err)
	}

	db, err := store.Open(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var s store.SessionFeaturesRow
	var repo string
	if err := db.QueryRowContext(context.Background(), `SELECT repo_key, files_read, files_edited, repeat_reads, compactions, retries,
		tool_results, error_share FROM session_features WHERE session_key = ?`, r.key()).
		Scan(&repo, &s.FilesRead, &s.FilesEdited, &s.RepeatReads, &s.Compactions, &s.Retries, &s.ToolResults, &s.ErrorShare); err != nil {
		t.Fatalf("no session_features row: %v; log: %s", err, r.hookLog())
	}
	if s.FilesRead != 2 || s.FilesEdited != 1 || s.RepeatReads != 1 || s.Compactions != 1 || s.Retries != 1 || s.ToolResults != 2 || s.ErrorShare != 1 || len(repo) != 16 {
		t.Errorf("features = %+v repo=%q", s, repo)
	}
	got, ok, err := db.Features().Repo(context.Background(), repo)
	if err != nil || !ok || got.TestFramework != "go_test" || got.SizeBucket != "S" {
		t.Errorf("repo features = %+v ok=%v err=%v", got, ok, err)
	}

	// Nothing from the content may reach the database, the journal or the hook log.
	for _, table := range []string{"session_features", "repo_features", "settings"} {
		rows, err := db.Query(`SELECT * FROM ` + table)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			_ = rows.Scan(ptrs...)
			if strings.Contains(fmt.Sprint(vals...), featSentinel) || strings.Contains(fmt.Sprint(vals...), r.files) {
				t.Errorf("%s row contains content: %v", table, vals)
			}
		}
		_ = rows.Close()
	}
}

func TestFeatureJournalHoldsNoContent(t *testing.T) {
	noClock(t)
	r := newRig(t, nil)
	r.start()
	p := r.file(featSentinel+".go", "x")
	r.run(EventPostRead, r.input("claude_post_read.json", "{{FILE}}", p))
	r.post("claude_post_bash.json", "error: "+featSentinel+"\n", false)
	b, err := os.ReadFile(featPath(r.dir, r.key()))
	if err != nil || len(b) == 0 {
		t.Fatalf("journal: %v %q", err, b)
	}
	if strings.Contains(string(b), featSentinel) || strings.Contains(string(b), r.files) || strings.Contains(string(b), "go test") {
		t.Errorf("the journal holds content:\n%s", b)
	}
}

func TestFeatureJournalIsBoundedAndSurvivesGarbage(t *testing.T) {
	r := newRig(t, nil)
	key := r.key()
	p := featPath(r.dir, key)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("garbage\nR zz\nT 1 2\nC x 1\nR 1f\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadFeatures(r.dir, key).Snapshot(); got.FilesRead != 1 {
		t.Errorf("only the valid line should count: %+v", got)
	}
	big := make([]byte, maxFeatBytes+1)
	for i := range big {
		big[i] = 'X'
	}
	if err := os.WriteFile(p, big, 0o600); err != nil {
		t.Fatal(err)
	}
	hh := &handler{dir: r.dir}
	hh.feat(key, "R 1")
	if fi, _ := os.Stat(p); fi.Size() != int64(len(big)) {
		t.Errorf("the journal grew past its cap: %d", fi.Size())
	}
}
