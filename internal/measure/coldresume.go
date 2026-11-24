package measure

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/store"
)

// ColdResume is what the cold-resume guard (hook_cold_resume) did in a window and what followed. Counts come from
// the hook's own events (measured). Tokens avoided are the rebuild sizes of sessions the user cleared after a
// block, less the note they may have pasted (estimated). The hour-after comparison reads Claude Code's own 5-hour
// meter (measured), but its two sides come from different periods, so it is not a randomized comparison.
type ColdResume struct {
	Blocked     int `json:"blocked"`
	WouldBlock  int `json:"would_block"`
	Advised     int `json:"advised"`
	WouldAdvise int `json:"would_advise"`
	Overridden  int `json:"overridden"`
	Cleared     int `json:"cleared"`
	// OverriddenShare and ClearedShare are percentages of blocked prompts (measured); nil with no block.
	OverriddenShare *Figure `json:"overridden_share_percent,omitempty"`
	ClearedShare    *Figure `json:"cleared_share_percent,omitempty"`
	// TokensAvoided is estimated: the cleared sessions' rebuilds, less the largest note each could have pasted.
	TokensAvoided Figure `json:"tokens_avoided"`
	// HourAfter compares the 5-hour meter's movement in the hour after a block with the hour after a prompt the
	// guard would have stopped in shadow. Nil until both sides have MinColdResumeArm matched events.
	HourAfter *HourAfter `json:"hour_after,omitempty"`
	// HourAfterNote says why HourAfter is missing, or what it cannot see.
	HourAfterNote string `json:"hour_after_note,omitempty"`
}

// HourAfter is the mean movement of the 5-hour meter, in percentage points, in the hour after each event.
type HourAfter struct {
	Guard  Figure `json:"guard_points"`  // after a block
	Shadow Figure `json:"shadow_points"` // after a would-block in shadow
	// Diff is Guard - Shadow, matched by rebuild size, with a 95% interval. Negative means less of the limit used.
	Diff    Figure  `json:"difference_points"`
	DiffLow float64 `json:"difference_low"`
	DiffHi  float64 `json:"difference_high"`
}

// MinColdResumeArm is how many matched events each side needs before the hour-after comparison is shown.
const MinColdResumeArm = 10

// ColdNoteTokens is the most a pasted note can cost; it must equal the registry's declared overhead for
// hook_cold_resume (TestColdNoteTokensMatchTheRegistry keeps them in step).
const ColdNoteTokens = 450

// coldBuckets match blocks with shadow events of a similar rebuild size, in tokens: below the default threshold
// (stopped for its share of the weekly limit), up to twice it, and above.
var coldBuckets = []int64{150000, 300000}

func coldBucket(tokens int64) int {
	for i, b := range coldBuckets {
		if tokens < b {
			return i
		}
	}
	return len(coldBuckets)
}

// ColdResumeSummary reads the cold-resume guard's events in w. It returns nil when the guard never did anything there.
func ColdResumeSummary(ctx context.Context, db *store.DB, w Window) (*ColdResume, error) {
	return GuardSummary(ctx, db, w, config.OptimizerHookColdResume)
}

// GuardSummary reads one rebuild guard's events in w: hook_cold_resume, hook_model_switch or hook_cold_compact. The
// figures mean the same for each; "stopped" is a warning for hook_model_switch, which never stops anything. It
// returns nil when the guard never did anything there.
func GuardSummary(ctx context.Context, db *store.DB, w Window, optimizer string) (*ColdResume, error) {
	rows, err := db.QueryContext(ctx, `SELECT ts, kind, tokens FROM hook_events WHERE hook = ? AND ts >= ? AND ts < ? ORDER BY ts`,
		optimizer, w.From.UnixMilli(), w.To.UnixMilli())
	if err != nil {
		return nil, err
	}
	type ev struct {
		ts     int64
		tokens int64
	}
	var guard, shadow []ev
	c := &ColdResume{}
	var avoided int64
	for rows.Next() {
		var e ev
		var kind string
		if err := rows.Scan(&e.ts, &kind, &e.tokens); err != nil {
			_ = rows.Close()
			return nil, err
		}
		switch kind {
		case "blocked":
			c.Blocked++
			guard = append(guard, e)
		case "would_block":
			c.WouldBlock++
			shadow = append(shadow, e)
		case "advised":
			c.Advised++
		case "would_advise":
			c.WouldAdvise++
		case "overridden":
			c.Overridden++
		case "cleared":
			c.Cleared++
			avoided += max(0, e.tokens-ColdNoteTokens)
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if c.Blocked+c.WouldBlock+c.Advised+c.WouldAdvise == 0 {
		return nil, nil
	}
	c.TokensAvoided = Figure{Value: float64(avoided), Label: Estimated, N: c.Cleared}
	acted := c.Blocked
	if optimizer == config.OptimizerHookModelSwitch {
		acted = c.Advised // it only ever warns; "overridden" means the switch went ahead
	}
	if acted > 0 {
		c.OverriddenShare = &Figure{Value: 100 * float64(c.Overridden) / float64(acted), Label: Measured, N: acted}
		c.ClearedShare = &Figure{Value: 100 * float64(c.Cleared) / float64(acted), Label: Measured, N: acted}
	}

	readings, err := fiveHourReadings(ctx, db, w)
	if err != nil {
		return nil, err
	}
	moved := func(es []ev) [][]float64 {
		out := make([][]float64, len(coldBuckets)+1)
		for _, e := range es {
			if d, ok := hourMovement(readings, e.ts); ok {
				b := coldBucket(e.tokens)
				out[b] = append(out[b], d)
			}
		}
		return out
	}
	g, s := moved(guard), moved(shadow)
	c.HourAfter, c.HourAfterNote = compareHourAfter(g, s)
	return c, nil
}

// reading is one 5-hour meter value from the status line.
type reading struct {
	ts, resets int64
	pct        float64
}

func fiveHourReadings(ctx context.Context, db *store.DB, w Window) ([]reading, error) {
	rows, err := db.QueryContext(ctx, `SELECT ts, five_hour_pct, five_hour_resets_at FROM limit_samples
		WHERE five_hour_pct IS NOT NULL AND five_hour_resets_at IS NOT NULL AND ts >= ? AND ts < ? ORDER BY ts`,
		w.From.Add(-6*time.Hour).UnixMilli(), w.To.Add(time.Hour).UnixMilli())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []reading
	for rows.Next() {
		var r reading
		if err := rows.Scan(&r.ts, &r.pct, &r.resets); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// hourMovement is how far the 5-hour meter moved between its last reading before ts and its last reading up to an
// hour after. The meter is account-wide and recorded only when it changes, so "no newer reading" means no change
// the status line saw. ok is false without a reading before ts, or when the window reset in between (the two
// readings must share a reset time, within a minute), or when the meter went down.
func hourMovement(rs []reading, ts int64) (float64, bool) {
	at := func(t int64) (reading, bool) {
		i := sort.Search(len(rs), func(i int) bool { return rs[i].ts > t })
		if i == 0 {
			return reading{}, false
		}
		return rs[i-1], true
	}
	before, ok1 := at(ts)
	after, ok2 := at(ts + 3600_000)
	if !ok1 || !ok2 || math.Abs(float64(before.resets-after.resets)) > 60_000 || after.pct < before.pct {
		return 0, false
	}
	return after.pct - before.pct, true
}

// compareHourAfter averages each side and takes the difference within each size bucket, weighted by the blocks
// in it, with a normal 95% interval. Buckets with no event on one side are left out.
func compareHourAfter(g, s [][]float64) (*HourAfter, string) {
	var ng, ns int
	var sumG, sumS float64
	var wsum, diff, varSum float64
	for b := range g {
		for _, v := range g[b] {
			sumG += v
		}
		for _, v := range s[b] {
			sumS += v
		}
		if len(g[b]) == 0 || len(s[b]) == 0 {
			continue
		}
		ng += len(g[b])
		ns += len(s[b])
		mg, vg := meanVar(g[b])
		ms, vs := meanVar(s[b])
		w := float64(len(g[b]))
		wsum += w
		diff += w * (mg - ms)
		varSum += w * w * (vg/float64(len(g[b])) + vs/float64(len(s[b])))
	}
	if ng < MinColdResumeArm || ns < MinColdResumeArm {
		return nil, fmt.Sprintf("Not enough sessions yet to compare the hour after: %d stopped and %d shadow events with meter readings in a matching size; %d of each are needed.",
			ng, ns, MinColdResumeArm)
	}
	d := diff / wsum
	se := math.Sqrt(varSum) / wsum
	totalG, totalS := 0, 0
	for b := range g {
		totalG += len(g[b])
		totalS += len(s[b])
	}
	return &HourAfter{
		Guard:  Figure{Value: sumG / float64(max(1, totalG)), Label: Measured, N: totalG},
		Shadow: Figure{Value: sumS / float64(max(1, totalS)), Label: Measured, N: totalS},
		Diff:   Figure{Value: d, Label: Measured, N: ng + ns}, DiffLow: d - 1.96*se, DiffHi: d + 1.96*se,
	}, "The shadow prompts come from a different period than the stopped ones, so this is not a randomized comparison. Meter changes no status line saw count as none."
}

func meanVar(xs []float64) (mean, variance float64) {
	for _, x := range xs {
		mean += x
	}
	mean /= float64(len(xs))
	if len(xs) < 2 {
		return mean, 0
	}
	for _, x := range xs {
		variance += (x - mean) * (x - mean)
	}
	return mean, variance / float64(len(xs)-1)
}
