package measure

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/store"
)

// Sibling stagger, shadow only (docs/sibling-stagger.md). When an agent starts several subagents of one type at
// once, each first request writes the shared prefix (system prompt and tools) because no sibling's entry exists
// yet; the provider makes an entry readable only once the first response starts. The stagger would hold the
// later siblings in PreToolUse until the first sibling's first reply, capped at a few seconds. Nothing is held
// today; this reads what the PreToolUse hook recorded (subagent_spawns) and the history, and says what the hold
// would have cost and saved.
//
// What it can and cannot show:
//   - Whether a later sibling's first request read or wrote is measured from its own log.
//   - The wait is an upper bound: the history logs the first reply when it was written, and the entry is
//     readable from the moment the response starts.
//   - The saving is an estimated upper bound: it assumes the whole of the smaller of the two first writes was
//     shared. Only the system prompt and tools are shared; the task prompt differs per sibling.
//   - Spawns are matched to runs by session, agent type label and time order. Two of the user's own agent types
//     spawned in the same second may be matched to each other's runs.

// Stagger defaults: siblings come within a few hundred milliseconds of each other, and the hold is kept well
// under the hook's own 5-second timeout.
const (
	StaggerWindow = 2 * time.Second
	StaggerCap    = 3 * time.Second
	// staggerMatch is how long after its spawn a run's SubagentStart may come and still be matched to it.
	staggerMatch = time.Minute
)

// StaggerShadow is what holding later siblings would have done.
type StaggerShadow struct {
	Label    Label `json:"label"`     // the counts are measured; WouldSaveUSD is estimated
	WindowMs int64 `json:"window_ms"` // spawns of one type this close together are siblings
	CapMs    int64 `json:"cap_ms"`    // the longest a sibling would have been held
	Spawns   int   `json:"spawns"`
	Groups   int   `json:"sibling_groups"`
	Later    int   `json:"later_siblings"` // the siblings the stagger would have held
	// Matched is the later siblings whose first request is in the history yet; the figures below cover only them.
	Matched int `json:"matched"`
	// AlreadyRead is matched later siblings whose first request read from the cache anyway.
	AlreadyRead int `json:"already_read"`
	// WithinCap is matched later siblings that wrote and whose first sibling replied within the cap, so the hold
	// would have let them read.
	WithinCap   int   `json:"within_cap"`
	WriteTokens int64 `json:"later_write_tokens"` // all matched later siblings' first writes: measured
	// HoldMs is the wait each WithinCap sibling would have had, total and median (upper bound).
	HoldMsTotal  int64   `json:"hold_ms_total"`
	HoldMsMedian int64   `json:"hold_ms_median"`
	WouldSaveUSD float64 `json:"would_save_usd"` // estimated upper bound, at list prices
	Unpriced     int     `json:"unpriced"`       // within-cap siblings whose model has no price
	Text         string  `json:"text"`
}

type spawnRow struct {
	session, typ, typeKey string
	ts                    int64
}

type staggerRun struct {
	agent string
	start int64
	first *firstReq
	used  bool
}

type firstReq struct {
	ts, read, w5, w1, wOther int64
	model                    string
}

// LoadStaggerShadow reads the spawns, runs and first requests and computes the shadow.
func LoadStaggerShadow(ctx context.Context, db *store.DB) (StaggerShadow, error) {
	spawns, err := loadSpawns(ctx, db)
	if err != nil {
		return StaggerShadow{}, err
	}
	runs, err := loadStaggerRuns(ctx, db)
	if err != nil {
		return StaggerShadow{}, err
	}
	tbl, err := pricing.Default()
	if err != nil {
		return StaggerShadow{}, err
	}
	return staggerShadow(spawns, runs, tbl, StaggerWindow, StaggerCap), nil
}

func loadSpawns(ctx context.Context, db *store.DB) ([]spawnRow, error) {
	rows, err := db.QueryContext(ctx, `SELECT session_key, agent_type, type_key, ts FROM subagent_spawns ORDER BY session_key, ts`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []spawnRow
	for rows.Next() {
		var s spawnRow
		if err := rows.Scan(&s.session, &s.typ, &s.typeKey, &s.ts); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// loadStaggerRuns returns each session's runs by agent type, with the first request of each run.
func loadStaggerRuns(ctx context.Context, db *store.DB) (map[string][]*staggerRun, error) {
	rows, err := db.QueryContext(ctx, `SELECT r.session_key, r.agent_type, r.agent_key, r.started_at,
		h.ts, h.model, h.cache_read, h.cache_write_5m, h.cache_write_1h, h.cache_write_other
		FROM subagent_runs r LEFT JOIN history_requests h ON h.session_key = r.session_key AND h.agent_key = r.agent_key
			AND h.ts = (SELECT MIN(ts) FROM history_requests x WHERE x.session_key = r.session_key AND x.agent_key = r.agent_key)
		WHERE r.started_at > 0 AND r.session_key IN (SELECT session_key FROM subagent_spawns)
		ORDER BY r.session_key, r.agent_type, r.started_at`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string][]*staggerRun{}
	for rows.Next() {
		var sess, typ, agent string
		var start int64
		var ts, read, w5, w1, wo sql.NullInt64
		var model sql.NullString
		if err := rows.Scan(&sess, &typ, &agent, &start, &ts, &model, &read, &w5, &w1, &wo); err != nil {
			return nil, err
		}
		k := sess + "\x00" + typ
		if n := len(out[k]); n > 0 && out[k][n-1].agent == agent {
			continue // two requests logged in the same millisecond
		}
		r := &staggerRun{agent: agent, start: start}
		if ts.Valid {
			r.first = &firstReq{ts: ts.Int64, model: model.String, read: read.Int64, w5: w5.Int64, w1: w1.Int64, wOther: wo.Int64}
		}
		out[k] = append(out[k], r)
	}
	return out, rows.Err()
}

// staggerShadow groups spawns of one session and type that each come within window of the previous one, matches
// every spawn to the next unmatched run of that type, and judges each later sibling against the group's first.
func staggerShadow(spawns []spawnRow, runs map[string][]*staggerRun, tbl *pricing.Table, window, limit time.Duration) StaggerShadow {
	s := StaggerShadow{Label: Measured, WindowMs: window.Milliseconds(), CapMs: limit.Milliseconds(), Spawns: len(spawns)}
	sorted := append([]spawnRow(nil), spawns...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.session != b.session {
			return a.session < b.session
		}
		return a.ts < b.ts
	})
	type member struct {
		spawn spawnRow
		run   *staggerRun
	}
	groups := map[string][]member{} // open group per session and type
	var holds []int64
	var save float64
	judge := func(g []member) {
		if len(g) < 2 {
			return
		}
		s.Groups++
		s.Later += len(g) - 1
		first := g[0].run
		for _, m := range g[1:] {
			if m.run == nil || m.run.first == nil {
				continue
			}
			s.Matched++
			f := m.run.first
			write := f.w5 + f.w1 + f.wOther
			s.WriteTokens += write
			if f.read > 0 {
				s.AlreadyRead++
				continue
			}
			if first == nil || first.first == nil || write == 0 {
				continue
			}
			hold := first.first.ts - m.spawn.ts
			if hold > limit.Milliseconds() {
				continue
			}
			s.WithinCap++
			hold = max(hold, 0)
			holds = append(holds, hold)
			s.HoldMsTotal += hold
			fw := first.first.w5 + first.first.w1 + first.first.wOther
			if usd, ok := shareSaving(tbl, f, min(write, fw)); ok {
				save += usd
			} else {
				s.Unpriced++
			}
		}
	}
	for _, sp := range sorted {
		gk := sp.session + "\x00" + sp.typ + "\x00" + sp.typeKey
		if g := groups[gk]; len(g) > 0 && sp.ts-g[len(g)-1].spawn.ts > window.Milliseconds() {
			judge(g)
			delete(groups, gk)
		}
		var run *staggerRun
		for _, r := range runs[sp.session+"\x00"+sp.typ] {
			if !r.used && r.start >= sp.ts && r.start-sp.ts <= staggerMatch.Milliseconds() {
				r.used = true
				run = r
				break
			}
		}
		groups[gk] = append(groups[gk], member{spawn: sp, run: run})
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		judge(groups[k])
	}
	if len(holds) > 0 {
		sort.Slice(holds, func(i, j int) bool { return holds[i] < holds[j] })
		s.HoldMsMedian = holds[len(holds)/2]
	}
	s.WouldSaveUSD = save
	s.Text = staggerText(s)
	return s
}

// shareSaving prices n tokens of the sibling's first request read from the cache instead of written, at the
// TTL that request wrote most of.
func shareSaving(tbl *pricing.Table, f *firstReq, n int64) (float64, bool) {
	if tbl == nil || n <= 0 {
		return 0, false
	}
	ttl := "5m"
	if f.w1 > f.w5 {
		ttl = "1h"
	}
	at := time.UnixMilli(f.ts).UTC()
	wrote := tbl.Cost(pricing.Request{Provider: pricing.Anthropic, Model: f.model, Time: at, Usage: pricing.Usage{CacheWrite: map[string]int64{ttl: n}}})
	read := tbl.Cost(pricing.Request{Provider: pricing.Anthropic, Model: f.model, Time: at, Usage: pricing.Usage{CacheRead: n}})
	if wrote.Status != pricing.Priced || read.Status != pricing.Priced {
		return 0, false
	}
	return wrote.Total.Sub(read.Total).Float64(), true
}

func staggerText(s StaggerShadow) string {
	switch {
	case s.Spawns == 0:
		return "No subagent calls recorded yet. The hooks record them from now on."
	case s.Later == 0:
		return fmt.Sprintf("%d subagent calls recorded; none started alongside a sibling of the same type.", s.Spawns)
	case s.Matched == 0:
		return fmt.Sprintf("%d later siblings found; their requests are not in the history yet. Run trove logs scan.", s.Later)
	}
	return fmt.Sprintf("%d of %d later siblings (measured) wrote the shared prefix instead of reading it; a hold of up to %ds "+
		"would have reached %d of them, with a median wait of %.1fs, for at most $%.2f (estimated, list prices).",
		s.Matched-s.AlreadyRead, s.Matched, s.CapMs/1000, s.WithinCap, float64(s.HoldMsMedian)/1000, s.WouldSaveUSD)
}
