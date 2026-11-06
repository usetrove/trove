package logs_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/logs"
	"github.com/usetrove/trove/internal/logs/cchistory"
	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/store"
)

var future = func() time.Time { return time.Now().Add(24 * time.Hour) }

func newIngester(t *testing.T) (*logs.Ingester, *store.DB) {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	tbl, err := pricing.Default()
	if err != nil {
		t.Fatal(err)
	}
	return &logs.Ingester{
		DB:       db,
		Sources:  []logs.Source{&cchistory.Source{Now: future}},
		Versions: logs.ParserVersions{logs.SourceClaudeCode: cchistory.ParserVersion},
		Pricing:  tbl,
		Plan:     func(string) logs.Plan { return logs.PlanAPIKey },
	}, db
}

func historyDir(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "projects", "C--Users-alice-secretproject")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(root)
}

func copyFixture(t *testing.T, projects, name string) string {
	t.Helper()
	b, err := os.ReadFile("cchistory/testdata/v2_basic.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(projects, "C--Users-alice-secretproject", name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func count(t *testing.T, db *store.DB, q string) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestScanStoresPricedRowsOnceAndSkipsUnchangedFiles(t *testing.T) {
	in, db := newIngester(t)
	proj := historyDir(t)
	copyFixture(t, proj, "sess-1.jsonl")
	ctx := context.Background()

	sum, err := in.Scan(ctx, logs.Options{Roots: []string{proj}})
	if err != nil || sum.Files != 1 || sum.Read != 1 || sum.Events != 2 {
		t.Fatalf("first scan: %+v %v", sum, err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM history_requests WHERE cost_usd > 0 AND pricing_status='priced' AND plan='api_key'`); n != 2 {
		t.Errorf("want 2 priced rows, got %d", n)
	}
	// 10 uncached + 1000 read + 300 1h-write + 50 out on Sonnet must be > 0 and small.
	sum, _ = in.Scan(ctx, logs.Options{Roots: []string{proj}})
	if sum.Unchanged != 1 || sum.Read != 0 {
		t.Errorf("second scan should not open the file: %+v", sum)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM history_requests`); n != 2 {
		t.Errorf("rescan changed row count: %d", n)
	}
}

func TestScanReadsOnlyAppendedData(t *testing.T) {
	in, db := newIngester(t)
	proj := historyDir(t)
	p := copyFixture(t, proj, "sess-1.jsonl")
	ctx := context.Background()
	if _, err := in.Scan(ctx, logs.Options{Roots: []string{proj}}); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(`{"type":"assistant","sessionId":"sess-1","requestId":"req_C","timestamp":"2026-10-20T11:00:00.000Z","version":"2.1.302","message":{"id":"m","model":"claude-sonnet-5-5","content":[{"type":"text","text":"x"}],"usage":{"input_tokens":1,"output_tokens":1}}}` + "\n")
	_ = f.Close()
	sum, err := in.Scan(ctx, logs.Options{Roots: []string{proj}})
	if err != nil || sum.Read != 1 || sum.Events != 1 {
		t.Fatalf("append scan should find only the new request: %+v %v", sum, err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM history_requests`); n != 3 {
		t.Errorf("want 3 rows, got %d", n)
	}
}

func TestSinceSkipsOldFiles(t *testing.T) {
	in, _ := newIngester(t)
	proj := historyDir(t)
	p := copyFixture(t, proj, "old.jsonl")
	old := time.Now().AddDate(0, 0, -90)
	_ = os.Chtimes(p, old, old)
	sum, _ := in.Scan(context.Background(), logs.Options{Roots: []string{proj}, Since: time.Now().AddDate(0, 0, -30)})
	if sum.Files != 0 || sum.Events != 0 {
		t.Errorf("old file should be ignored: %+v", sum)
	}
}

// Nothing from the logs may reach the database: not prompt text, tool output,
// file paths, raw session ids or provider request ids.
func TestNoLogContentReachesDatabase(t *testing.T) {
	in, db := newIngester(t)
	proj := historyDir(t)
	copyFixture(t, proj, "sess-1.jsonl")
	if _, err := in.Scan(context.Background(), logs.Options{Roots: []string{proj}}); err != nil {
		t.Fatal(err)
	}
	var dump strings.Builder
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table'`)
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
	for _, tbl := range tables {
		r, err := db.Query(`SELECT * FROM "` + tbl + `"`)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := r.Columns()
		for r.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			_ = r.Scan(ptrs...)
			fmt.Fprint(&dump, vals...)
			dump.WriteByte('\n')
		}
		_ = r.Close()
	}
	if !strings.Contains(dump.String(), "claude_code") {
		t.Fatal("dump is missing history rows; test is not checking anything")
	}
	for _, s := range []string{"SECRET", "alice", "secretproject", "main.go", "/home", "sess-1", "req_A", "msg_A", "toolu_1", "line1", "please fix"} {
		if strings.Contains(dump.String(), s) {
			t.Errorf("database contains log content %q", s)
		}
	}
}

// synthetic writes a history of about mb megabytes shaped like real Claude Code
// files: many large non-turn lines, plus requests with tool calls and results.
func synthetic(t *testing.T, proj string, mb int) int {
	t.Helper()
	filler := `{"type":"api-request-blob","sessionId":"s","hash":"h","message":{"data":"` + strings.Repeat("x", 30000) + `"}}` + "\n"
	result := strings.Repeat("r", 2000)
	perFile := 8 << 20
	nFiles := max(1, mb<<20/perFile)
	reqs := 0
	base := time.Now().AddDate(0, 0, -20)
	for f := range nFiles {
		var b strings.Builder
		sid := "sess-" + strconv.Itoa(f)
		for i := 0; b.Len() < perFile; i++ {
			ts := base.Add(time.Duration(f*100+i) * time.Second).UTC().Format("2006-01-02T15:04:05.000Z")
			req := fmt.Sprintf("req_%d_%d", f, i)
			tool := fmt.Sprintf("toolu_%d_%d", f, i)
			fmt.Fprintf(&b, `{"type":"assistant","sessionId":%q,"requestId":%q,"timestamp":%q,"version":"2.1.302","message":{"id":"m","model":"claude-sonnet-5-5","content":[{"type":"tool_use","id":%q,"name":"Bash","input":{}}],"usage":{"input_tokens":5,"output_tokens":100,"cache_read_input_tokens":20000,"cache_creation_input_tokens":500,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":500}}}}`+"\n", sid, req, ts, tool)
			fmt.Fprintf(&b, `{"type":"user","sessionId":%q,"message":{"content":[{"type":"tool_result","tool_use_id":%q,"content":%q}]}}`+"\n", sid, tool, result)
			b.WriteString(filler)
			reqs++
		}
		if err := os.WriteFile(filepath.Join(proj, "C--p", sid+".jsonl"), []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return reqs
}

func TestScanLargeHistoryWithinBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("large synthetic history")
	}
	mb := 150
	if v, err := strconv.Atoi(os.Getenv("TROVE_PERF_MB")); err == nil {
		mb = v // set to 450 to match a heavy 30-day history
	}
	in, db := newIngester(t)
	proj := filepath.Join(t.TempDir(), "projects")
	if err := os.MkdirAll(filepath.Join(proj, "C--p"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := synthetic(t, proj, mb)
	start := time.Now()
	sum, err := in.Scan(context.Background(), logs.Options{Roots: []string{proj}, Since: time.Now().AddDate(0, 0, -30)})
	took := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d MB, %d requests, %d files in %s", mb, sum.Events, sum.Files, took)
	if int(sum.Events) != want || count(t, db, `SELECT COUNT(*) FROM history_requests`) != int64(want) {
		t.Errorf("want %d requests, scan found %d", want, sum.Events)
	}
	if took > 30*time.Second {
		t.Errorf("scan took %s; the first run must finish in under 30 s", took)
	}
	start = time.Now()
	sum, _ = in.Scan(context.Background(), logs.Options{Roots: []string{proj}})
	if sum.Read != 0 || time.Since(start) > 2*time.Second {
		t.Errorf("unchanged rescan should be near-instant: %+v in %s", sum, time.Since(start))
	}
}
