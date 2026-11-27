package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/gateway/session"
	"github.com/usetrove/trove/internal/store"
)

func TestStatusLineWithNoDataIsShortAndSucceeds(t *testing.T) {
	for _, format := range []string{"shell", "tmux", "claude"} {
		h := newHarness(t, nil)
		if code := h.run("status-line", "--format", format); code != 0 {
			t.Fatalf("%s: exit %d: %s", format, code, h.err)
		}
		if got := strings.TrimSpace(h.out.String()); got != "Trove: no usage yet" {
			t.Errorf("%s: %q", format, got)
		}
	}
	h := newHarness(t, nil)
	if code := h.run("status-line", "--format", "html"); code != 1 {
		t.Errorf("a bad format should fail, exit %d", code)
	}
}

// A broken setup must never break the agent's status bar: print a hint, exit 0.
func TestStatusLineSurvivesABrokenConfig(t *testing.T) {
	h := newHarness(t, map[string]string{"TROVE_GATEWAY_PORT": "banana"})
	if code := h.run("status-line"); code != 0 || !strings.Contains(h.out.String(), "trove doctor") {
		t.Errorf("exit %d, out %q", code, h.out)
	}
}

func seedForStatus(t *testing.T, dir string, rows int) {
	t.Helper()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	key := session.ClaudeCodeKey("sess-abc")
	for i := 0; i < rows; i++ {
		ts := now.Add(-time.Duration(i) * time.Minute).UnixMilli()
		if _, err := tx.Exec(`INSERT INTO requests(ts, session_key, agent, model, status_code, input_uncached_tokens, cost_usd)
			VALUES(?,?,?,?,200,100,0.01)`, ts, fmt.Sprintf("s%d", i%500), "claude_code", "m"); err != nil {
			t.Fatal(err)
		}
	}
	// The per-day summary the gateway keeps; the status line reads only this.
	for d := 0; d < 31; d++ {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO usage_daily(day, requests, unpriced, spend_usd, saved_usd) VALUES(?,?,?,?,?)`,
			store.DayKey(now.AddDate(0, 0, -d)), 100, 0, 1.0, 0.1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO sessions(session_key, agent, started_at, ended_at, request_count, total_cost_usd) VALUES(?,?,?,?,4,3.2)`,
		key, "claude_code", now.Add(-time.Hour).UnixMilli(), now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestStatusLineReadsClaudeCodeInputAndIsFast(t *testing.T) {
	h := newHarness(t, nil)
	dir := h.app.Getenv("TROVE_DATA_DIR")
	seedForStatus(t, dir, 50000)
	if code := h.run("budget", "set", "1500", "--period", "month"); code != 0 {
		t.Fatalf("budget: exit %d: %s", code, h.err)
	}
	if raceEnabled {
		old := limitSampleTimeout
		limitSampleTimeout *= 10
		t.Cleanup(func() { limitSampleTimeout = old })
	}

	// The full input Claude Code documents, including the plan-usage meter and prompt cache.
	full := statusFixture(t, "all.json")
	run := func(input string) (string, time.Duration) {
		h.out.Reset()
		h.app.In = strings.NewReader(input)
		start := time.Now()
		code := h.run("status-line", "--format", "claude")
		d := time.Since(start)
		if code != 0 {
			t.Fatalf("exit %d: %s", code, h.err)
		}
		return strings.TrimSpace(h.out.String()), d
	}
	line, _ := run(full)
	for _, want := range []string{"/ $1,500", "% used", "of month", "session ~$3.20", "saved ~$", " · 5h 23% · wk 41%"} {
		if !strings.Contains(line, want) {
			t.Errorf("status line lacks %q: %q", want, line)
		}
	}
	if strings.ContainsAny(line, "\n\r") {
		t.Errorf("status line must be one line: %q", line)
	}
	if n := spooledSamples(t, dir); n != 1 {
		t.Fatalf("the first run should spool one sample, found %d", n)
	}

	// Timing: 50,000 recorded requests, 50 runs; the 95th percentile must stay
	// under the 50 ms budget (in-process, so it excludes process start-up).
	// The budget is 50 ms for a normal build. The race detector slows code roughly ten times, so under it the same check
	// allows ten times as long; the strict budget is still enforced by the ordinary (non-race) test run.
	budget := 50 * time.Millisecond
	if raceEnabled {
		budget *= 10
	}
	timeRuns := func(name string, input func(i int) string) {
		t.Helper()
		var ds []time.Duration
		for i := 0; i < 50; i++ {
			_, d := run(input(i))
			ds = append(ds, d)
		}
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		p95 := ds[len(ds)*95/100]
		t.Logf("status-line over 50,000 requests, %s: median %v, p95 %v", name, ds[len(ds)/2], p95)
		if p95 > budget {
			t.Errorf("%s: p95 is %v; the status line must finish in under %v", name, p95, budget)
		}
	}

	// The same meter every time: compared with the spool, nothing written.
	timeRuns("meter unchanged", func(int) string { return full })
	if n := spooledSamples(t, dir); n != 1 {
		t.Errorf("unchanged values must add nothing, found %d spooled", n)
	}
	// The worst case: the five-hour percentage moves on every run, so every run spools a sample.
	timeRuns("meter changes every run", func(i int) string {
		return strings.Replace(full, `"used_percentage": 23.5`, fmt.Sprintf(`"used_percentage": %d.25`, i), 1)
	})
	if n := spooledSamples(t, dir); n != 51 {
		t.Errorf("each change should spool one sample: want 51, found %d", n)
	}
	if n := ingestedRows(t, dir); n != 51 {
		t.Errorf("ingest should store 51 rows, found %d", n)
	}
	// After ingest the spool is empty, and the database's latest row stops a repeat.
	run(strings.Replace(full, `"used_percentage": 23.5`, `"used_percentage": 49.25`, 1))
	if n := spooledSamples(t, dir); n != 0 {
		t.Errorf("the value already in the database was spooled again (%d lines)", n)
	}
}

func statusFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "statusline", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// spooledSamples counts the samples waiting in the status line's spool.
func spooledSamples(t *testing.T, dir string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, store.LimitSpoolDir, "limits.jsonl"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), "\n")
}

// ingestedRows moves the spool into the database, as session end does, and counts limit_samples.
func ingestedRows(t *testing.T, dir string) int {
	t.Helper()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.IngestLimitSpool(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM limit_samples`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// statusJSON runs the claude-format status line with input on stdin and returns its --json output.
func statusJSON(t *testing.T, h *harness, input string) statusOutput {
	t.Helper()
	h.out.Reset()
	h.app.In = strings.NewReader(input)
	if code := h.run("status-line", "--format", "claude", "--json"); code != 0 {
		t.Fatalf("the status line must always exit 0, got %d: %s", code, h.err)
	}
	var out statusOutput
	if err := json.Unmarshal(h.out.Bytes(), &out); err != nil {
		t.Fatalf("json: %v: %s", err, h.out)
	}
	if out.Line == "" {
		t.Errorf("the status line printed nothing")
	}
	return out
}

// A busy database never slows or breaks the status line, and the sample is still kept: the status line
// never needs the write lock (rule 4).
func TestStatusLineSamplesWhileDatabaseLocked(t *testing.T) {
	h := newHarness(t, nil)
	dir := h.app.Getenv("TROVE_DATA_DIR")
	seedForStatus(t, dir, 10)
	full := statusFixture(t, "all.json")

	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	out := statusJSON(t, h, full)
	if d := time.Since(start); d > time.Second {
		t.Errorf("the status line waited %v on a locked database", d)
	}
	if out.LimitSample != sampleSpooled || out.Error != "" || !strings.Contains(out.Line, "session ~$3.20") {
		t.Errorf("locked: sample %q, error %q, line %q", out.LimitSample, out.Error, out.Line)
	}
	_, _ = conn.ExecContext(ctx, `ROLLBACK`)
	_ = conn.Close()
	if n := ingestedRows(t, dir); n != 1 {
		t.Errorf("the sample taken while locked should be ingested, found %d rows", n)
	}
}

func TestStatusLineRecordsOnlyAMeterWithASession(t *testing.T) {
	fixture := func(name string) func(*testing.T) string {
		return func(t *testing.T) string { return statusFixture(t, name) }
	}
	tests := []struct {
		name  string
		input func(*testing.T) string
		want  string
		rows  int
	}{
		{"full input", fixture("all.json"), sampleSpooled, 1},
		{"no meter yet", fixture("none.json"), "", 0},
		{"no session id", func(t *testing.T) string {
			return strings.Replace(statusFixture(t, "all.json"), `"session_id": "sess-abc"`, `"session_id": ""`, 1)
		}, "", 0},
		{"cache only (API key)", fixture("no_rate_limits_cache_only.json"), sampleSpooled, 1},
		{"invalid values only", fixture("invalid.json"), "", 0},
		{"not JSON", fixture("garbage.txt"), "", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil)
			dir := h.app.Getenv("TROVE_DATA_DIR")
			seedForStatus(t, dir, 10)
			if out := statusJSON(t, h, tc.input(t)); out.LimitSample != tc.want {
				t.Errorf("limit_sample %q, want %q", out.LimitSample, tc.want)
			}
			if n := spooledSamples(t, dir); n != tc.rows {
				t.Errorf("%d spooled, want %d", n, tc.rows)
			}
		})
	}
}

// The status line never creates Trove's data folder or database and never upgrades the schema. With a
// data folder but no database yet (a fresh plugin install), the meter is still spooled.
func TestStatusLineSampleWithoutDatabaseOrTable(t *testing.T) {
	h := newHarness(t, nil)
	dir := h.app.Getenv("TROVE_DATA_DIR")
	full := statusFixture(t, "all.json")
	if out := statusJSON(t, h, full); out.LimitSample != sampleNoDataDir || out.Line != "Trove: no usage yet · 5h 23% · wk 41%" {
		t.Errorf("no data folder: %+v", out)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the status line created the data folder: %v", err)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if out := statusJSON(t, h, full); out.LimitSample != sampleSpooled || out.Line != "Trove: no usage yet · 5h 23% · wk 41%" {
		t.Errorf("no database: %+v", out)
	}
	if _, err := os.Stat(filepath.Join(dir, store.DBFileName)); !os.IsNotExist(err) {
		t.Errorf("the status line created the database: %v", err)
	}
	if out := statusJSON(t, h, full); out.LimitSample != sampleUnchanged {
		t.Errorf("a repeat compares with the spool even without a database: %+v", out)
	}

	// A database from before the table existed: the comparison falls back to the spool.
	seedForStatus(t, dir, 10)
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE limit_samples`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	changed := strings.Replace(full, `"used_percentage": 23.5`, `"used_percentage": 24`, 1)
	if out := statusJSON(t, h, changed); out.LimitSample != sampleSpooled || !strings.Contains(out.Line, "session ~$3.20") {
		t.Errorf("no table: %+v", out)
	}
}

// `trove report` moves the spool into the database, also for users with the status line but no hooks.
func TestReportIngestsTheStatusLineSpool(t *testing.T) {
	h := newHarness(t, nil)
	dir := h.app.Getenv("TROVE_DATA_DIR")
	seedForStatus(t, dir, 10)
	if out := statusJSON(t, h, statusFixture(t, "all.json")); out.LimitSample != sampleSpooled {
		t.Fatalf("limit_sample %q", out.LimitSample)
	}
	h.out.Reset()
	if code := h.run("report", "--json"); code != 0 {
		t.Fatalf("report: exit %d: %s", code, h.err)
	}
	if n := spooledSamples(t, dir); n != 0 {
		t.Errorf("the spool should be ingested, %d lines left", n)
	}
	db, err := store.OpenReadOnly(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var pct, seven float64
	var resets int64
	var warm, ttl int
	var model, version string
	if err := db.QueryRow(`SELECT five_hour_pct, five_hour_resets_at, seven_day_pct, cache_warm, cache_ttl, model, cc_version
		FROM limit_samples WHERE session_key = ?`, session.ClaudeCodeKey("sess-abc")).
		Scan(&pct, &resets, &seven, &warm, &ttl, &model, &version); err != nil {
		t.Fatal(err)
	}
	if pct != 23.5 || resets != 4102444800000 || seven != 41.2 || warm != 1 || ttl != 3600 || model != "claude-opus-5-5" || version != "2.1.290" {
		t.Errorf("stored %v %v %v %v %v %q %q", pct, resets, seven, warm, ttl, model, version)
	}
}

// The meter is in --json with its label and source, and shown even before Trove has any usage of its own.
func TestStatusLinePlanUsageJSON(t *testing.T) {
	h := newHarness(t, nil)
	h.out.Reset()
	h.app.In = strings.NewReader(statusFixture(t, "all.json"))
	if code := h.run("status-line", "--format", "claude", "--json"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	var v struct {
		Line      string `json:"line"`
		PlanUsage struct {
			Source   string `json:"source"`
			FiveHour struct {
				UsedPercent float64   `json:"used_percent"`
				Label       string    `json:"label"`
				ResetsAt    time.Time `json:"resets_at"`
			} `json:"five_hour"`
			SevenDay struct {
				UsedPercent float64 `json:"used_percent"`
				Label       string  `json:"label"`
			} `json:"seven_day"`
		} `json:"plan_usage"`
	}
	if err := json.Unmarshal(h.out.Bytes(), &v); err != nil {
		t.Fatalf("%v: %s", err, h.out)
	}
	p := v.PlanUsage
	if v.Line != "Trove: no usage yet · 5h 23% · wk 41%" || p.Source != "claude_code" ||
		p.FiveHour.UsedPercent != 23.5 || p.FiveHour.Label != "measured" || !p.FiveHour.ResetsAt.Equal(time.Unix(4102444800, 0)) ||
		p.SevenDay.UsedPercent != 41.2 || p.SevenDay.Label != "measured" {
		t.Errorf("got %+v", v)
	}

	// No meter in the input: no plan_usage key and nothing added to the line.
	h.out.Reset()
	h.app.In = strings.NewReader(statusFixture(t, "none.json"))
	h.run("status-line", "--format", "claude", "--json")
	if strings.Contains(h.out.String(), "plan_usage") || strings.Contains(h.out.String(), "5h") {
		t.Errorf("no meter was sent, yet: %s", h.out)
	}
}

// Shell and tmux formats never read stdin, so they record nothing.
func TestStatusLineOtherFormatsRecordNothing(t *testing.T) {
	for _, format := range []string{"shell", "tmux"} {
		h := newHarness(t, nil)
		dir := h.app.Getenv("TROVE_DATA_DIR")
		seedForStatus(t, dir, 10)
		h.app.In = strings.NewReader(statusFixture(t, "all.json"))
		if code := h.run("status-line", "--format", format); code != 0 {
			t.Fatalf("%s: exit %d", format, code)
		}
		if n := spooledSamples(t, dir); n != 0 {
			t.Errorf("%s: %d spooled", format, n)
		}
	}
}

func TestStatusLineJSONAndNoColor(t *testing.T) {
	h := newHarness(t, map[string]string{"NO_COLOR": "1"})
	seedForStatus(t, h.app.Getenv("TROVE_DATA_DIR"), 10)
	h.run("budget", "set", "20", "--period", "month") // small cap: certainly near or over
	h.out.Reset()
	if code := h.run("status-line", "--format", "claude"); code != 0 || strings.Contains(h.out.String(), "\x1b[") {
		t.Errorf("NO_COLOR must remove color: exit %d %q", code, h.out)
	}
	h.out.Reset()
	if code := h.run("status-line", "--format", "claude", "--json"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	var v struct {
		Line   string `json:"line"`
		Status struct {
			Spend struct {
				Label string `json:"label"`
			} `json:"spend_usd"`
		} `json:"status"`
	}
	if err := json.Unmarshal(h.out.Bytes(), &v); err != nil || v.Line == "" || v.Status.Spend.Label != "estimated" {
		t.Errorf("json: %q (%v)", h.out, err)
	}
}
