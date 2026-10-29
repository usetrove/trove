package report

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/store"
)

var update = flag.Bool("update", false, "rewrite golden files")

// now is fixed so every golden file is stable. Everything is in UTC.
var now = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

func openDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func exec(t *testing.T, db *store.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), q, args...); err != nil {
		t.Fatal(err)
	}
}

// installedDaysAgo sets the install time the baseline counts from.
func installedDaysAgo(t *testing.T, db *store.DB, days int) {
	t.Helper()
	exec(t, db, `INSERT OR REPLACE INTO settings(key, value) VALUES('measure.install_at', ?)`,
		fmt.Sprint(now.AddDate(0, 0, -days).UnixMilli()))
}

type req struct {
	ts       time.Time
	session  string
	agent    string
	model    string
	cost     any // float64 or nil
	findings string
	shadow   string
	applied  string
	removed  int
	diff     any
}

func addReq(t *testing.T, db *store.DB, r req) {
	t.Helper()
	nilIfEmpty := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	exec(t, db, `INSERT INTO requests(ts, session_key, agent, provider, model, status_code, input_uncached_tokens, cache_read_tokens,
		output_tokens, cost_usd, findings, shadow_savings, optimizations_applied, tokens_removed, cost_diff_usd)
		VALUES(?,?,?,?,?,200,10000,40000,2000,?,?,?,?,?,?)`,
		r.ts.UnixMilli(), r.session, r.agent, "anthropic", r.model, r.cost,
		nilIfEmpty(r.findings), nilIfEmpty(r.shadow), nilIfEmpty(r.applied), r.removed, r.diff)
	exec(t, db, `INSERT INTO sessions(session_key, agent, started_at, ended_at, request_count) VALUES(?,?,?,?,1)
		ON CONFLICT(session_key) DO UPDATE SET ended_at = excluded.ended_at, request_count = request_count + 1`,
		r.session, r.agent, r.ts.UnixMilli(), r.ts.UnixMilli())
}

const (
	findingsBoth = `[{"detector":"duplicate_tool_result","tokens":4000,"usd":0.30,"confidence":"high","label":"estimated"},` +
		`{"detector":"noisy_output","tokens":6000,"usd":0.20,"confidence":"medium","label":"estimated"}]`
	findingsCache = `[{"detector":"cache_miss","tokens":9000,"usd":0.45,"confidence":"high","label":"estimated"}]`
	shadowDedupe  = `{"label":"estimated","optimizers":{"duplicate_result_collapser":{"tokens":4000,"usd":0.30}}}`
	appliedNoise  = `{"label":"estimated","optimizers":[{"name":"noise_normalizer","state":"on","tokens_removed":1000}]}`
)

// seedSmall records five requests.
func seedSmall(t *testing.T, db *store.DB) {
	t.Helper()
	installedDaysAgo(t, db, 2)
	for i := 0; i < 5; i++ {
		addReq(t, db, req{ts: now.Add(-time.Duration(i+1) * time.Hour), session: fmt.Sprintf("s%d", i), agent: "claude_code", model: "claude-a", cost: 1.25})
	}
}

// seedNormal records 30 requests over October from two agents and three models.
func seedNormal(t *testing.T, db *store.DB) {
	t.Helper()
	installedDaysAgo(t, db, 30)
	for i := 0; i < 30; i++ {
		r := req{ts: time.Date(2026, 10, 1, 6, 0, 0, 0, time.UTC).Add(time.Duration(i) * 20 * time.Hour), session: fmt.Sprintf("s%02d", i),
			agent: "claude_code", model: []string{"claude-a", "claude-b"}[i%2], cost: 1.0 + float64(i)*0.1}
		if i%3 == 0 {
			r.agent, r.model = "codex_cli", "gpt-x"
		}
		switch {
		case i%2 == 0:
			r.findings = findingsBoth
		case i%5 == 1:
			r.findings = findingsCache
		}
		if i < 12 {
			r.shadow = shadowDedupe
		}
		if i >= 20 {
			r.applied, r.removed, r.diff = appliedNoise, 1000, 0.05
		}
		addReq(t, db, r)
	}
}

func testConfig() config.Config {
	cfg := config.Default()
	cfg.Optimizers["noise_normalizer"] = "on"
	return cfg
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	got = strings.ReplaceAll(got, "\r\n", "\n")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden file %s; run `go test ./internal/report -update`", path)
	}
	if w := strings.ReplaceAll(string(want), "\r\n", "\n"); w != got {
		t.Errorf("%s differs from the golden file.\n--- want\n%s\n--- got\n%s", name, w, got)
	}
}

func toJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}
