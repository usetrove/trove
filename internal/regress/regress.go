// Package regress is the regression watch: it catches a Claude Code release, or a change on the
// provider's side, that suddenly makes the same work burn more. It compares each recent Claude Code
// version, and each recent day, with the user's own earlier history on four numbers: the rate of
// unexplained cache rebuilds, thinking tokens per request, the share of requests with thinking, and
// percentage points of the 5-hour limit per 1,000 output-equivalent tokens. See docs/regress.md.
//
// It works on numbers only (counts, token sizes, version strings, timestamps) and changes nothing:
// every alert carries advice for the user to act on, never an action. Pure and deterministic: the
// same history gives the same result in any order.
package regress

import (
	"math"
	"sort"
	"time"
)

// Thresholds. Starting values to tune (docs/regress.md): an alert needs all of them.
const (
	// MinRequests and MinSessions: the version or day being checked needs at least this much data.
	MinRequests = 200
	MinSessions = 3
	// MinMADs: the new value must sit at least this many robust standard deviations (1.4826 x the
	// median absolute deviation of the baseline's daily values) above the baseline median.
	MinMADs = 3.5
	// MinRelChange: and at least this much above it, relatively (+30%).
	MinRelChange = 0.30
)

// Supporting limits, also starting values.
const (
	// MinIntervals replaces MinRequests for the plan-limit metric, whose unit is a meter interval.
	MinIntervals = 10
	// BaselineDays of history before the version or day form its baseline, one block per day.
	BaselineDays = 28
	// MinBaselineBlocks: baseline days with enough data (MinBlockRequests, or MinBlockIntervals for the
	// plan-limit metric) needed before anything is compared.
	MinBaselineBlocks = 5
	MinBlockRequests  = 20
	MinBlockIntervals = 2
	// RecentDays: the days checked, today included. RecentVersions: the newest versions checked, each only
	// while it was seen in the last VersionActiveDays.
	RecentDays        = 2
	RecentVersions    = 3
	VersionActiveDays = 7
	// MinActiveRequests: a version counts as active on a day, for telling the client from the server,
	// with at least this many requests that day.
	MinActiveRequests = 30
	// minScaleRel keeps a perfectly flat baseline from making a tiny move look huge: the scale is at
	// least 5% of the baseline median.
	minScaleRel = 0.05
)

// Metrics.
const (
	MetricRebuilds      = "unexplained_rebuild_rate"
	MetricThinkTokens   = "thinking_tokens_per_request"
	MetricThinkShare    = "thinking_request_share"
	MetricLimitPerKOE   = "limit_pct_per_1k_output_equivalent"
	BasisAPIPrices      = "estimated from API prices"
	LabelMeasured       = "measured"
	LabelEstimated      = "estimated"
	KindVersion         = "version"
	KindDay             = "day"
	CauseClient         = "client"
	CauseServer         = "server"
	CauseUnclear        = "unclear"
	StatusAlert         = "alert"
	StatusNormal        = "normal"
	StatusTooLittleData = "too_little_data"
	StatusNoBaseline    = "no_baseline"
)

// Metrics lists the metrics in output order.
var Metrics = []string{MetricRebuilds, MetricThinkTokens, MetricThinkShare, MetricLimitPerKOE}

// absFloor is the smallest scale per metric, so a baseline of zeros does not alert on one event.
var absFloor = map[string]float64{
	MetricRebuilds:    0.005, // half a point of requests
	MetricThinkTokens: 20,
	MetricThinkShare:  0.02,
	MetricLimitPerKOE: 1e-6,
}

// Request is one Claude Code request as the watch sees it.
type Request struct {
	TS               int64 // unix ms
	Session          string
	Version          string // "" when the log did not give it
	Unexplained      bool   // its cache write was an unexplained rebuild (analyze.CauseUnexplained)
	Split            bool   // its output could be split, so Thinking means something
	Thinking         int64
	ThinkingMeasured bool
}

// Interval is one clean meter interval of the 5-hour limit with this machine's tokens in it.
type Interval struct {
	Start, End int64   // unix ms
	Delta      float64 // percentage points the meter moved (measured)
	OETokens   float64 // output-equivalent tokens in it (estimated from API prices)
	Version    string  // the one version its requests ran on; "" when mixed or unknown
	Sessions   []string
}

// Data is everything Compute needs.
type Data struct {
	Requests  []Request
	Intervals []Interval
	Now       time.Time
	Loc       *time.Location // the user's days; nil = UTC
}

// Check is one comparison, alerting or not.
type Check struct {
	Kind     string  `json:"kind"`
	Key      string  `json:"key"` // the version, or the day (YYYY-MM-DD)
	Metric   string  `json:"metric"`
	Status   string  `json:"status"`
	Value    float64 `json:"value"`
	Baseline float64 `json:"baseline"`
	MADs     float64 `json:"mads"`
	Change   float64 `json:"relative_change"` // +0.48 = 48% above the baseline; +Inf shows as 1e9
	N        int     `json:"n"`               // requests, or meter intervals for the plan-limit metric
	Sessions int     `json:"sessions"`
	Blocks   int     `json:"baseline_days"`
	Label    string  `json:"label"`
	Basis    string  `json:"basis,omitempty"`
}

// Alert is a check that tripped, with its cause and the advice for the user.
type Alert struct {
	Check
	Cause    string   `json:"cause"`
	Version  string   `json:"version,omitempty"`  // the version blamed or checked
	Previous string   `json:"previous,omitempty"` // the version before it, to pin
	Active   []string `json:"active_versions,omitempty"`
	Sentence string   `json:"sentence"`
	Advice   string   `json:"advice"`
}

// Result is the watch's output.
type Result struct {
	ComputedAt int64   `json:"computed_at"`
	Alerts     []Alert `json:"alerts"`
	Checks     []Check `json:"checks"`
}

// cell accumulates one group.
type cell struct {
	reqs, unexplained, split, withThinking int
	thinking                               int64
	allMeasured                            bool
	sessions                               map[string]bool
	intervals                              int
	delta, oe                              float64
}

func newCell() *cell { return &cell{allMeasured: true, sessions: map[string]bool{}} }

func (c *cell) addReq(r *Request) {
	c.reqs++
	c.sessions[r.Session] = true
	if r.Unexplained {
		c.unexplained++
	}
	if r.Split {
		c.split++
		c.thinking += r.Thinking
		if r.Thinking > 0 {
			c.withThinking++
		}
		if !r.ThinkingMeasured {
			c.allMeasured = false
		}
	}
}

func (c *cell) addIv(iv *Interval) {
	c.intervals++
	c.delta += iv.Delta
	c.oe += iv.OETokens
	for _, s := range iv.Sessions {
		c.sessions[s] = true
	}
}

// value is the metric's value and its sample size; ok is false when the cell has none of it.
func (c *cell) value(m string) (v float64, n int, ok bool) {
	switch m {
	case MetricRebuilds:
		if c.reqs == 0 {
			return 0, 0, false
		}
		return float64(c.unexplained) / float64(c.reqs), c.reqs, true
	case MetricThinkTokens:
		if c.split == 0 {
			return 0, 0, false
		}
		return float64(c.thinking) / float64(c.split), c.split, true
	case MetricThinkShare:
		if c.split == 0 {
			return 0, 0, false
		}
		return float64(c.withThinking) / float64(c.split), c.split, true
	case MetricLimitPerKOE:
		if c.intervals == 0 || c.oe <= 0 {
			return 0, 0, false
		}
		return c.delta / c.oe * 1000, c.intervals, true
	}
	return 0, 0, false
}

func minN(m string) int {
	if m == MetricLimitPerKOE {
		return MinIntervals
	}
	return MinRequests
}

func minBlock(m string) int {
	if m == MetricLimitPerKOE {
		return MinBlockIntervals
	}
	return MinBlockRequests
}

func label(m string, c *cell) (string, string) {
	switch m {
	case MetricLimitPerKOE:
		return LabelEstimated, BasisAPIPrices
	case MetricThinkTokens, MetricThinkShare:
		if !c.allMeasured {
			return LabelEstimated, ""
		}
	}
	return LabelMeasured, ""
}

// grouping holds the history split by day and by version.
type grouping struct {
	loc  *time.Location
	reqs []Request
	ivs  []Interval
}

func (g *grouping) day(ts int64) string { return time.UnixMilli(ts).In(g.loc).Format("2006-01-02") }

// collect fills one cell from the requests and intervals that keep says yes to (by timestamp and version).
func (g *grouping) collect(keep func(ts int64, version string) bool) *cell {
	c := newCell()
	for i := range g.reqs {
		if keep(g.reqs[i].TS, g.reqs[i].Version) {
			c.addReq(&g.reqs[i])
		}
	}
	for i := range g.ivs {
		if keep(g.ivs[i].End, g.ivs[i].Version) {
			c.addIv(&g.ivs[i])
		}
	}
	return c
}

// blocks returns the metric's daily values over requests and intervals that keep says yes to, one per
// day with enough data.
func (g *grouping) blocks(m string, keep func(ts int64, version string) bool) []float64 {
	days := map[string]*cell{}
	get := func(ts int64) *cell {
		d := g.day(ts)
		if days[d] == nil {
			days[d] = newCell()
		}
		return days[d]
	}
	for i := range g.reqs {
		if keep(g.reqs[i].TS, g.reqs[i].Version) {
			get(g.reqs[i].TS).addReq(&g.reqs[i])
		}
	}
	for i := range g.ivs {
		if keep(g.ivs[i].End, g.ivs[i].Version) {
			get(g.ivs[i].End).addIv(&g.ivs[i])
		}
	}
	keys := make([]string, 0, len(days))
	for k := range days {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []float64
	for _, k := range keys {
		if v, n, ok := days[k].value(m); ok && n >= minBlock(m) {
			out = append(out, v)
		}
	}
	return out
}

// changeTest compares v with the baseline blocks: their median, a robust scale and how far above it v is.
func changeTest(m string, v float64, base []float64) (median, mads, rel float64) {
	median = medianOf(base)
	dev := make([]float64, len(base))
	for i, b := range base {
		dev[i] = math.Abs(b - median)
	}
	scale := math.Max(1.4826*medianOf(dev), math.Max(minScaleRel*math.Abs(median), absFloor[m]))
	mads = (v - median) / scale
	switch {
	case median > 0:
		rel = v/median - 1
	case v > 0:
		rel = math.Inf(1)
	}
	return median, mads, rel
}

func spikes(mads, rel float64) bool { return mads >= MinMADs && rel >= MinRelChange }

func medianOf(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

// check runs one comparison of cell c against base.
func check(kind, key, m string, c *cell, base []float64) Check {
	ch := Check{Kind: kind, Key: key, Metric: m, Blocks: len(base), Sessions: len(c.sessions)}
	ch.Label, ch.Basis = label(m, c)
	v, n, ok := c.value(m)
	ch.Value, ch.N = round(v), n
	switch {
	case !ok || n < minN(m) || ch.Sessions < MinSessions:
		ch.Status = StatusTooLittleData
		return ch
	case len(base) < MinBaselineBlocks:
		ch.Status = StatusNoBaseline
		return ch
	}
	med, mads, rel := changeTest(m, v, base)
	ch.Baseline, ch.MADs, ch.Change = round(med), round(mads), roundRel(rel)
	ch.Status = StatusNormal
	if spikes(mads, rel) {
		ch.Status = StatusAlert
	}
	return ch
}

type version struct {
	name        string
	first, last int64
}

// versions lists the known versions by first appearance.
func (g *grouping) versions() []version {
	by := map[string]*version{}
	for _, r := range g.reqs {
		if r.Version == "" {
			continue
		}
		v := by[r.Version]
		if v == nil {
			v = &version{name: r.Version, first: r.TS, last: r.TS}
			by[r.Version] = v
		}
		v.first, v.last = min(v.first, r.TS), max(v.last, r.TS)
	}
	out := make([]version, 0, len(by))
	for _, v := range by {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].first != out[j].first {
			return out[i].first < out[j].first
		}
		return out[i].name < out[j].name
	})
	return out
}

const dayMS = 24 * 3600 * 1000

// Compute runs every check.
func Compute(d Data) Result {
	loc := d.Loc
	if loc == nil {
		loc = time.UTC
	}
	g := &grouping{loc: loc, reqs: d.Requests, ivs: d.Intervals}
	now := d.Now.UnixMilli()
	res := Result{ComputedAt: now}

	vs := g.versions()
	older := map[string]bool{}
	type key struct{ metric, version string }
	versionAlerts := map[key]bool{}
	start := max(0, len(vs)-RecentVersions)
	for i, v := range vs {
		if i < start || i == 0 || v.last < now-VersionActiveDays*dayMS {
			older[v.name] = true
			continue
		}
		prev := vs[i-1].name
		for _, m := range Metrics {
			name := v.name
			c := g.collect(func(_ int64, ver string) bool { return ver == name })
			base := g.blocks(m, func(ts int64, ver string) bool {
				return older[ver] && ts < v.first && ts >= v.first-BaselineDays*dayMS
			})
			ch := check(KindVersion, v.name, m, c, base)
			res.Checks = append(res.Checks, ch)
			if ch.Status != StatusAlert {
				continue
			}
			// Was an older version running alongside it, and did it spike too?
			co := g.collect(func(ts int64, ver string) bool { return older[ver] && ts >= v.first })
			a := Alert{Check: ch, Version: v.name, Previous: prev, Cause: CauseUnclear}
			if cv, n, ok := co.value(m); ok && n >= MinActiveRequests {
				_, mads, rel := changeTest(m, cv, base)
				a.Cause = CauseClient
				if spikes(mads, rel) {
					a.Cause = CauseServer
				}
			}
			versionAlerts[key{m, v.name}] = a.Cause == CauseClient
			res.Alerts = append(res.Alerts, a)
		}
		older[v.name] = true
	}

	prevOf := map[string]string{}
	for i := 1; i < len(vs); i++ {
		prevOf[vs[i].name] = vs[i-1].name
	}
	today := time.UnixMilli(now).In(loc)
	for k := RecentDays - 1; k >= 0; k-- {
		dt := time.Date(today.Year(), today.Month(), today.Day()-k, 0, 0, 0, 0, loc)
		from, to := dt.UnixMilli(), dt.AddDate(0, 0, 1).UnixMilli()
		day := dt.Format("2006-01-02")
		inDay := func(ts int64, _ string) bool { return ts >= from && ts < to }
		active := g.activeOn(from, to)
		for _, m := range Metrics {
			c := g.collect(inDay)
			base := g.blocks(m, func(ts int64, _ string) bool { return ts < from && ts >= from-BaselineDays*dayMS })
			ch := check(KindDay, day, m, c, base)
			res.Checks = append(res.Checks, ch)
			if ch.Status != StatusAlert {
				continue
			}
			a := Alert{Check: ch, Cause: CauseUnclear, Active: active}
			if len(active) >= 2 {
				var spiked []string
				for _, ver := range active {
					name := ver
					vc := g.collect(func(ts int64, vv string) bool { return vv == name && ts >= from && ts < to })
					if v, _, ok := vc.value(m); ok {
						if _, mads, rel := changeTest(m, v, base); spikes(mads, rel) {
							spiked = append(spiked, ver)
						}
					}
				}
				switch {
				case len(spiked) >= 2:
					a.Cause = CauseServer
				case len(spiked) == 1:
					a.Cause, a.Version, a.Previous = CauseClient, spiked[0], prevOf[spiked[0]]
				}
			} else if len(active) == 1 {
				a.Version, a.Previous = active[0], prevOf[active[0]]
			}
			if a.Cause == CauseClient && versionAlerts[key{m, a.Version}] {
				continue // the version's own alert already says it
			}
			res.Alerts = append(res.Alerts, a)
		}
	}
	for i := range res.Alerts {
		res.Alerts[i].Sentence, res.Alerts[i].Advice = Sentence(res.Alerts[i]), Advice(res.Alerts[i])
	}
	sortAlerts(res.Alerts)
	return res
}

// activeOn lists the versions with at least MinActiveRequests requests in [from, to), sorted.
func (g *grouping) activeOn(from, to int64) []string {
	n := map[string]int{}
	for _, r := range g.reqs {
		if r.Version != "" && r.TS >= from && r.TS < to {
			n[r.Version]++
		}
	}
	var out []string
	for v, c := range n {
		if c >= MinActiveRequests {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

// sortAlerts puts the strongest first, deterministically.
func sortAlerts(as []Alert) {
	sort.SliceStable(as, func(i, j int) bool {
		if as[i].MADs != as[j].MADs {
			return as[i].MADs > as[j].MADs
		}
		if as[i].Kind != as[j].Kind {
			return as[i].Kind < as[j].Kind
		}
		if as[i].Key != as[j].Key {
			return as[i].Key < as[j].Key
		}
		return as[i].Metric < as[j].Metric
	})
}

func round(v float64) float64 { return math.Round(v*1e6) / 1e6 }

// roundRel keeps +Inf out of JSON.
func roundRel(v float64) float64 {
	if math.IsInf(v, 1) {
		return 1e9
	}
	return math.Round(v*1e4) / 1e4
}
