package limitfit

import (
	"hash/fnv"
	"math"
	"math/rand/v2"
	"runtime"
	"sort"
	"sync"
)

// Fit settings (docs/limit-fit.md).
const (
	TrimShare  = 0.20 // at most this share of intervals is trimmed as outside drain
	trimRounds = 5
	trimK      = 2.5 // trim residuals above median + trimK * robust sigma
	Bootstrap  = 400 // bootstrap rounds for each interval
	CILow      = 0.05
	CIHigh     = 0.95 // a 90% interval
	altIters   = 30   // alternating rounds between type weights and family multipliers
)

// model is a fitted meter: type weights in percentage points per million tokens, and a multiplier per
// family (the reference family is 1; a pooled family uses 1 too).
type model struct {
	w    [NTypes]float64
	mult map[string]float64
}

func (m *model) predict(iv *Interval) float64 {
	var p float64
	for _, ft := range iv.Tokens {
		mf, ok := m.mult[ft.Family]
		if !ok {
			mf = 1
		}
		for k, v := range ft.T {
			p += m.w[k] * mf * v / 1e6
		}
	}
	return p
}

// fitSpec says what to fit.
type fitSpec struct {
	separate    []string           // families with their own multiplier (the reference is not in it)
	priceOnly   bool               // fit one scale on API-equivalent dollars instead of type weights
	profile     *[NTypes]float64   // fit one scale on this type profile (the weekly meter borrowing the 5h profile)
	profileMult map[string]float64 // the profile's family multipliers
}

// fitResult is one fit: the model, the dollar scale (price fits) or profile scale, and which intervals
// were trimmed.
type fitResult struct {
	m       model
	scale   float64 // % per API dollar (priceOnly) or per profile unit (profile)
	trimmed []bool
}

func (s fitSpec) predict(r *fitResult, iv *Interval) float64 {
	switch {
	case s.priceOnly:
		return r.scale * iv.USD
	case s.profile != nil:
		m := model{w: *s.profile, mult: s.profileMult}
		return r.scale * m.predict(iv)
	}
	return r.m.predict(iv)
}

// fitOnce fits ivs without trimming.
func fitOnce(ivs []*Interval, s fitSpec) fitResult {
	if s.priceOnly || s.profile != nil {
		pm := model{mult: s.profileMult}
		if s.profile != nil {
			pm.w = *s.profile
		}
		var g, h float64
		for _, iv := range ivs {
			x := iv.USD
			if s.profile != nil {
				x = pm.predict(iv)
			}
			g += x * x
			h += x * iv.Delta
		}
		sc := 0.0
		if g > 0 {
			sc = math.Max(0, h/g)
		}
		return fitResult{scale: sc, m: model{mult: map[string]float64{}}}
	}
	// Resolve each interval's families to an index once: -1 for the reference and pooled families.
	sep := s.separate
	type row struct {
		t   [NTypes]float64 // tokens of families counted at multiplier 1, in millions
		sep [][NTypes]float64
	}
	rows := make([]row, len(ivs))
	for i, iv := range ivs {
		rows[i].sep = make([][NTypes]float64, len(sep))
		for _, ft := range iv.Tokens {
			j := indexOf(sep, ft.Family)
			for k, v := range ft.T {
				if j < 0 {
					rows[i].t[k] += v / 1e6
				} else {
					rows[i].sep[j][k] += v / 1e6
				}
			}
		}
	}
	mult := make([]float64, len(sep))
	for j := range mult {
		mult[j] = 1
	}
	var w [NTypes]float64
	G := make([][]float64, NTypes)
	for k := range G {
		G[k] = make([]float64, NTypes)
	}
	G2 := make([][]float64, len(sep))
	for k := range G2 {
		G2[k] = make([]float64, len(sep))
	}
	for it := 0; it < altIters; it++ {
		// Type weights with the multipliers fixed.
		var h [NTypes]float64
		for k := range G {
			clear(G[k])
		}
		for i := range rows {
			x := rows[i].t
			for j, st := range rows[i].sep {
				for k := range x {
					x[k] += mult[j] * st[k]
				}
			}
			for a := 0; a < NTypes; a++ {
				if x[a] == 0 {
					continue
				}
				h[a] += x[a] * ivs[i].Delta
				for b := a; b < NTypes; b++ {
					G[a][b] += x[a] * x[b]
				}
			}
		}
		symmetric(G)
		copy(w[:], nnlsGram(G, h[:]))
		if len(sep) == 0 {
			break
		}
		// Multipliers with the type weights fixed; the reference and pooled families stay at 1.
		h2 := make([]float64, len(sep))
		for k := range G2 {
			clear(G2[k])
		}
		z := make([]float64, len(sep))
		for i := range rows {
			b := ivs[i].Delta
			for k := 0; k < NTypes; k++ {
				b -= w[k] * rows[i].t[k]
			}
			for j, st := range rows[i].sep {
				z[j] = 0
				for k := 0; k < NTypes; k++ {
					z[j] += w[k] * st[k]
				}
			}
			for a := range z {
				h2[a] += z[a] * b
				for c := a; c < len(z); c++ {
					G2[a][c] += z[a] * z[c]
				}
			}
		}
		symmetric(G2)
		next := nnlsGram(G2, h2)
		moved := 0.0
		for j := range mult {
			moved = math.Max(moved, math.Abs(next[j]-mult[j])/math.Max(1e-9, math.Abs(mult[j])))
			mult[j] = next[j]
		}
		if moved < 1e-6 {
			break
		}
	}
	m := model{w: w, mult: map[string]float64{}}
	for j, f := range sep {
		m.mult[f] = mult[j]
	}
	return fitResult{m: m}
}

func symmetric(G [][]float64) {
	for a := range G {
		for b := 0; b < a; b++ {
			G[a][b] = G[b][a]
		}
	}
}

func indexOf(xs []string, s string) int {
	for i, x := range xs {
		if x == s {
			return i
		}
	}
	return -1
}

// fitTrimmed fits, then repeatedly drops the intervals with the largest positive residuals (the meter
// moved more than this machine's tokens explain: outside drain) and refits, trimming at most TrimShare.
func fitTrimmed(ivs []*Interval, s fitSpec) fitResult {
	keep := make([]bool, len(ivs))
	for i := range keep {
		keep[i] = true
	}
	maxTrim := int(math.Floor(TrimShare * float64(len(ivs))))
	trimmed := 0
	var r fitResult
	for round := 0; ; round++ {
		var sub []*Interval
		for i, iv := range ivs {
			if keep[i] {
				sub = append(sub, iv)
			}
		}
		r = fitOnce(sub, s)
		if round == trimRounds || trimmed >= maxTrim {
			break
		}
		res := make([]float64, len(ivs))
		var kept []float64
		for i, iv := range ivs {
			res[i] = iv.Delta - s.predict(&r, iv)
			if keep[i] {
				kept = append(kept, res[i])
			}
		}
		med := median(kept)
		dev := make([]float64, len(kept))
		for i, v := range kept {
			dev[i] = math.Abs(v - med)
		}
		sigma := math.Max(1.4826*median(dev), 0.05) // floor: a fraction of a meter point
		thr := med + trimK*sigma
		type cand struct {
			i int
			r float64
		}
		var cs []cand
		for i := range ivs {
			if keep[i] && res[i] > thr {
				cs = append(cs, cand{i, res[i]})
			}
		}
		if len(cs) == 0 {
			break
		}
		sort.Slice(cs, func(a, b int) bool {
			if cs[a].r != cs[b].r {
				return cs[a].r > cs[b].r
			}
			return cs[a].i < cs[b].i
		})
		for _, c := range cs {
			if trimmed >= maxTrim {
				break
			}
			keep[c.i] = false
			trimmed++
		}
	}
	r.trimmed = make([]bool, len(ivs))
	for i := range keep {
		r.trimmed[i] = !keep[i]
	}
	return r
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// outsideShare is the share of the meter's movement this machine's tokens do not explain, in percent:
// (total movement - predicted) / total movement over every interval given, never below 0. Noise
// cancels in the sum; outside drain does not.
func outsideShare(ivs []*Interval, s fitSpec, r *fitResult) float64 {
	var moved, pred float64
	for _, iv := range ivs {
		moved += iv.Delta
		if iv.total() > 0 || iv.USD > 0 {
			pred += s.predict(r, iv)
		}
	}
	if moved == 0 {
		return 0
	}
	return math.Max(0, (moved-pred)/moved*100)
}

// seedOf derives the bootstrap seed from the data, so the same intervals always give the same output.
func seedOf(ivs []*Interval) uint64 {
	h := fnv.New64a()
	var buf [8]byte
	put := func(v uint64) {
		for i := range buf {
			buf[i] = byte(v >> (8 * i))
		}
		_, _ = h.Write(buf[:])
	}
	for _, iv := range ivs {
		put(uint64(iv.Start))
		put(uint64(iv.End))
		put(math.Float64bits(iv.Delta))
		put(math.Float64bits(iv.total()))
	}
	return h.Sum64()
}

// replicate is one bootstrap round's estimates.
type replicate struct {
	m       model
	scale   float64
	outside float64
}

// bootstrap refits on rounds of resampled blocks (whole reset windows, or days), keeping each round's
// model. all are every clean interval, including those with no tokens (for the outside share).
func bootstrap(fitIvs, all []*Interval, s fitSpec, rounds int) []replicate {
	blocks := map[int64][]int{}
	var keys []int64
	for i, iv := range all {
		if _, ok := blocks[iv.Block]; !ok {
			keys = append(keys, iv.Block)
		}
		blocks[iv.Block] = append(blocks[iv.Block], i)
	}
	sort.Slice(keys, func(a, b int) bool { return keys[a] < keys[b] })
	inFit := map[*Interval]bool{}
	for _, iv := range fitIvs {
		inFit[iv] = true
	}
	// Draw every round's resample first, in order, so the result does not depend on scheduling.
	rng := rand.New(rand.NewPCG(seedOf(all), 0x7472_6f76_65))
	type draw struct{ all, fit []*Interval }
	draws := make([]draw, rounds)
	for r := range draws {
		for range keys {
			for _, i := range blocks[keys[rng.IntN(len(keys))]] {
				draws[r].all = append(draws[r].all, all[i])
				if inFit[all[i]] {
					draws[r].fit = append(draws[r].fit, all[i])
				}
			}
		}
	}
	reps := make([]*replicate, rounds)
	var wg sync.WaitGroup
	next := make(chan int)
	for range max(1, min(runtime.NumCPU(), 8)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := range next {
				d := draws[r]
				if len(d.fit) == 0 {
					continue
				}
				f := fitTrimmed(d.fit, s)
				reps[r] = &replicate{m: f.m, scale: f.scale, outside: outsideShare(d.all, s, &f)}
			}
		}()
	}
	for r := range draws {
		next <- r
	}
	close(next)
	wg.Wait()
	out := make([]replicate, 0, rounds)
	for _, r := range reps {
		if r != nil {
			out = append(out, *r)
		}
	}
	return out
}

// Range is a point estimate with a 90% bootstrap interval.
type Range struct {
	Point float64 `json:"point"`
	Lo    float64 `json:"lo"`
	Hi    float64 `json:"hi"`
}

// percentiles returns the 90% interval of xs around point.
func percentiles(point float64, xs []float64) Range {
	if len(xs) == 0 {
		return Range{Point: point, Lo: point, Hi: point}
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	at := func(q float64) float64 {
		pos := q * float64(len(s)-1)
		lo := int(math.Floor(pos))
		hi := min(lo+1, len(s)-1)
		return s[lo] + (s[hi]-s[lo])*(pos-float64(lo))
	}
	return Range{Point: point, Lo: at(CILow), Hi: at(CIHigh)}
}

// Contains reports whether v is inside the interval.
func (r Range) Contains(v float64) bool { return v >= r.Lo && v <= r.Hi }
