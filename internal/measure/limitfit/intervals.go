package limitfit

import (
	"regexp"
	"sort"
	"strings"
)

// Token types, in the order of every weight vector.
const (
	TInput = iota
	TOutput
	TCacheRead
	TWrite5m
	TWrite1h
	NTypes
)

// TypeNames are the token types' names in output.
var TypeNames = [NTypes]string{"input", "output", "cache_read", "cache_write_5m", "cache_write_1h"}

// Meters.
const (
	FiveHour = "five_hour"
	SevenDay = "seven_day"
)

// Sample is one reading of Claude Code's plan-usage meter.
type Sample struct {
	TS            int64 // unix ms
	Session       string
	FiveHour      *float64 // percent used
	FiveHourReset *int64   // unix ms
	Week          *float64
	WeekReset     *int64
}

// Request is one Claude Code request from this machine's history.
type Request struct {
	TS      int64 // unix ms
	Session string
	Model   string
	Tokens  [NTypes]int64
	USD     float64 // API list price (estimated); meaningful only when Priced
	Priced  bool
}

// Interval is the meter's movement between two readings in one reset window, with this machine's
// tokens in between.
type Interval struct {
	Start, End int64          // unix ms
	Window     int64          // the window's reset time, unix ms
	Block      int64          // bootstrap block: the reset window (5h) or the day (week)
	Delta      float64        // percentage points the meter moved
	Tokens     []FamilyTokens // sorted by family, so sums always run in the same order
	USD        float64
	Priced     bool // every request in it had a list price
	Clean      bool
	Why        string // why it is not clean
}

// FamilyTokens is one model family's tokens by type.
type FamilyTokens struct {
	Family string
	T      [NTypes]float64
}

func (iv *Interval) total() float64 {
	var s float64
	for _, ft := range iv.Tokens {
		for _, v := range ft.T {
			s += v
		}
	}
	return s
}

// add adds tokens to family f, keeping Tokens sorted.
func (iv *Interval) add(f string, tok [NTypes]int64) {
	i := sort.Search(len(iv.Tokens), func(i int) bool { return iv.Tokens[i].Family >= f })
	if i == len(iv.Tokens) || iv.Tokens[i].Family != f {
		iv.Tokens = append(iv.Tokens, FamilyTokens{})
		copy(iv.Tokens[i+1:], iv.Tokens[i:])
		iv.Tokens[i] = FamilyTokens{Family: f}
	}
	for k, v := range tok {
		iv.Tokens[i].T[k] += float64(v)
	}
}

// meterRules are the interval rules per meter.
type meterRules struct {
	minDelta float64 // merge raw intervals until the meter moved at least this many points
	maxSpan  int64   // a merged interval longer than this (ms) is dropped: too much can happen unseen
	dayBlock bool    // bootstrap by day instead of by reset window
}

var rules = map[string]meterRules{
	FiveHour: {minDelta: 3, maxSpan: 2 * 3600_000},
	SevenDay: {minDelta: 1, maxSpan: 12 * 3600_000, dayBlock: true},
}

// Reasons an interval is not clean.
const (
	whyNotRead  = "history_not_read"  // history has not been read up to the interval's end
	whyUnknown  = "unmetered_session" // tokens from a session that never sent the meter (maybe an API key)
	whyTooLong  = "too_long"
	whyDropped  = "meter_dropped"
	whyTooSmall = "too_small" // the window reset before the meter moved enough
)

const resetTolerance = 60_000 // ms: two readings belong to one window when their reset times are this close

// reading is one meter's value at one time.
type reading struct {
	ts    int64
	pct   float64
	reset int64
}

func readings(samples []Sample, meter string) []reading {
	var out []reading
	for _, s := range samples {
		p, r := s.FiveHour, s.FiveHourReset
		if meter == SevenDay {
			p, r = s.Week, s.WeekReset
		}
		if p == nil || r == nil {
			continue
		}
		out = append(out, reading{s.TS, *p, *r})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.ts != b.ts {
			return a.ts < b.ts
		}
		if a.reset != b.reset {
			return a.reset < b.reset
		}
		return a.pct < b.pct
	})
	return out
}

// BuildIntervals turns readings of one meter and the requests between them into intervals. Raw
// intervals (consecutive readings) are merged until the meter moved at least the meter's minimum, so
// the meter's rounding and the timing at the edges matter less. historyThrough is the time up to which
// history is known to be complete (unix ms).
func BuildIntervals(samples []Sample, reqs []Request, meter string, historyThrough int64) []Interval {
	rs := readings(samples, meter)
	rule := rules[meter]
	metered := map[string]bool{}
	for _, s := range samples {
		metered[s.Session] = true
	}
	sorted := append([]Request(nil), reqs...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.TS != b.TS {
			return a.TS < b.TS
		}
		if a.Session != b.Session {
			return a.Session < b.Session
		}
		return a.Model < b.Model
	})

	var out []Interval
	var cur *Interval
	flush := func(why string) {
		if cur != nil && why != "" {
			cur.Clean, cur.Why = false, why
		}
		if cur != nil && cur.Delta > 0 {
			out = append(out, *cur)
		}
		cur = nil
	}
	ri := 0
	for k := 1; k < len(rs); k++ {
		a, b := rs[k-1], rs[k]
		if abs64(a.reset-b.reset) > resetTolerance {
			flush(whyTooSmall) // a new window: what was collecting never reached the minimum
			continue
		}
		if b.pct < a.pct {
			flush(whyDropped)
			continue
		}
		if cur == nil {
			cur = &Interval{Start: a.ts, Window: b.reset, Priced: true, Clean: true}
		}
		cur.End, cur.Delta = b.ts, cur.Delta+(b.pct-a.pct)
		for ri < len(sorted) && sorted[ri].TS <= a.ts {
			ri++
		}
		for ; ri < len(sorted) && sorted[ri].TS <= b.ts; ri++ {
			q := sorted[ri]
			if !metered[q.Session] {
				cur.Clean, cur.Why = false, whyUnknown
			}
			cur.add(Family(q.Model), q.Tokens)
			cur.USD += q.USD
			cur.Priced = cur.Priced && q.Priced
		}
		if cur.End-cur.Start > rule.maxSpan {
			flush(whyTooLong)
			continue
		}
		if cur.Delta >= rule.minDelta {
			if cur.End > historyThrough {
				cur.Clean, cur.Why = false, whyNotRead
			}
			cur.Block = cur.Window
			if rule.dayBlock {
				cur.Block = cur.End / 86_400_000
			}
			flush("")
		}
	}
	if cur != nil {
		flush(whyTooSmall)
	}
	return out
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

var familyRe = regexp.MustCompile(`^(?:claude-)?([a-z]+)-[0-9]`)

// Family is a model's family ("opus", "sonnet", ...), or "other".
func Family(model string) string {
	m := familyRe.FindStringSubmatch(strings.ToLower(model))
	if m == nil {
		return "other"
	}
	return m[1]
}
