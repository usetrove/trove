package stress

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/pricing"
)

// PricingDate is the date every saving is priced at. Pricing is effective-dated, so a fixed date keeps the
// numbers reproducible and makes the table date visible in the report.
var PricingDate = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

// models are the pinned model IDs the realism layer samples from. They are priced; the report says every
// saving is an estimate at list price.
var models = []string{"claude-haiku-5-5", "claude-sonnet-5-5", "claude-opus-5-5"}

var mixes = []string{"read-heavy", "shell-heavy", "search-heavy"}

var profiles = []string{"daily-pro", "heavy-max", "casual-free"}

// sessionOutcome is one session run in both arms.
type sessionOutcome struct {
	offUSD, onUSD float64
	violations    []Violation
	requests      int
}

// runSession prices one session with the engine off and on, and checks every request the engine produces.
// The cache model is the same in both arms: the cached prefix is the part of this request that matches the
// previous one byte for byte, and an expired turn reads nothing from cache.
func runSession(spec SessionSpec, seed uint64, tab *pricing.Table) (sessionOutcome, error) {
	var so sessionOutcome
	turns := genTurns(spec, seed)
	bodies := buildBodies(spec, turns)
	store := &memStore{}
	eng := newEngine(allNames(), store, PricingDate)
	expiry := rng(seed, "expiry", spec.Name)

	var prevOff, prevOn []byte
	for k, orig := range bodies {
		expired := k > 0 && expiry.Float64() < spec.ExpiryProb
		res := eng.TransformRequest(context.Background(), optimize.RequestIn{
			Provider: "anthropic", Path: "/v1/messages", Header: http.Header{}, Body: orig,
		}, optimize.Session{Key: fmt.Sprintf("%s-%d", spec.Name, seed), Agent: "claude_code", TroveRun: true})
		out := res.Body

		so.requests++
		so.violations = append(so.violations, CheckRequest(orig, out, declaredOverheadTokens(res))...)
		if len(res.Failures) > 0 && !bytes.Equal(out, orig) {
			so.violations = append(so.violations, Violation{InvFailOpenOnError, "a failed optimizer still changed the request"})
		}
		so.violations = append(so.violations, CheckPrefix(prevOn, out)...)

		so.offUSD += requestUSD(tab, spec.Model, prevOff, orig, turns[k].Reply, expired)
		so.onUSD += requestUSD(tab, spec.Model, prevOn, out, turns[k].Reply, expired)
		prevOff, prevOn = orig, out
	}
	if len(bodies) > 0 {
		// The final request holds every tool output a stored blob can come from.
		so.violations = append(so.violations, CheckRecovery(bodies[len(bodies)-1], store.snapshot())...)
	}
	so.violations = uniqueByInvariant(so.violations)
	return so, nil
}

// requestUSD prices one request at list price. The whole prompt is treated as cached after the first request,
// as Claude Code's prefix caching does, so uncached input is zero.
func requestUSD(tab *pricing.Table, model string, prev, cur []byte, replyTokens int, expired bool) float64 {
	total := tokensOf(len(cur))
	var read int64
	if !expired && len(prev) > 0 {
		read = tokensOf(commonPrefix(prev, cur))
	}
	write := total - read
	u := pricing.Usage{CacheRead: read, CacheWrite: map[string]int64{"5m": write}, Output: int64(replyTokens)}
	c := tab.Cost(pricing.Request{Provider: "anthropic", Model: model, Time: PricingDate, Usage: u})
	if c.Status != pricing.Priced {
		return math.NaN()
	}
	return c.Total.Float64()
}

// savedPct is the saving as a percent of the off arm's cost.
func savedPct(off, on float64) float64 {
	if off <= 0 {
		return 0
	}
	return (off - on) / off * 100
}

// sampleSpec draws one point in the parameter space from the run seed.
func sampleSpec(seed uint64, i int, maxTurns int) SessionSpec {
	r := rng(seed, "sample", fmt.Sprint(i))
	cache := []float64{0, 0.25, 1} // hit, expiring, cold
	return SessionSpec{
		Name:           fmt.Sprintf("sample-%04d", i),
		PrefixTokens:   3000 + r.IntN(37001),
		Turns:          3 + r.IntN(maxTurns-2),
		Model:          models[r.IntN(len(models))],
		Mix:            mixes[r.IntN(len(mixes))],
		OutMedianBytes: int(math.Exp(math.Log(200) + r.Float64()*(math.Log(20000)-math.Log(200)))),
		OutSigma:       0.8,
		ExpiryProb:     cache[r.IntN(len(cache))],
		RepeatProb:     r.Float64() * 0.4,
		NoiseProb:      r.Float64() * 0.5,
		Profile:        profiles[r.IntN(len(profiles))],
	}
}

// edgeCases are named points at the corners of the parameter space.
func edgeCases() []SessionSpec {
	base := SessionSpec{Model: models[1], Mix: "shell-heavy", OutMedianBytes: 800, OutSigma: 0.5, Profile: "edge"}
	mk := func(name string, f func(*SessionSpec)) SessionSpec {
		s := base
		s.Name = "edge-" + name
		f(&s)
		return s
	}
	return []SessionSpec{
		mk("one-turn", func(s *SessionSpec) { s.PrefixTokens, s.Turns = 20000, 1 }),
		mk("prefix-near-cache-minimum", func(s *SessionSpec) { s.PrefixTokens, s.Turns = 1100, 12 }),
		mk("all-duplicate-reads", func(s *SessionSpec) { s.PrefixTokens, s.Turns, s.RepeatProb = 8000, 30, 0.95 }),
		mk("one-giant-output", func(s *SessionSpec) { s.PrefixTokens, s.Turns, s.OutMedianBytes = 8000, 4, 3_000_000 }),
		mk("all-ansi-crlf", func(s *SessionSpec) { s.PrefixTokens, s.Turns, s.NoiseProb, s.OutMedianBytes = 8000, 20, 1, 20000 }),
		mk("cold-cache-every-turn", func(s *SessionSpec) { s.PrefixTokens, s.Turns, s.ExpiryProb = 30000, 25, 1 }),
		mk("long-session", func(s *SessionSpec) { s.PrefixTokens, s.Turns, s.OutMedianBytes = 3000, 200, 200 }),
	}
}

// realism is layer 1. It is the only layer that prints savings, and it labels them estimated.
func (e *env) realism() LayerResult {
	lr := LayerResult{Name: LayerRealism}
	tab, err := pricing.Default()
	if err != nil {
		lr.Status = Skipped
		lr.Notes = append(lr.Notes, "the built-in pricing table did not load: "+err.Error())
		return lr
	}
	maxTurns := 60
	if e.opts.Mode == ModeFull {
		maxTurns = 120
	}

	var sample []SessionSpec
	for i := 0; i < e.p.samples; i++ {
		sample = append(sample, sampleSpec(e.opts.Seed, i, maxTurns))
	}
	edges := edgeCases()

	var rows []sampleRow
	for _, spec := range sample {
		so, err := runSession(spec, e.opts.Seed, tab)
		if err != nil {
			e.record(&lr, spec.Name, []Violation{{"run", err.Error()}}, nil)
			continue
		}
		rows = append(rows, sampleRow{spec: spec, saved: savedPct(so.offUSD, so.onUSD)})
		e.record(&lr, spec.Name, so.violations, nil)
	}

	sv := &Savings{Label: "estimated", Samples: len(rows), Pricing: PricingDate.Format("2006-01-02"),
		Notes: []string{
			"Simulated sessions, priced at list price on the pinned models. These are estimates, not a bill.",
			"Token counts use the engine's bytes-per-token estimate (3.5), not provider counts.",
			"Trove's own tool definitions (a few hundred tokens per request) are not counted in this layer.",
		}}
	sv.Overall = summarize(rows)
	sv.Drivers = drivers(rows, e.opts.Seed)
	sv.ByModel = byModel(rows)
	for _, spec := range edges {
		if e.opts.skipEdges {
			break
		}
		so, err := runSession(spec, e.opts.Seed, tab)
		if err != nil {
			e.record(&lr, spec.Name, []Violation{{"run", err.Error()}}, nil)
			continue
		}
		e.record(&lr, spec.Name, so.violations, nil)
		sv.Edges = append(sv.Edges, EdgeResult{Name: spec.Name, Turns: spec.Turns,
			SavedPct: round1(savedPct(so.offUSD, so.onUSD)), Passed: len(so.violations) == 0})
	}
	lr.Savings = sv
	lr.Notes = append(lr.Notes, fmt.Sprintf("seed %d; mode %s; every saving figure is estimated", e.opts.Seed, e.opts.Mode))
	if e.opts.ReplayDir != "" {
		rs, err := replayLogs(e.opts.ReplayDir, replayPerSession(e.opts.Mode))
		if err != nil {
			e.record(&lr, "replay-local-logs", []Violation{{"replay", err.Error()}}, nil)
		} else {
			sv.Replay = &rs
			e.record(&lr, "replay-local-logs", nil, nil)
			lr.Notes = append(lr.Notes, fmt.Sprintf("replay: %d log files, %d sessions, %d turns; aggregates only, nothing from the logs was written anywhere",
				rs.LogFiles, rs.Sessions, rs.Turns))
		}
	}
	finish(&lr)
	return lr
}

type sampleRow struct {
	spec  SessionSpec
	saved float64
}

func summarize(rows []sampleRow) SavingSummary {
	xs := make([]float64, 0, len(rows))
	for _, r := range rows {
		if !math.IsNaN(r.saved) {
			xs = append(xs, r.saved)
		}
	}
	if len(xs) == 0 {
		return SavingSummary{}
	}
	sort.Float64s(xs)
	return SavingSummary{MedianPct: round1(quantile(xs, 0.5)), P10Pct: round1(quantile(xs, 0.1)), P90Pct: round1(quantile(xs, 0.9))}
}

func byModel(rows []sampleRow) []GroupSaving {
	groups := map[string][]float64{}
	for _, r := range rows {
		if !math.IsNaN(r.saved) {
			groups[r.spec.Model] = append(groups[r.spec.Model], r.saved)
		}
	}
	var out []GroupSaving
	for _, m := range models {
		g := groups[m]
		out = append(out, GroupSaving{Model: m, N: len(g), MedianPct: round1(quantile(sortedCopy(g), 0.5))})
	}
	return out
}

// spearman is the Spearman rank correlation of xs and ys (average ranks for ties).
func spearman(xs, ys []float64) float64 {
	if len(xs) < 3 {
		return 0
	}
	rx, ry := ranks(xs), ranks(ys)
	return pearson(rx, ry)
}

func ranks(xs []float64) []float64 {
	idx := make([]int, len(xs))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return xs[idx[a]] < xs[idx[b]] })
	out := make([]float64, len(xs))
	for i := 0; i < len(idx); {
		j := i
		for j+1 < len(idx) && xs[idx[j+1]] == xs[idx[i]] {
			j++
		}
		avg := float64(i+j)/2 + 1
		for k := i; k <= j; k++ {
			out[idx[k]] = avg
		}
		i = j + 1
	}
	return out
}

func pearson(a, b []float64) float64 {
	n := float64(len(a))
	var ma, mb float64
	for i := range a {
		ma += a[i]
		mb += b[i]
	}
	ma, mb = ma/n, mb/n
	var num, da, db float64
	for i := range a {
		num += (a[i] - ma) * (b[i] - mb)
		da += (a[i] - ma) * (a[i] - ma)
		db += (b[i] - mb) * (b[i] - mb)
	}
	if da == 0 || db == 0 {
		return 0
	}
	return num / math.Sqrt(da*db)
}

func sortedCopy(xs []float64) []float64 {
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	return s
}

// quantile returns the q-quantile of a sorted slice by linear interpolation. Empty input gives 0.
func quantile(s []float64, q float64) float64 {
	if len(s) == 0 {
		return 0
	}
	pos := q * float64(len(s)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	return s[lo] + (s[hi]-s[lo])*(pos-float64(lo))
}

func round1(x float64) float64 { return math.Round(x*10) / 10 }
func round2(x float64) float64 { return math.Round(x*100) / 100 }
