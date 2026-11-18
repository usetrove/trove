package fixer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/store"
)

// bashSessions writes Claude Code sessions in which the agent runs the given
// commands, in order, one Bash call per request.
func (f *fx) bashSessions(cmds [][]string) {
	f.t.Helper()
	dir := filepath.Join(f.env.ClaudeDir, "projects", encodeProject(f.proj))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	usage := `"usage":{"input_tokens":10,"output_tokens":5}`
	for i, sess := range cmds {
		id := fmt.Sprintf("bash-%d", i)
		var b strings.Builder
		for j, c := range sess {
			ts := time.Date(2026, 10, 20, 10, i, j, 0, time.UTC).Format("2006-01-02T15:04:05.000Z")
			in, _ := json.Marshal(map[string]string{"command": c})
			fmt.Fprintf(&b, `{"type":"assistant","sessionId":%q,"requestId":"r%d_%d","timestamp":%q,"message":{"id":"m%d_%d","model":"claude-opus-5-5","content":[{"type":"tool_use","id":"t%d_%d","name":"Bash","input":%s}],%s}}`+"\n",
				id, i, j, ts, i, j, i, j, in, usage)
			fmt.Fprintf(&b, `{"type":"user","sessionId":%q,"timestamp":%q,"message":{"content":[{"type":"tool_result","tool_use_id":"t%d_%d","content":"ok"}]}}`+"\n", id, ts, i, j)
		}
		p := filepath.Join(dir, id+".jsonl")
		if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
			f.t.Fatal(err)
		}
		if err := os.Chtimes(p, f.idle(), f.idle()); err != nil {
			f.t.Fatal(err)
		}
	}
}

// repeat spreads n copies of cmd over s sessions.
func repeat(cmd string, n, s int) [][]string {
	out := make([][]string, s)
	for i := 0; i < n; i++ {
		out[i%s] = append(out[i%s], cmd)
	}
	return out
}

const genCmd = `python3 scripts/gen_fixtures.py --all "two words"`

func TestRetypedPlainCommandBecomesTroveRunEntry(t *testing.T) {
	f := newFx(t)
	sess := repeat(genCmd, 6, 3)
	sess[0][0] = "python3   scripts/gen_fixtures.py --all \"two words\"" // spacing differs, same command
	f.bashSessions(sess)
	fixes := f.detect(RetypedCommand)
	if len(fixes) != 1 {
		t.Fatalf("want one fix, got %d", len(fixes))
	}
	x := fixes[0]
	if !x.Applicable() || !x.Suggestion || x.Class != "config_overhead" || x.Tier != "T1" || !strings.HasPrefix(x.ID, "run-") {
		t.Fatalf("unexpected fix: %+v", x)
	}
	if !strings.Contains(x.Evidence[0], "6 times in 3 of 3 sessions") || x.TokensPerSession <= 0 || x.USDPerSession != nil {
		t.Fatalf("evidence or estimate wrong: %v %d", x.Evidence, x.TokensPerSession)
	}
	got, err := parseCommands(x.updated)
	if err != nil {
		t.Fatal(err)
	}
	if run := got["python3-gen_fixtures"].Run; !slices.Equal(run, []string{"python3", "scripts/gen_fixtures.py", "--all", "two words"}) {
		t.Fatalf("entry: %v in\n%s", got, x.updated)
	}
	if !strings.Contains(string(x.updated), "trove mcp trust") {
		t.Fatal("a new file must say it needs approval")
	}
	if again := f.detect(RetypedCommand); len(again) != 1 || again[0].ID != x.ID {
		t.Fatal("the fix id must be stable between runs")
	}
}

func TestRetypedThresholds(t *testing.T) {
	cases := []struct {
		name string
		sess [][]string
	}{
		{"too few times", repeat(genCmd, 4, 3)},
		{"too few sessions", repeat(genCmd, 6, 2)},
		{"too short", repeat("go test ./...", 9, 3)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFx(t)
			f.bashSessions(c.sess)
			if got := f.detect(RetypedCommand); len(got) != 0 {
				t.Fatalf("no fix expected, got %+v", got[0])
			}
		})
	}
}

func TestRetypedShellSyntaxIsAdvisory(t *testing.T) {
	for _, cmd := range []string{
		"go test ./... 2>&1 | tail -50",
		"cd internal && go test ./fixer/...",
		"python3 - <<'EOF'\nprint(1)\nEOF",
		"FOO=bar ./scripts/run_everything.sh",
		`echo "unterminated quote here`,
	} {
		f := newFx(t)
		f.bashSessions(repeat(cmd, 5, 3))
		fixes := f.detect(RetypedCommand)
		if len(fixes) != 1 || fixes[0].Applicable() || !strings.Contains(fixes[0].Advisory, "script") || !strings.Contains(fixes[0].Why, "shell") {
			t.Fatalf("%q: want one advisory fix, got %+v", cmd, fixes)
		}
	}
}

func TestRetypedSkipsListedAndAvoidsNameClash(t *testing.T) {
	f := newFx(t)
	f.write(".trove/commands.toml", "[commands.python3-gen_fixtures]\nrun = [\"other\"]\n\n[commands.lint]\nrun = [\"golangci-lint\", \"run\", \"--fix\", \"./internal/...\"]\n")
	f.bashSessions(append(repeat(genCmd, 5, 3), repeat("golangci-lint run --fix ./internal/...", 5, 3)...))
	fixes := f.detect(RetypedCommand)
	if len(fixes) != 1 {
		t.Fatalf("the listed command must be skipped; got %d fixes", len(fixes))
	}
	got, err := parseCommands(fixes[0].updated)
	if err != nil || len(got) != 3 || got["python3-gen_fixtures-2"].Run == nil || got["python3-gen_fixtures"].Run[0] != "other" {
		t.Fatalf("existing entries must stay and the new name must not clash: %v %v", got, err)
	}
}

func TestRetypedInvalidFileBlocks(t *testing.T) {
	f := newFx(t)
	f.write(".trove/commands.toml", "[commands\n")
	f.bashSessions(repeat(genCmd, 5, 3))
	fixes := f.detect(RetypedCommand)
	if len(fixes) != 1 || fixes[0].Applicable() || !strings.Contains(fixes[0].Blocked, "not valid") {
		t.Fatalf("want a blocked fix, got %+v", fixes)
	}
}

func TestSplitArgv(t *testing.T) {
	cases := []struct {
		in   string
		want []string
		ok   bool
	}{
		{"go test -run TestX ./internal/fixer/", []string{"go", "test", "-run", "TestX", "./internal/fixer/"}, true},
		{`npm run "build all" 'x y'`, []string{"npm", "run", "build all", "x y"}, true},
		{`echo ""`, []string{"echo", ""}, true},
		{"ls | wc", nil, false},
		{"echo $HOME", nil, false},
		{"rm *.tmp", nil, false},
		{"export X=1", nil, false},
		{`echo "a\"b"`, nil, false},
	}
	for _, c := range cases {
		got, ok := splitArgv(c.in)
		if ok != c.ok || !slices.Equal(got, c.want) {
			t.Errorf("splitArgv(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

// Applying the fix writes the file and records it; adoption is then measured by
// the existing trove_run counter (hook_events kind "adopted").
func TestRetypedApplyAndAdoption(t *testing.T) {
	f := newFx(t)
	f.bashSessions(repeat(genCmd, 6, 3))
	x := f.detect(RetypedCommand)[0]
	res, err := Apply(context.Background(), f.env, []string{x.ID})
	if err != nil || len(res) != 1 || res[0].Status != "applied" {
		t.Fatalf("apply: %+v %v", res, err)
	}
	if !strings.Contains(f.read(filepath.Join(f.proj, ".trove", "commands.toml")), "[commands.python3-gen_fixtures]") {
		t.Fatal("file not written")
	}

	db, err := store.Open(f.env.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	f.env.DB = db.DB
	ctx := context.Background()
	var applied int64
	if err := db.QueryRowContext(ctx, `SELECT applied_at FROM fixes WHERE fix_id = ?`, x.ID).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	ins := func(ts int64, sess, hook, kind string) {
		if _, err := db.ExecContext(ctx, `INSERT INTO hook_events(ts, session_key, hook, kind) VALUES (?,?,?,?)`, ts, sess, hook, kind); err != nil {
			t.Fatal(err)
		}
	}
	lo := config.OptimizerHookLargeOutput
	ins(applied-1000, "c-before", lo, "adopted") // before the fix: not counted
	ins(applied+1000, "c-a", lo, "adopted")
	ins(applied+2000, "c-a", lo, "adopted")
	ins(applied+3000, "c-b", "hook_repeat_read", "blocked")
	ins(applied+4000, "x-probe", lo, "adopted") // probe sessions are not real sessions

	a, err := RetypedAdoption(ctx, f.env, true)
	if err != nil || a == nil {
		t.Fatalf("adoption: %+v %v", a, err)
	}
	if a.Commands != 1 || a.AdoptedSessions != 1 || a.HookSessions != 2 || a.Label != "measured" {
		t.Fatalf("adoption: %+v", a)
	}
	if a, _ := RetypedAdoption(ctx, f.env, false); a == nil || a.Observable || a.AdoptedSessions != 0 {
		t.Fatalf("without the counter hook nothing may be claimed: %+v", a)
	}
	other := *f.env
	other.ProjectDir = filepath.Join(f.env.Home, "work", "other")
	if a, _ := RetypedAdoption(ctx, &other, true); a != nil {
		t.Fatalf("another project has no retyped fixes: %+v", a)
	}
}

// The database never sees command text: only the fixes row with a path hash.
func TestRetypedStoresNoCommandText(t *testing.T) {
	f := newFx(t)
	f.bashSessions(repeat(genCmd, 6, 3))
	x := f.detect(RetypedCommand)[0]
	if _, err := Apply(context.Background(), f.env, []string{x.ID}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(f.env.DataDir, "trove.db"))
	if err != nil {
		t.Skip("database file not found:", err)
	}
	for _, s := range []string{"gen_fixtures", "two words", "python3"} {
		if strings.Contains(string(b), s) {
			t.Fatalf("database contains %q", s)
		}
	}
}
