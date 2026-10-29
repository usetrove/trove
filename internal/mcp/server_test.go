package mcp

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/usetrove/trove/internal/mcp/root"
	"github.com/usetrove/trove/internal/recovery"
)

func session(t *testing.T, o Options) *sdk.ClientSession {
	t.Helper()
	ct, st := sdk.NewInMemoryTransports()
	ss, err := NewServer(o).Connect(context.Background(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "0"}, nil).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func call(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error: %v", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

func serverOpts(t *testing.T) Options {
	t.Helper()
	r, dir := newProject(t)
	write(t, dir, "x.txt", "needle here\n")
	st, err := recovery.Open(filepath.Join(t.TempDir(), "rec"), recovery.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return Options{Root: r, Store: st, Version: "test"}
}

func TestProtocolListAndCallEveryTool(t *testing.T) {
	cs := session(t, serverOpts(t))
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
	}
	if strings.Join(names, ",") != "trove_fetch_full,trove_read,trove_run,trove_search" && len(names) != 4 {
		t.Fatalf("tools: %v", names)
	}
	if got, bad := call(t, cs, "trove_read", map[string]any{"path": "small.txt"}); bad || got != "hello\nworld\n" {
		t.Errorf("read: %q %v", got, bad)
	}
	if got, bad := call(t, cs, "trove_search", map[string]any{"query": "needle"}); bad || !strings.Contains(got, "x.txt:1: needle here") {
		t.Errorf("search: %q %v", got, bad)
	}
	if got, bad := call(t, cs, "trove_run", map[string]any{"name": "test"}); !bad || !strings.Contains(got, "commands.toml") {
		t.Errorf("run without allowlist should be a tool error with guidance: %q %v", got, bad)
	}
}

func TestToolErrorsAreResultsNotCrashes(t *testing.T) {
	cs := session(t, serverOpts(t))
	for name, args := range map[string]map[string]any{
		"trove_read":       {"path": "../../etc/passwd"},
		"trove_fetch_full": {"output_id": "../../x"},
		"trove_search":     {"query": ""},
	} {
		if got, bad := call(t, cs, name, args); !bad || got == "" {
			t.Errorf("%s: want a tool error with a message, got %q %v", name, got, bad)
		}
	}
}

func TestFetchFullRoundTrip(t *testing.T) {
	o := serverOpts(t)
	cs := session(t, o)
	raw := bigFailingGoTest()
	id, err := o.Store.Put([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	got, bad := call(t, cs, "trove_fetch_full", map[string]any{"output_id": id})
	if bad || got != raw {
		t.Fatalf("full output must come back byte for byte (bad=%v, %d vs %d bytes)", bad, len(got), len(raw))
	}
	part, _ := call(t, cs, "trove_fetch_full", map[string]any{"output_id": id, "start": 599, "end": 601})
	if !strings.Contains(part, "=== RUN   TestBad") || !strings.HasPrefix(part, "(") {
		t.Errorf("range: %q", part)
	}
	if got, bad := call(t, cs, "trove_fetch_full", map[string]any{"output_id": "0123456789abcdef"}); !bad || !strings.Contains(got, "expired") {
		t.Errorf("unknown id: %q %v", got, bad)
	}
}

func TestFetchLargeOutputIsFullyReachableByRanges(t *testing.T) {
	o := serverOpts(t)
	var b strings.Builder
	for i := 0; i < 20000; i++ {
		b.WriteString("a line of output that is fairly long to push past the cap ....................\n")
	}
	id, _ := o.Store.Put([]byte(b.String()))
	var rebuilt strings.Builder
	start := 1
	for guard := 0; guard < 50; guard++ {
		got, err := Fetch(o.Store, FetchArgs{OutputID: id, Start: start})
		if err != nil {
			t.Fatal(err)
		}
		i := strings.LastIndex(got, "\n[showing lines ")
		if i < 0 {
			_, body, _ := strings.Cut(got, "\n") // drop "(N lines in all)"
			rebuilt.WriteString(body)
			break
		}
		rebuilt.WriteString(got[:i])
		var a, z, n int
		if _, err := fmt.Sscanf(strings.TrimLeft(got[i:], "\n"), "[showing lines %d-%d of %d;", &a, &z, &n); err != nil {
			t.Fatal(err)
		}
		start = z + 1
	}
	if rebuilt.String() != b.String() {
		t.Errorf("paging through ranges did not reproduce the output (%d vs %d bytes)", rebuilt.Len(), b.Len())
	}
}

// The schema is sent with every request, so its size is a budget.
func TestSchemaSizeBudget(t *testing.T) {
	r, _ := root.New(t.TempDir())
	sz, err := MeasureSchema(context.Background(), Options{Root: r, Store: nil})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("tools=%d bytes=%d estimated tokens=%d per tool=%v", sz.Tools, sz.Bytes, sz.EstimatedTokens, sz.PerTool)
	if sz.Tools != 4 || sz.Label != "estimated" {
		t.Fatalf("%+v", sz)
	}
	const budget = 400
	if sz.EstimatedTokens > budget {
		t.Errorf("tool schemas are about %d tokens, over the %d budget; shorten descriptions", sz.EstimatedTokens, budget)
	}
}
