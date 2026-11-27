package measure

import (
	"context"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/store"
)

const sonnet = "claude-sonnet-5-5"

func TestStaggerShadow(t *testing.T) {
	tbl, err := pricing.Default()
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC).UnixMilli()
	sp := func(sess, typ string, at int64) spawnRow {
		return spawnRow{session: sess, typ: typ, typeKey: "k-" + typ, ts: base + at}
	}
	run := func(start, reply, read, w5 int64) *staggerRun {
		return &staggerRun{agent: "a", start: base + start, first: &firstReq{ts: base + reply, model: sonnet, read: read, w5: w5}}
	}
	// A 100,000-token write moved to a read saves (2.5 - 0.1) / 10 = $0.24 on Sonnet 5.5, under the long-context threshold.
	const mtok = 100_000
	cases := []struct {
		name                                 string
		spawns                               []spawnRow
		runs                                 map[string][]*staggerRun
		groups, later, matched, read, within int
		holdMedian                           int64
		save                                 float64
	}{
		{name: "no spawns"},
		{name: "a lone spawn is not a sibling", spawns: []spawnRow{sp("s", "Explore", 0)},
			runs: map[string][]*staggerRun{"s\x00Explore": {run(10, 2000, 0, mtok)}}},
		{name: "two siblings, first replies in 2s: held 1.9s, the smaller write shared",
			spawns: []spawnRow{sp("s", "Explore", 0), sp("s", "Explore", 100)},
			runs:   map[string][]*staggerRun{"s\x00Explore": {run(10, 2000, 0, mtok), run(110, 2100, 0, 2*mtok)}},
			groups: 1, later: 1, matched: 1, within: 1, holdMedian: 1900, save: 0.24},
		{name: "first reply past the cap: nothing held",
			spawns: []spawnRow{sp("s", "Explore", 0), sp("s", "Explore", 100)},
			runs:   map[string][]*staggerRun{"s\x00Explore": {run(10, 9000, 0, mtok), run(110, 9100, 0, mtok)}},
			groups: 1, later: 1, matched: 1},
		{name: "a sibling that already read is not counted as a saving",
			spawns: []spawnRow{sp("s", "Explore", 0), sp("s", "Explore", 100)},
			runs:   map[string][]*staggerRun{"s\x00Explore": {run(10, 2000, 0, mtok), run(110, 2100, 500, mtok)}},
			groups: 1, later: 1, matched: 1, read: 1},
		{name: "different types and sessions, or too far apart, are not siblings",
			spawns: []spawnRow{sp("s", "Explore", 0), sp("s", "Plan", 100), sp("t", "Explore", 200), sp("s", "Explore", 5000)},
			runs:   map[string][]*staggerRun{}},
		{name: "a sibling whose run is not in the history yet is counted but not judged",
			spawns: []spawnRow{sp("s", "Explore", 0), sp("s", "Explore", 100), sp("s", "Explore", 150)},
			runs:   map[string][]*staggerRun{"s\x00Explore": {run(10, 2000, 0, mtok), run(110, 2100, 0, mtok)}},
			groups: 1, later: 2, matched: 1, within: 1, holdMedian: 1900, save: 0.24},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := staggerShadow(c.spawns, c.runs, tbl, StaggerWindow, StaggerCap)
			if s.Groups != c.groups || s.Later != c.later || s.Matched != c.matched || s.AlreadyRead != c.read || s.WithinCap != c.within ||
				s.HoldMsMedian != c.holdMedian || int(s.WouldSaveUSD*1000+0.5) != int(c.save*1000+0.5) || s.Text == "" || s.Label != Measured {
				t.Errorf("got %+v", s)
			}
		})
	}
}

func TestLoadStaggerShadowJoinsSpawnsRunsAndHistory(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	base := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC).UnixMilli()
	for _, q := range []string{
		`INSERT INTO subagent_spawns (session_key, tool_key, ts, agent_type, type_key) VALUES ('s','t1',?,'Explore','k'),('s','t2',?,'Explore','k')`,
		`INSERT INTO subagent_runs (session_key, agent_key, agent_type, started_at) VALUES ('s','a1','Explore',?),('s','a2','Explore',?)`,
	} {
		if _, err := db.ExecContext(ctx, q, base, base+100); err != nil {
			t.Fatal(err)
		}
	}
	// A run's later requests must not count as its first.
	for _, r := range []struct {
		agent       string
		at, read, w int64
	}{{"a1", 2000, 0, 19000}, {"a1", 9000, 19000, 300}, {"a2", 2100, 0, 19100}, {"a2", 9100, 19100, 200}} {
		if _, err := db.ExecContext(ctx, `INSERT INTO history_requests (source, request_key, session_key, ts, model, cache_read, cache_write_5m, agent_key)
			VALUES ('claude_code', ?, 's', ?, ?, ?, ?, ?)`, r.agent+string(rune('0'+r.at/1000)), base+r.at, sonnet, r.read, r.w, r.agent); err != nil {
			t.Fatal(err)
		}
	}
	s, err := LoadStaggerShadow(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if s.Spawns != 2 || s.Groups != 1 || s.Matched != 1 || s.AlreadyRead != 0 || s.WithinCap != 1 || s.WriteTokens != 19100 || s.HoldMsMedian != 1900 {
		t.Errorf("got %+v", s)
	}
}
