package measure

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/usetrove/trove/internal/store"
)

// Effort cache-break check (plan item 7.4). Lowering effort on tool turns is only
// worth trying if a change of effort keeps the cached prefix. The provider says it
// "can invalidate" cached message blocks, and on some models the system and tool
// caches too (platform.claude.com/docs/en/build-with-claude/effort and
// /prompt-caching, read 2026-11-17), without saying which models. So Trove measures
// it per model from the usage the provider reports, before anything changes effort.
//
// Method: within one session and one model, take consecutive successful requests
// less than effortMaxGap apart whose earlier request had a cached prefix of at least
// effortMinCached tokens. The later request "kept" the cache when it read back at
// least effortKeptShare of what the earlier one read or wrote. Pairs where effort
// changed are compared with pairs where it did not (the control), so ordinary misses
// (compaction, edits to earlier turns) are not blamed on effort.

const (
	effortMaxGap    = 4 * time.Minute // inside the 5-minute default cache lifetime
	effortMinCached = 1024
	effortKeptShare = 0.9
	// EffortMinPairs is the fewest changed and control pairs a verdict needs.
	EffortMinPairs = 5
)

// Effort cache verdicts.
const (
	EffortBreaks       = "breaks"       // a change of effort loses the cached prefix
	EffortKeeps        = "keeps"        // a change of effort keeps it as often as the control
	EffortUnclear      = "unclear"      // in between: not safe to lower effort mid-session
	EffortInsufficient = "insufficient" // not enough pairs yet
)

// EffortCacheModel is the check's result for one model.
type EffortCacheModel struct {
	Model         string `json:"model"`
	ChangedPairs  int    `json:"changed_pairs"`
	ChangedBroken int    `json:"changed_broken"`
	ControlPairs  int    `json:"control_pairs"`
	ControlBroken int    `json:"control_broken"`
	Verdict       string `json:"verdict"`
	Label         Label  `json:"label"`
	Text          string `json:"text"`
}

type effortRow struct {
	session, model, effort string
	ts                     int64
	read, write            int64
}

// EffortCacheCheck runs the check over requests recorded in [from, to).
func EffortCacheCheck(ctx context.Context, db *store.DB, from, to time.Time) ([]EffortCacheModel, error) {
	rows, err := db.QueryContext(ctx, `SELECT session_key, model, effort, ts, cache_read_tokens, cache_write_tokens
		FROM requests WHERE provider = 'anthropic' AND status_code >= 200 AND status_code < 300 AND session_key != ''
		AND ts >= ? AND ts < ? ORDER BY session_key, ts, id`, from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, err
	}
	var rs []effortRow
	for rows.Next() {
		var r effortRow
		if err := rows.Scan(&r.session, &r.model, &r.effort, &r.ts, &r.read, &r.write); err != nil {
			_ = rows.Close()
			return nil, err
		}
		rs = append(rs, r)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return effortCache(rs), nil
}

func effortCache(rs []effortRow) []EffortCacheModel {
	by := map[string]*EffortCacheModel{}
	last := map[string]effortRow{} // session+model -> previous request
	for _, r := range rs {
		k := r.session + "\x00" + r.model
		prev, ok := last[k]
		last[k] = r
		if !ok || r.ts-prev.ts > effortMaxGap.Milliseconds() {
			continue
		}
		cached := prev.read + prev.write
		if cached < effortMinCached {
			continue
		}
		m := by[r.model]
		if m == nil {
			m = &EffortCacheModel{Model: r.model, Label: Measured}
			by[r.model] = m
		}
		broken := float64(r.read) < effortKeptShare*float64(cached)
		if prev.effort != r.effort {
			m.ChangedPairs++
			if broken {
				m.ChangedBroken++
			}
		} else {
			m.ControlPairs++
			if broken {
				m.ControlBroken++
			}
		}
	}
	out := make([]EffortCacheModel, 0, len(by))
	for _, m := range by {
		m.Verdict, m.Text = effortVerdict(*m)
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}

func effortVerdict(m EffortCacheModel) (string, string) {
	if m.ChangedPairs < EffortMinPairs || m.ControlPairs < EffortMinPairs {
		return EffortInsufficient, fmt.Sprintf("%s: %d turns changed effort and %d did not; at least %d of each are needed. Lowering effort mid-session stays off.",
			m.Model, m.ChangedPairs, m.ControlPairs, EffortMinPairs)
	}
	changed := float64(m.ChangedBroken) / float64(m.ChangedPairs)
	control := float64(m.ControlBroken) / float64(m.ControlPairs)
	pct := func(f float64) int { return int(f*100 + 0.5) }
	switch {
	case changed-control >= 0.5:
		return EffortBreaks, fmt.Sprintf("%s: changing effort lost the cached prefix on %d%% of turns, against %d%% when it did not change (measured). Lowering effort mid-session would cost more than it saves.",
			m.Model, pct(changed), pct(control))
	case changed <= control+0.1:
		return EffortKeeps, fmt.Sprintf("%s: changing effort kept the cached prefix as often as not changing it (%d%% lost against %d%%, measured). Lowering effort on tool turns may go to a session A/B.",
			m.Model, pct(changed), pct(control))
	}
	return EffortUnclear, fmt.Sprintf("%s: changing effort lost the cached prefix on %d%% of turns, against %d%% otherwise (measured). Not clear enough to lower effort mid-session.",
		m.Model, pct(changed), pct(control))
}
