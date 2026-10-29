package measure

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/store"
)

// testPrices is a fixed price table so hand calculations never drift when the
// shipped prices change. Per million tokens: input 2, output 10, cache read 0.2,
// 5-minute cache write 2.5, one-hour cache write 4.
const testPrices = `{
  "schema_version": 1,
  "data_version": "test.1",
  "entries": [
    {"provider": "anthropic", "model": "m", "effective_from": "2026-01-01", "effective_from_source": "https://example.com/a",
     "standard": {"input": "2", "output": "10", "cache_read": "0.2", "cache_write": {"5m": "2.5", "1h": "4"}},
     "source_url": "https://example.com/p", "verified_at": "2026-01-01"}
  ]
}`

func testTable(t *testing.T) *pricing.Table {
	t.Helper()
	tb, err := pricing.Load(strings.NewReader(testPrices))
	if err != nil {
		t.Fatal(err)
	}
	return tb
}

func openDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

var t0 = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

// seed describes one finished session for the guardrail tests.
type seed struct {
	key       string
	pinnedAt  time.Time
	on        []string
	repeats   int
	toolErrs  int
	collapsed int
	refetches int
	noTools   bool
}

// insertFinished stores a session row with its roll-up, as RollupSessions would.
func insertFinished(t *testing.T, db *store.DB, s seed) {
	t.Helper()
	sig := SessionSignals{
		Turns: 10, Tokens: 1000, DurationS: 600, HasTools: !s.noTools, ToolCalls: 10 + s.repeats,
		Repeats: s.repeats, ToolErrors: s.toolErrs, Collapsed: s.collapsed, Refetches: s.refetches,
		On: s.on, Acted: s.on, PinnedAt: s.pinnedAt.UnixMilli(),
	}
	if sig.On == nil {
		sig.On, sig.Acted = []string{}, []string{}
	}
	b, _ := json.Marshal(sig)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO sessions(session_key, agent, started_at, ended_at, request_count, outcome_signals)
		VALUES(?, 'claude_code', ?, ?, 10, ?)`, s.key, s.pinnedAt.UnixMilli(), s.pinnedAt.Add(10*time.Minute).UnixMilli(), string(b)); err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, n := range s.on {
		states[n] = "on"
	}
	sj, _ := json.Marshal(states)
	if _, err := db.ExecContext(ctx, `INSERT INTO session_pins(session_key, states, custom_agents, bytes_per_token, pinned_at)
		VALUES(?, ?, '[]', 3.5, ?)`, s.key, string(sj), s.pinnedAt.UnixMilli()); err != nil {
		t.Fatal(err)
	}
}

// cycle returns n values taken in turn from pattern.
func cycle(pattern []int, n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = pattern[i%len(pattern)]
	}
	return out
}
