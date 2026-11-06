// Package privacy holds the end-to-end proof of CLAUDE.md rules 2 and 7: no prompt text, code, file path, repo
// name, ticket title, credential or other secret reaches Trove's database, its files or anything it prints
// for sharing. It drives the real flows with canary strings and then looks everywhere for them.
package privacy

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/cli"
	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/hooks"
	"github.com/usetrove/trove/internal/store"
)

// Each canary stands for one kind of content that must never be stored or sent.
var canaries = map[string]string{
	"prompt text":  "CANARY-PROMPT-7731",
	"repo name":    "canary-repo-9f3a",
	"ticket title": "TICKET-4711-refund-bug",
	"file path":    "canary-private-notes.go",
	"file content": "CANARY-FILE-CONTENT-5c1d",
	"credential":   "sk-ant-api03-CANARYKEY0123456789",
	"user name":    "alicecanary",
	"session id":   "alicecanary-session-id-0001",
}

type world struct {
	home, data, project, file string
	env                       map[string]string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	root := t.TempDir()
	w := &world{home: filepath.Join(root, "home-alicecanary")}
	w.data = filepath.Join(w.home, ".trove")
	w.project = filepath.Join(w.home, "code", canaries["repo name"])
	w.file = filepath.Join(w.project, canaries["file path"])
	w.env = map[string]string{"TROVE_DATA_DIR": w.data}
	for _, d := range []string{w.data, w.project} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(w.file, []byte("package a // "+canaries["file content"]+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *world) app(out, errw *bytes.Buffer) *cli.App {
	return &cli.App{
		Out: out, Err: errw,
		Getenv:   func(k string) string { return w.env[k] },
		LookPath: func(string) (string, error) { return "", errors.New("not found") },
		HomeDir:  func() (string, error) { return w.home, nil },
	}
}

// history writes a Claude Code session log whose every field carries a canary.
func (w *world) history(t *testing.T) {
	t.Helper()
	dir := filepath.Join(w.home, ".claude", "projects", "-home-alicecanary-code-"+canaries["repo name"])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ts := func(d time.Duration) string { return time.Now().UTC().Add(d).Format("2006-01-02T15:04:05.000Z") }
	usage := `"usage":{"input_tokens":10,"output_tokens":50,"cache_read_input_tokens":1000,"cache_creation_input_tokens":300,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":300},"service_tier":"standard"}`
	lines := []string{
		fmt.Sprintf(`{"type":"user","sessionId":%q,"timestamp":%q,"version":"2.1.302","isSidechain":false,"cwd":%q,"gitBranch":"feature/%s","message":{"role":"user","content":"%s please fix %s: see %s"}}`,
			canaries["session id"], ts(-3*time.Hour), w.project, canaries["ticket title"], canaries["prompt text"], canaries["ticket title"], w.file),
		fmt.Sprintf(`{"type":"assistant","sessionId":%q,"requestId":"req_A","timestamp":%q,"version":"2.1.302","isSidechain":false,"cwd":%q,"message":{"id":"msg_A","model":"claude-sonnet-5-5","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":%q}}],%s}}`,
			canaries["session id"], ts(-3*time.Hour+2*time.Second), w.project, w.file, usage),
		fmt.Sprintf(`{"type":"user","sessionId":%q,"timestamp":%q,"version":"2.1.302","isSidechain":false,"cwd":%q,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"%s and the key %s"}]}}`,
			canaries["session id"], ts(-3*time.Hour+3*time.Second), w.project, canaries["file content"], canaries["credential"]),
		fmt.Sprintf(`{"type":"assistant","sessionId":%q,"requestId":"req_B","timestamp":%q,"version":"2.1.302","isSidechain":false,"cwd":%q,"message":{"id":"msg_B","model":"claude-sonnet-5-5","content":[{"type":"text","text":"I changed %s for %s"}],%s}}`,
			canaries["session id"], ts(-3*time.Hour+5*time.Second), w.project, w.file, canaries["ticket title"], usage),
	}
	if err := os.WriteFile(filepath.Join(dir, "s1.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// hookSession runs a Claude Code session through Trove's hooks: start, the same read three times
// (the repeats are blocked), then the end-of-session receipt. Every payload carries canaries.
func (w *world) hookSession(t *testing.T) {
	t.Helper()
	if err := hooks.WriteSnapshot(w.data, hooks.Snapshot{States: map[string]string{config.OptimizerHookRepeatRead: "on"}, RolledBack: []string{}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	run := func(event, payload string) {
		t.Helper()
		var out bytes.Buffer
		code := hooks.Main(hooks.Env{Args: []string{event, "--data-dir", w.data}, Stdin: strings.NewReader(payload), Stdout: &out,
			Getenv: func(k string) string { return w.env[k] }, Now: func() time.Time { return now }})
		if code != 0 {
			t.Fatalf("hook %s exited %d", event, code)
		}
	}
	file, _ := json.Marshal(w.file)
	base := fmt.Sprintf(`"session_id":%q,"prompt_id":"%s","transcript_path":%q,"cwd":%q,"permission_mode":"default"`,
		canaries["session id"], canaries["prompt text"], filepath.Join(w.home, ".claude", "projects", "x", "s.jsonl"), w.project)
	run(hooks.EventReset, fmt.Sprintf(`{%s,"hook_event_name":"SessionStart","source":"startup"}`, base))
	for i := 0; i < 3; i++ {
		pre := fmt.Sprintf(`{%s,"hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":%s},"tool_use_id":"toolu_01ABC"}`, base, file)
		run(hooks.EventPreRead, pre)
		post := fmt.Sprintf(`{%s,"hook_event_name":"PostToolUse","tool_name":"Read","tool_input":{"file_path":%s},"tool_response":{"type":"text","text":"%s"},"tool_use_id":"toolu_01ABC"}`,
			base, file, canaries["file content"])
		run(hooks.EventPostRead, post)
	}
	run(hooks.EventSessionEnd, fmt.Sprintf(`{%s,"hook_event_name":"SessionEnd","reason":"other"}`, base))
}

// dbText returns every value of every column of every table as text, and the row count per table.
func dbText(t *testing.T, dir string) (string, map[string]int) {
	t.Helper()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tables = append(tables, n)
	}
	_ = rows.Close()
	var all strings.Builder
	counts := map[string]int{}
	for _, tbl := range tables {
		rs, err := db.Query(`SELECT * FROM "` + tbl + `"`)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rs.Columns()
		for rs.Next() {
			vals := make([]sql.RawBytes, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rs.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			counts[tbl]++
			for i, v := range vals {
				all.WriteString(tbl + "." + cols[i] + "=" + string(v) + "\n")
			}
		}
		_ = rs.Close()
	}
	return all.String(), counts
}

func dirText(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, _ := os.ReadFile(p)
		rel, _ := filepath.Rel(dir, p)
		out[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	return out
}

// containsCanary reports which canaries appear in s, in any of the forms Trove might have written it
// (as is, JSON-escaped for Windows paths, or lower-cased).
func containsCanary(s string) []string {
	var hit []string
	for kind, c := range canaries {
		esc, _ := json.Marshal(c)
		for _, form := range []string{c, strings.Trim(string(esc), `"`), strings.ToLower(c)} {
			if strings.Contains(s, form) || strings.Contains(strings.ToLower(s), strings.ToLower(form)) {
				hit = append(hit, kind+" ("+c+")")
				break
			}
		}
	}
	return hit
}

func TestNoContentReachesTheDatabaseFilesOrAnythingShared(t *testing.T) {
	w := newWorld(t)
	w.history(t)
	w.hookSession(t)

	run := func(args ...string) string {
		t.Helper()
		var out, errw bytes.Buffer
		if code := cli.Execute(w.app(&out, &errw), args); code != 0 {
			t.Fatalf("trove %s exited %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), code, out.String(), errw.String())
		}
		return out.String() + errw.String()
	}
	// `trove doctor` is a local diagnostic that prints the user's own data-folder path (it holds their
	// username). Everything else it prints is checked: mask only that path.
	doctor := strings.ReplaceAll(run("doctor"), w.home, "<HOME>")
	outputs := map[string]string{
		"report --json":   run("report", "--json"), // also reads the session history above
		"report":          run("report"),
		"contribute":      run("contribute"), // the exact text that would be sent
		"feedback":        run("feedback"),
		"doctor":          doctor,
		"logs":            run("logs"),
		"receipts":        run("receipts"),
		"recommend":       run("recommend", "--repo", w.project),
		"tune":            run("tune", "--repo", w.project),
		"contribute json": run("contribute", "--json"),
	}
	export := filepath.Join(t.TempDir(), "export")
	outputs["data export"] = run("data", "export", "--out", export)

	// The test must prove something: the flows really did write rows.
	text, counts := dbText(t, w.data)
	for _, tbl := range []string{"history_requests", "hook_events", "session_features"} {
		if counts[tbl] == 0 {
			t.Errorf("%s has no rows, so the scan below proves nothing for that flow (counts: %v)", tbl, counts)
		}
	}

	check := func(where, s string) {
		t.Helper()
		if hits := containsCanary(s); len(hits) > 0 {
			t.Errorf("%s contains %s", where, strings.Join(hits, ", "))
		}
	}
	check("the database", text)
	for name, body := range dirText(t, w.data) {
		if strings.HasPrefix(name, "trove.db") { // checked row by row above; the raw file also holds free pages
			continue
		}
		check("data folder file "+name, body)
	}
	for name, body := range outputs {
		check("the output of `trove "+name+"`", body)
	}
	for name, body := range dirText(t, export) {
		check("the exported file "+name, body)
	}
}

// The raw database file, including pages freed by deletes and the write-ahead log, must not hold content either.
func TestRawDatabaseFilesHoldNoContent(t *testing.T) {
	w := newWorld(t)
	w.history(t)
	w.hookSession(t)
	var out, errw bytes.Buffer
	if code := cli.Execute(w.app(&out, &errw), []string{"report", "--json"}); code != 0 {
		t.Fatalf("report exited %d: %s", code, errw.String())
	}
	for name, body := range dirText(t, w.data) {
		if !strings.HasPrefix(name, "trove.db") {
			continue
		}
		if hits := containsCanary(body); len(hits) > 0 {
			t.Errorf("%s contains %s", name, strings.Join(hits, ", "))
		}
	}
}

// A scan that cannot fail proves nothing: the detector must see every canary in every form it could be stored.
func TestTheCanaryDetectorSeesEveryFormAndNoFalseAlarms(t *testing.T) {
	for kind, c := range canaries {
		esc, _ := json.Marshal(`C:\Users\` + c)
		for name, text := range map[string]string{
			"plain":   "prefix " + c + " suffix",
			"escaped": "path " + strings.Trim(string(esc), `"`),
			"lower":   strings.ToLower(c),
			"upper":   strings.ToUpper(c),
		} {
			if len(containsCanary(text)) == 0 {
				t.Errorf("the %s canary (%s) is not detected in its %s form", kind, c, name)
			}
		}
	}
	if hits := containsCanary(`{"requests_bucket":"20-99","agents":["claude-code"],"waste_percent":{"percent":12,"label":"estimated"}}`); len(hits) != 0 {
		t.Errorf("false alarm on a clean payload: %v", hits)
	}
}
