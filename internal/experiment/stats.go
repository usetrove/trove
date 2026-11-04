package experiment

import (
	"math"
	"math/rand"
	"sort"
)

// Resamples is the number of bootstrap resamples. It matches the benchmark
// lab (bench/config.yaml bootstrap_resamples is larger because it runs offline;
// this runs on every analysis pass, so it is smaller).
const Resamples = 2000

// byStratum holds one metric's per-session values for one arm, keyed by stratum.
type byStratum map[string][]float64

func meanOf(x []float64) float64 {
	if len(x) == 0 {
		return 0
	}
	var s float64
	for _, v := range x {
		s += v
	}
	return s / float64(len(x))
}

// strata returns the strata that have at least minPer sessions in both arms, in
// sorted order, with their weights (sessions in the stratum, both arms).
func strata(on, off byStratum, minPer int) ([]string, []float64) {
	var keys []string
	for k, v := range on {
		if len(v) >= minPer && len(off[k]) >= minPer {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	w := make([]float64, len(keys))
	for i, k := range keys {
		w[i] = float64(len(on[k]) + len(off[k]))
	}
	return keys, w
}

// diff computes the stratified difference of means (on minus off). When
// relative is true it is divided by the stratified off mean, so 0.05 means 5%
// higher with the optimizer on. ok is false when a relative change is
// undefined (the off mean is zero).
func diff(on, off byStratum, keys []string, w []float64, relative bool) (float64, bool) {
	var d, base, wt float64
	for i, k := range keys {
		d += w[i] * (meanOf(on[k]) - meanOf(off[k]))
		base += w[i] * meanOf(off[k])
		wt += w[i]
	}
	if wt == 0 {
		return 0, false
	}
	d, base = d/wt, base/wt
	if !relative {
		return d, true
	}
	if base == 0 {
		return 0, false
	}
	return d / base, true
}

func resample(r *rand.Rand, x []float64) []float64 {
	out := make([]float64, len(x))
	for i := range out {
		out[i] = x[r.Intn(len(x))]
	}
	return out
}

// bootstrap returns the stratified estimate and its 95% percentile interval.
// Each stratum's two arms are resampled separately, so the design (balanced
// arms inside strata) is preserved. The seed makes it reproducible. ok is false
// when the statistic is undefined.
func bootstrap(on, off byStratum, minPer int, relative bool, seed int64) (est, lo, hi float64, strataUsed int, ok bool) {
	keys, w := strata(on, off, minPer)
	if len(keys) == 0 {
		return 0, 0, 0, 0, false
	}
	est, ok = diff(on, off, keys, w, relative)
	if !ok {
		return 0, 0, 0, len(keys), false
	}
	r := rand.New(rand.NewSource(seed)) // #nosec G404 -- reproducible statistics, not security
	draws := make([]float64, 0, Resamples)
	for i := 0; i < Resamples; i++ {
		bon, boff := byStratum{}, byStratum{}
		for _, k := range keys {
			bon[k], boff[k] = resample(r, on[k]), resample(r, off[k])
		}
		if v, vok := diff(bon, boff, keys, w, relative); vok {
			draws = append(draws, v)
		}
	}
	if len(draws) < Resamples/2 {
		return est, 0, 0, len(keys), false
	}
	sort.Float64s(draws)
	return est, quantile(draws, 0.025), quantile(draws, 0.975), len(keys), true
}

// quantile of a sorted slice, linear interpolation.
func quantile(sorted []float64, q float64) float64 {
	pos := q * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo == hi {
		return sorted[lo]
	}
	f := pos - float64(lo)
	return sorted[lo]*(1-f) + sorted[hi]*f
}
