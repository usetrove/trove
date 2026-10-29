package fixer

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/setup"
	"github.com/usetrove/trove/internal/store"
)

var update = flag.Bool("update", false, "rewrite golden files")

var now = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

type fx struct {
	t    *testing.T
	env  *Env
	proj string
}

// newFx builds a fake home, project and data dir. Nothing touches the real home
// or the real ~/.trove.
func newFx(t *testing.T) *fx {
	t.Helper()
	home := t.TempDir()
	proj := filepath.Join(home, "work", "demo")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default().Fixer
	cfg.ContextMinTokens = 100
	e := &Env{
		Home: home, ProjectDir: proj, ClaudeDir: filepath.Join(home, ".claude"), CodexDir: filepath.Join(home, ".codex"),
		DataDir: filepath.Join(home, ".trove"), Getenv: func(string) string { return "" }, Now: now, Cfg: cfg,
	}
	return &fx{t: t, env: e, proj: proj}
}

func (f *fx) write(rel, body string) string {
	f.t.Helper()
	p := filepath.Join(f.proj, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
	return p
}

func (f *fx) read(p string) string {
	f.t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		f.t.Fatal(err)
	}
	return string(b)
}

// sessions writes n Claude Code sessions for the project; tools[i%len] is the
// tool each session calls. mtime sets how old the history files look.
func (f *fx) sessions(n int, tools []string, mtime time.Time) {
	f.t.Helper()
	dir := filepath.Join(f.env.ClaudeDir, "projects", encodeProject(f.proj))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("sess-%d", i)
		ts := func(s int) string {
			return time.Date(2026, 10, 20, 10, i, s, 0, time.UTC).Format("2006-01-02T15:04:05.000Z")
		}
		usage := `"usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":100,"cache_creation_input_tokens":0}`
		var b strings.Builder
		fmt.Fprintf(&b, `{"type":"user","sessionId":%q,"timestamp":%q,"message":{"role":"user","content":"hi"}}`+"\n", id, ts(0))
		fmt.Fprintf(&b, `{"type":"assistant","sessionId":%q,"requestId":"req_%d_a","timestamp":%q,"message":{"id":"m%da","model":"claude-opus-5-5","content":[{"type":"tool_use","id":"tu%d","name":%q,"input":{}}],%s}}`+"\n",
			id, i, ts(1), i, i, tools[i%len(tools)], usage)
		fmt.Fprintf(&b, `{"type":"user","sessionId":%q,"timestamp":%q,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu%d","content":"ok"}]}}`+"\n", id, ts(2), i)
		fmt.Fprintf(&b, `{"type":"assistant","sessionId":%q,"requestId":"req_%d_b","timestamp":%q,"message":{"id":"m%db","model":"claude-opus-5-5","content":[{"type":"text","text":"done"}],%s}}`+"\n", id, i, ts(3), i, usage)
		p := filepath.Join(dir, id+".jsonl")
		if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
			f.t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *fx) idle() time.Time { return now.Add(-72 * time.Hour) }

func (f *fx) detect(kind string) []*Fix {
	f.t.Helper()
	f.env.usage = nil
	fixes, errs := Detect(context.Background(), f.env)
	if len(errs) > 0 {
		f.t.Fatal(errs)
	}
	var out []*Fix
	for _, x := range fixes {
		if x.Kind == kind {
			out = append(out, x)
		}
	}
	return out
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", name)
	got = strings.ReplaceAll(got, "\r\n", "\n")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("missing golden file %s (run go test -update): %v", p, err)
	}
	if strings.ReplaceAll(string(want), "\r\n", "\n") != got {
		t.Fatalf("%s differs:\n--- got\n%s\n--- want\n%s", p, got, want)
	}
}

const mcpJSON = "{\n  \"mcpServers\": {\n    \"github\": {\"command\": \"gh-mcp\"},\n    \"notion\": {\"type\": \"http\", \"url\": \"https://example.com/mcp\"}\n  }\n}\n"

func TestMCPPrune(t *testing.T) {
	f := newFx(t)
	f.write(".mcp.json", mcpJSON)
	f.sessions(6, []string{"Read", "mcp__github__create_issue"}, f.idle())

	fixes := f.detect(MCPPrune)
	if len(fixes) != 1 {
		t.Fatalf("want one fix (notion), got %d", len(fixes))
	}
	x := fixes[0]
	if !strings.Contains(x.Title, "notion") || !x.Applicable() || x.Label != "estimated" {
		t.Fatalf("unexpected fix: %+v", x)
	}
	if x.TokensPerSession != 0 || x.USDPerSession != nil {
		t.Fatalf("with no gateway data there must be no saving figure, got %d %v", x.TokensPerSession, x.USDPerSession)
	}
	if !strings.Contains(strings.Join(x.Notes, " "), "no gateway data") {
		t.Fatalf("the missing estimate must be explained: %v", x.Notes)
	}
	golden(t, "mcp_prune_new_local.diff.golden", x.Diff)

	// An existing settings.local.json keeps its formatting and other keys.
	f.write(".claude/settings.local.json", "{\n  \"permissions\": {\"allow\": []},\n  \"disabledMcpjsonServers\": [\n    \"old\"\n  ]\n}\n")
	golden(t, "mcp_prune_existing_local.diff.golden", f.detect(MCPPrune)[0].Diff)
}

func TestMCPPruneNeedsEnoughHistory(t *testing.T) {
	f := newFx(t)
	f.write(".mcp.json", mcpJSON)
	f.sessions(4, []string{"Read"}, f.idle()) // below min_sessions (5)
	if got := f.detect(MCPPrune); len(got) != 0 {
		t.Fatalf("4 sessions is not enough to call a server unused: %+v", got)
	}
	// No history folder for this project at all.
	g := newFx(t)
	g.write(".mcp.json", mcpJSON)
	if got := g.detect(MCPPrune); len(got) != 0 {
		t.Fatalf("no history, no fix: %+v", got)
	}
}

func TestMCPPruneSkipsServersUsedOrAlreadyDisabled(t *testing.T) {
	f := newFx(t)
	f.write(".mcp.json", mcpJSON)
	f.write(".claude/settings.json", `{"disabledMcpjsonServers": ["notion"]}`)
	f.sessions(6, []string{"Read"}, f.idle())
	fixes := f.detect(MCPPrune)
	if len(fixes) != 1 || !strings.Contains(fixes[0].Title, "github") {
		t.Fatalf("only github is unused and enabled, got %+v", fixes)
	}
}

const readme = `# Demo

The demo service reads events from the queue and writes summaries to the database nightly.
Configuration lives in config.yaml and every key has a default documented below in this file.
Deployments are done through the release pipeline which tags the repository and publishes artifacts.
Run the migrations before starting the service for the first time on a new machine or database.
`

func claudeMD() string {
	return "# Demo project\n\n" +
		"## Overview\n\nThis is a demo project. It is a service that does useful things for people.\n\n" +
		"## Deployments\n\n" +
		"The demo service reads events from the queue and writes summaries to the database nightly.\n" +
		"Configuration lives in config.yaml and every key has a default documented below in this file.\n" +
		"Deployments are done through the release pipeline which tags the repository and publishes artifacts.\n" +
		"Run the migrations before starting the service for the first time on a new machine or database.\n\n" +
		"## Rules\n\nNever edit generated files under gen/. Always run `make check` before committing.\n\n" +
		"## Commands\n\n```\nmake check\nmake test\n```\n"
}

func TestContextSlim(t *testing.T) {
	f := newFx(t)
	f.write("README.md", readme)
	f.write("CLAUDE.md", claudeMD())
	fixes := f.detect(ContextSlim)
	if len(fixes) != 1 {
		t.Fatalf("want one fix, got %d", len(fixes))
	}
	x := fixes[0]
	out := string(x.updated)
	for _, keep := range []string{"# Demo project", "## Rules", "Never edit generated files", "## Commands", "make check"} {
		if !strings.Contains(out, keep) {
			t.Errorf("must keep %q", keep)
		}
	}
	for _, gone := range []string{"## Overview", "## Deployments"} {
		if strings.Contains(out, gone) {
			t.Errorf("must remove %q", gone)
		}
	}
	if x.TokensPerSession <= 0 {
		t.Fatalf("a removal has a token saving, got %d", x.TokensPerSession)
	}
	golden(t, "context_slim.diff.golden", x.Diff)
}

func TestContextSlimLeavesShortAndRuleFilesAlone(t *testing.T) {
	f := newFx(t)
	f.write("CLAUDE.md", "# Demo\n\n## Rules\n\nNever do the thing.\n")
	if got := f.detect(ContextSlim); len(got) != 0 {
		t.Fatalf("a short file needs no fix: %+v", got)
	}
	// A big file of only project-specific rules is left alone.
	var b strings.Builder
	b.WriteString("# Demo\n\n")
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&b, "## Rule %d\n\nNever change the schema version %d without a migration and a reviewer's sign-off.\n\n", i, i)
	}
	f.write("CLAUDE.md", b.String())
	if got := f.detect(ContextSlim); len(got) != 0 {
		t.Fatalf("project rules must be kept: %+v", got)
	}
}

func TestCacheBreakerFix(t *testing.T) {
	f := newFx(t)
	f.write("CLAUDE.md", "# Demo\n\nToday's date: 2026-10-29\nLast updated: 2026-10-28 by the build script\nWe chose Postgres on 2026-03-01 after the review.\n\n```\nnow: 2026-10-29\n```\n\nNever edit gen/.\n")
	fixes := f.detect(CacheBreaker)
	if len(fixes) != 1 {
		t.Fatalf("want one fix, got %d", len(fixes))
	}
	out := string(fixes[0].updated)
	if strings.Contains(out, "Today's date") || strings.Contains(out, "Last updated") {
		t.Fatalf("changing lines must go: %q", out)
	}
	for _, keep := range []string{"We chose Postgres on 2026-03-01", "now: 2026-10-29", "Never edit gen/."} {
		if !strings.Contains(out, keep) {
			t.Fatalf("must keep %q (a fixed date in prose or in a code block does not change)", keep)
		}
	}
	golden(t, "cache_breaker.diff.golden", fixes[0].Diff)
}

func TestModelDefaultIsSuggestionOnly(t *testing.T) {
	f := newFx(t)
	db, err := store.Open(f.env.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	finding := `[{"detector":"expensive_model_small_task","tokens":100,"detail":{"alternative":"claude-sonnet-5-5"}}]`
	for i := 0; i < 30; i++ {
		fin := any(nil)
		if i < 25 {
			fin = finding
		}
		if _, err := db.Exec(`INSERT INTO history_requests (request_key, source, session_key, ts, model, findings) VALUES (?, 'claude_code', 's', ?, 'claude-opus-5-5', ?)`,
			fmt.Sprintf("k%d", i), now.Add(-24*time.Hour).UnixMilli(), fin); err != nil {
			t.Fatal(err)
		}
	}
	f.env.DB = db.DB
	if err := os.MkdirAll(f.env.ClaudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(f.env.ClaudeDir, "settings.json")
	if err := os.WriteFile(settings, []byte("{\n  \"theme\": \"dark\"\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixes := f.detect(ModelDefault)
	if len(fixes) != 1 || !fixes[0].Suggestion || !fixes[0].Applicable() {
		t.Fatalf("want one applicable suggestion, got %+v", fixes)
	}
	if fixes[0].USDPerSession != nil || fixes[0].TokensPerSession != 0 {
		t.Fatalf("no honest saving exists for a model change, got %+v", fixes[0])
	}
	golden(t, "model_default.diff.golden", fixes[0].Diff)

	// ANTHROPIC_MODEL wins over the file, so the edit would do nothing.
	f.env.Getenv = func(k string) string {
		if k == "ANTHROPIC_MODEL" {
			return "claude-opus-5-5"
		}
		return ""
	}
	fixes = f.detect(ModelDefault)
	if len(fixes) != 1 || fixes[0].Applicable() || !strings.Contains(fixes[0].Advisory, "ANTHROPIC_MODEL") {
		t.Fatalf("an environment override must turn the fix into advice: %+v", fixes)
	}
}

func TestAllowedRefusesManagedCredentialAndStrayFiles(t *testing.T) {
	f := newFx(t)
	e := f.env
	ok := []string{
		filepath.Join(f.proj, "CLAUDE.md"), filepath.Join(f.proj, ".claude", "settings.local.json"),
		filepath.Join(f.proj, ".claude", "rules", "x.md"), filepath.Join(e.ClaudeDir, "settings.json"), filepath.Join(e.CodexDir, "config.toml"),
	}
	for _, p := range ok {
		if err := e.Allowed(p); err != nil {
			t.Errorf("%s should be allowed: %v", p, err)
		}
	}
	bad := map[string]string{
		"/Library/Application Support/ClaudeCode/CLAUDE.md": "managed",
		"/etc/claude-code/managed-settings.json":            "managed",
		`C:\Program Files\ClaudeCode\managed-mcp.json`:      "managed",
		"/etc/codex/config.toml":                            "managed",
		filepath.Join(e.ClaudeDir, ".credentials.json"):     "credentials",
		filepath.Join(e.Home, ".claude.json"):               "credentials",
		filepath.Join(e.CodexDir, "auth.json"):              "credentials",
		filepath.Join(f.proj, ".env"):                       "credentials",
		filepath.Join(f.proj, "main.go"):                    "not one of",
		filepath.Join(f.proj, "managed-settings.json"):      "managed",
		"relative/CLAUDE.md":                                "absolute",
	}
	for p, want := range bad {
		if err := e.Allowed(p); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want a refusal mentioning %q, got %v", p, want, err)
		}
	}
}

func TestAllowedRefusesSymlinkOutsideAllowedSet(t *testing.T) {
	f := newFx(t)
	target := filepath.Join(f.env.Home, "elsewhere.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(f.proj, "CLAUDE.md")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	if err := f.env.Allowed(link); err == nil {
		t.Fatal("a CLAUDE.md that links outside the allowed files must be refused")
	}
}

// applyAll applies every non-suggestion applicable fix.
func (f *fx) applyAll() []Result {
	f.t.Helper()
	fixes := f.detect("")
	_ = fixes
	all, _ := Detect(context.Background(), f.env)
	var ids []string
	for _, x := range all {
		if x.Applicable() && !x.Suggestion {
			ids = append(ids, x.ID)
		}
	}
	res, err := Apply(context.Background(), f.env, ids)
	if err != nil {
		f.t.Fatal(err)
	}
	return res
}

func TestApplyThenUndoLeavesFilesByteIdentical(t *testing.T) {
	f := newFx(t)
	f.write(".mcp.json", mcpJSON)
	f.sessions(6, []string{"Read", "mcp__github__x"}, f.idle())
	f.write("README.md", readme)
	// CRLF and no trailing newline must survive the round trip.
	claude := strings.ReplaceAll(claudeMD(), "\n", "\r\n") + "Today's date: 2026-10-29"
	cp := f.write("CLAUDE.md", claude)
	localPath := filepath.Join(f.proj, ".claude", "settings.local.json")
	f.write(".claude/settings.local.json", "{\r\n  \"x\": 1\r\n}")
	before := map[string]string{cp: f.read(cp), localPath: f.read(localPath)}

	res := f.applyAll()
	applied := 0
	for _, r := range res {
		if r.Status == "applied" {
			applied++
		}
	}
	if applied != 3 {
		t.Fatalf("want 3 applied (mcp, ctx, cache), got %+v", res)
	}
	if f.read(cp) == before[cp] || f.read(localPath) == before[localPath] {
		t.Fatal("files should have changed")
	}
	if strings.Contains(f.read(cp), "Today's date") {
		t.Fatal("both fixes to CLAUDE.md must apply, one after the other")
	}
	if _, err := os.Stat(filepath.Join(f.env.DataDir, "backups")); err != nil {
		t.Fatal("backups folder missing")
	}

	if _, err := setup.Undo(f.env.DataDir); err != nil {
		t.Fatal(err)
	}
	for p, want := range before {
		if got := f.read(p); got != want {
			t.Fatalf("%s not byte-identical after undo:\n got %q\nwant %q", p, got, want)
		}
	}
}

func TestUndoRemovesFileTheFixCreated(t *testing.T) {
	f := newFx(t)
	f.write(".mcp.json", mcpJSON)
	f.sessions(6, []string{"Read"}, f.idle())
	local := filepath.Join(f.proj, ".claude", "settings.local.json")
	res := f.applyAll()
	if len(res) == 0 || res[0].Status != "applied" {
		t.Fatalf("%+v", res)
	}
	if _, err := os.Stat(local); err != nil {
		t.Fatal("the fix should have created settings.local.json")
	}
	if _, err := setup.Undo(f.env.DataDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(local); !os.IsNotExist(err) {
		t.Fatal("undo must remove a file the fix created")
	}
}

func TestNothingChangesDuringActiveSession(t *testing.T) {
	f := newFx(t)
	f.write("README.md", readme)
	cp := f.write("CLAUDE.md", claudeMD())
	f.sessions(6, []string{"Read"}, now.Add(-5*time.Minute)) // a session wrote history 5 minutes ago
	before := f.read(cp)
	all, _ := Detect(context.Background(), f.env)
	x := Find(all, fixID(ContextSlim, cp))
	if x == nil {
		t.Fatal("expected a context_slim fix")
	}

	res, err := Apply(context.Background(), f.env, []string{x.ID})
	if err != nil || len(res) != 1 || res[0].Status != "queued" {
		t.Fatalf("want queued, got %+v %v", res, err)
	}
	if f.read(cp) != before {
		t.Fatal("a file changed during an active session")
	}
	if _, err := os.Stat(filepath.Join(f.env.DataDir, "changes.json")); err == nil {
		t.Fatal("nothing should be recorded as changed")
	}
	q, _ := LoadQueue(f.env.DataDir)
	if len(q) != 1 || q[0].ID != x.ID {
		t.Fatalf("queue: %+v", q)
	}
	if b, _ := os.ReadFile(filepath.Join(f.env.DataDir, queueFile)); strings.Contains(string(b), "Overview") || strings.Contains(string(b), f.proj) {
		t.Fatal("the queue must hold ids and hashes only")
	}

	// Still active: stays queued.
	res, _ = ApplyQueued(context.Background(), f.env)
	if len(res) != 1 || res[0].Status != "queued" || f.read(cp) != before {
		t.Fatalf("still active: %+v", res)
	}
	// Idle later: applied.
	f.env.Now = now.Add(2 * time.Hour)
	res, err = ApplyQueued(context.Background(), f.env)
	if err != nil || len(res) != 1 || res[0].Status != "applied" || f.read(cp) == before {
		t.Fatalf("idle: %+v %v", res, err)
	}
	if q, _ := LoadQueue(f.env.DataDir); len(q) != 0 {
		t.Fatalf("queue should be empty: %+v", q)
	}
}

func TestQueuedFixIsDroppedIfFileChanged(t *testing.T) {
	f := newFx(t)
	f.write("README.md", readme)
	cp := f.write("CLAUDE.md", claudeMD())
	f.sessions(6, []string{"Read"}, now.Add(-5*time.Minute))
	all, _ := Detect(context.Background(), f.env)
	x := Find(all, fixID(ContextSlim, cp))
	if _, err := Apply(context.Background(), f.env, []string{x.ID}); err != nil {
		t.Fatal(err)
	}
	edited := f.read(cp) + "\nNever forget the tests.\n"
	f.write("CLAUDE.md", edited)
	f.env.Now = now.Add(2 * time.Hour)
	res, _ := ApplyQueued(context.Background(), f.env)
	if len(res) != 1 || res[0].Status != "skipped" || f.read(cp) != edited {
		t.Fatalf("a changed file must drop the queued fix and stay untouched: %+v", res)
	}
}

func TestManagedFileIsNeverApplied(t *testing.T) {
	f := newFx(t)
	f.env.ClaudeDir = filepath.Join(f.env.Home, "etc", "claude-code") // looks like the Linux managed folder
	f.env.ProjectDir = f.proj
	settings := filepath.Join(f.env.ClaudeDir, "managed-settings.json")
	if err := os.MkdirAll(f.env.ClaudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.env.Allowed(settings); err == nil {
		t.Fatal("managed-settings.json must be refused")
	}
	x := &Fix{ID: "t", Path: settings}
	x.updated = []byte("{\"model\": \"x\"}\n")
	if err := f.env.Allowed(x.Path); err == nil {
		t.Fatal("refused")
	}
}

func TestAppliedFixIsRegisteredWithoutPathsOrText(t *testing.T) {
	f := newFx(t)
	f.write("README.md", readme)
	f.write("CLAUDE.md", claudeMD())
	f.sessions(6, []string{"Read"}, f.idle())
	res := f.applyAll()
	if len(res) == 0 || res[0].Status != "applied" {
		t.Fatalf("%+v", res)
	}
	db, err := store.Open(f.env.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query(`SELECT change_id, fix_id, kind, agent, path_hash, applied_at, reverted_at, est_tokens, label FROM fixes`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	n := 0
	for rows.Next() {
		var cid, fid, kind, agent, ph, label string
		var at, tok int64
		var rev any
		if err := rows.Scan(&cid, &fid, &kind, &agent, &ph, &at, &rev, &tok, &label); err != nil {
			t.Fatal(err)
		}
		n++
		dump := strings.Join([]string{cid, fid, kind, agent, ph, label}, " ")
		if strings.Contains(dump, "demo") || strings.Contains(dump, "Overview") || strings.Contains(dump, f.env.Home) {
			t.Fatalf("a path or text leaked into the fixes table: %s", dump)
		}
		if label != "estimated" || rev != nil || at != now.UnixMilli() {
			t.Fatalf("row: %s %v %d", label, rev, at)
		}
	}
	if n == 0 {
		t.Fatal("applied fix was not registered")
	}
	if err := MarkReverted(f.env.DataDir, []setup.UndoResult{{FixID: res[0].ID, Action: "restored"}}, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var rev int64
	if err := db.QueryRow(`SELECT reverted_at FROM fixes WHERE fix_id = ?`, res[0].ID).Scan(&rev); err != nil || rev != now.Add(time.Hour).UnixMilli() {
		t.Fatalf("reverted_at: %d %v", rev, err)
	}
}

func TestCacheBreakerDoesNotLeaveADoubleBlankLine(t *testing.T) {
	f := newFx(t)
	f.write("CLAUDE.md", "# Demo\n\nToday's date: 2026-10-29\n\nNever edit gen/.\n")
	fixes := f.detect(CacheBreaker)
	if len(fixes) != 1 || string(fixes[0].updated) != "# Demo\n\nNever edit gen/.\n" {
		t.Fatalf("got %+v", fixes)
	}
}
