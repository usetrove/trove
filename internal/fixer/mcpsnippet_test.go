package fixer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/mcp"
	"github.com/usetrove/trove/internal/mcp/install"
	"github.com/usetrove/trove/internal/store"
	"github.com/usetrove/trove/internal/technique"
)

func (f *fx) registerMCP() {
	f.t.Helper()
	if err := install.SaveCLI(f.env.DataDir, []install.CLIInstall{{Agent: "claude_code", Scope: "user", AppliedAt: now}}); err != nil {
		f.t.Fatal(err)
	}
}

// Without Trove's MCP server the snippet names tools the agent cannot call, so nothing is offered.
func TestMCPSnippetNeedsTheServer(t *testing.T) {
	f := newFx(t)
	f.write("CLAUDE.md", "# Demo\n")
	if got := f.detect(MCPSnippet); len(got) != 0 {
		t.Fatalf("offered without the MCP server: %+v", got)
	}
}

func TestMCPSnippetIsAT2SuggestionWithChargedOverhead(t *testing.T) {
	f := newFx(t)
	f.registerMCP()
	f.write("CLAUDE.md", "# Demo\nRun make test.")
	got := f.detect(MCPSnippet)
	if len(got) != 1 {
		t.Fatalf("want one fix, got %+v", got)
	}
	x := got[0]
	if x.Tier != "T2" || x.Class != "behavior_instructions" || !x.Suggestion {
		t.Errorf("must be a T2 behavioural suggestion: %+v", x)
	}
	if x.OverheadTokensPerRequest != int64(mcp.SnippetTokens()) || x.OverheadLabel != "estimated" {
		t.Errorf("overhead not charged: %d %q", x.OverheadTokensPerRequest, x.OverheadLabel)
	}
	if x.TokensPerSession != 0 || x.USDPerSession != nil {
		t.Errorf("the snippet claims no saving: %+v", x)
	}
	if !strings.Contains(x.Diff, "+"+mcp.Snippet) {
		t.Errorf("diff does not add the snippet:\n%s", x.Diff)
	}
	// --all never applies it.
	if n := f.applyAllCount(MCPSnippet); n != 0 {
		t.Errorf("--all applied the snippet %d times", n)
	}
	// The registry's declared overhead matches the snippet.
	if tx, ok := technique.Get(MCPSnippet); !ok || tx.Overhead == nil || *tx.Overhead != mcp.SnippetTokens() {
		t.Errorf("registry overhead out of date: %+v (snippet is %d tokens)", tx.Overhead, mcp.SnippetTokens())
	}
}

func TestMCPSnippetApplyThenNotOfferedAgain(t *testing.T) {
	f := newFx(t)
	f.registerMCP()
	p := f.write("CLAUDE.md", "# Demo\n")
	x := f.detect(MCPSnippet)[0]
	res, err := Apply(context.Background(), f.env, []string{x.ID})
	if err != nil || len(res) != 1 || res[0].Status != "applied" {
		t.Fatalf("apply: %+v %v", res, err)
	}
	if want := "# Demo\n\n" + mcp.Snippet + "\n"; f.read(p) != want {
		t.Fatalf("CLAUDE.md = %q, want %q", f.read(p), want)
	}
	if again := f.detect(MCPSnippet); len(again) != 0 {
		t.Fatalf("offered again after applying: %+v", again)
	}
}

// Done when: adoption and overhead are both measured.
func TestMCPSnippetAdoptionAndOverheadMeasured(t *testing.T) {
	f := newFx(t)
	f.registerMCP()
	f.write("CLAUDE.md", "# Demo\n")
	f.sessions(4, []string{"Read"}, f.idle()) // 2 requests per session in the project's history
	x := f.detect(MCPSnippet)[0]
	if x.RequestsPerSession != 2 || x.RequestsLabel != "measured" {
		t.Errorf("requests per session: %v %q", x.RequestsPerSession, x.RequestsLabel)
	}
	if _, err := Apply(context.Background(), f.env, []string{x.ID}); err != nil {
		t.Fatal(err)
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
	n := 0
	ins := func(ts int64, tools map[string]int) {
		n++
		b, _ := json.Marshal(tools)
		if _, err := db.ExecContext(ctx, `INSERT INTO history_requests(request_key, source, session_key, ts, tool_names) VALUES (?,?,?,?,?)`,
			fmt.Sprintf("r%d", n), "claude_code", fmt.Sprintf("s%d", n), ts, string(b)); err != nil {
			t.Fatal(err)
		}
	}
	ins(applied-int64(time.Hour/time.Millisecond), map[string]int{"Read": 9, "mcp__trove__trove_read": 1})   // before: 10%
	ins(applied+1000, map[string]int{"Read": 1, "mcp__trove__trove_read": 2, "mcp__trove__trove_search": 1}) // after: 75%
	f.env.Now = time.UnixMilli(applied + 5000)

	u, err := MCPSnippetUse(ctx, f.env)
	if err != nil || u == nil {
		t.Fatalf("use: %+v %v", u, err)
	}
	if u.Before == nil || int(u.Before.Value+0.5) != 10 || u.After == nil || int(u.After.Value+0.5) != 75 {
		t.Fatalf("adoption before/after: %+v / %+v", u.Before, u.After)
	}
	if u.Before.Label != "estimated" || u.TroveCallsAfter != 3 {
		t.Errorf("labels or counts: %+v", u)
	}
	if u.OverheadTokensPerRequest != int64(mcp.SnippetTokens()) || u.OverheadLabel != "estimated" ||
		u.RequestsPerSession != 2 || u.OverheadPerSession != 2*int64(mcp.SnippetTokens()) {
		t.Errorf("overhead: %+v", u)
	}

	// Undone: nothing is attributed to it any more.
	if _, err := db.ExecContext(ctx, `UPDATE fixes SET reverted_at = ? WHERE fix_id = ?`, applied+2000, x.ID); err != nil {
		t.Fatal(err)
	}
	if u, _ := MCPSnippetUse(ctx, f.env); u != nil {
		t.Fatalf("reverted snippet still measured: %+v", u)
	}
}

// The database never sees the snippet or CLAUDE.md text.
func TestMCPSnippetStoresNoText(t *testing.T) {
	f := newFx(t)
	f.registerMCP()
	f.write("CLAUDE.md", "# Secret project notes\n")
	x := f.detect(MCPSnippet)[0]
	if _, err := Apply(context.Background(), f.env, []string{x.ID}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(f.env.DataDir, "trove.db"))
	if err != nil {
		t.Skip("database file not found:", err)
	}
	for _, s := range []string{"Secret project", "trove_fetch_full", "CLAUDE.md"} {
		if strings.Contains(string(b), s) {
			t.Fatalf("database contains %q", s)
		}
	}
}
