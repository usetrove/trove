package experiment

import (
	"math"
	"math/rand"
	"sort"
)

// Covariate adjustment (CUPED) for the cost metrics. The method was pre-registered in
// docs/benchmark-methodology.md section 4a before this code was written; change both together, with a new
// dated entry there.

// CovariatePriorCost names the one covariate in use: the same metric's mean over earlier off-arm sessions.
const CovariatePriorCost = "prior_cost"

const (
	// priorWindow is how many earlier off-arm sessions a session's covariate averages over.
	priorWindow = 5
	// minCovered is the share of sessions that must have a covariate for the adjustment to be used.
	minCovered = 0.8
	// minCorr is the smallest absolute within-stratum correlation of covariate and metric for the adjustment
	// to be used. Below it the variance gain (r squared) is under 9%.
	minCorr = 0.3
)

// pair is one session's metric value y and its covariate x. has is false when the session had no earlier
// off-arm session to take x from.
type pair struct {
	y, x float64
	has  bool
}

type pairsByStratum map[string][]pair

// values drops the covariates.
func values(p pairsByStratum) byStratum {
	out := byStratum{}
	for k, v := range p {
		ys := make([]float64, len(v))
		for i, q := range v {
			ys[i] = q.y
		}
		out[k] = ys
	}
	return out
}

// priorCovariate returns, for each session in all, the mean of pick over the last priorWindow off-arm
// sessions of the same stratum in earlier randomisation blocks. Sessions in one block share the covariate, so
// it cannot depend on a session's own arm; only off-arm sessions count, so the optimizer never moves it.
// Sessions rejected by a non-nil keep neither get nor give a covariate.
func priorCovariate(all []sess, pick func(sess) float64, keep func(sess) bool) ([]float64, []bool) {
	type off struct {
		block int
		v     float64
	}
	offs := map[string][]off{}
	byIdx := make([]int, 0, len(all))
	for i := range all {
		byIdx = append(byIdx, i)
	}
	sort.SliceStable(byIdx, func(a, b int) bool { return all[byIdx[a]].seq < all[byIdx[b]].seq })
	for _, i := range byIdx {
		s := all[i]
		if s.arm == ArmOff && (keep == nil || keep(s)) {
			offs[s.stratum] = append(offs[s.stratum], off{s.seq / blockSize, pick(s)})
		}
	}
	x, has := make([]float64, len(all)), make([]bool, len(all))
	for i, s := range all {
		if keep != nil && !keep(s) {
			continue
		}
		list, b := offs[s.stratum], s.seq/blockSize
		end := sort.Search(len(list), func(j int) bool { return list[j].block >= b })
		if end == 0 {
			continue
		}
		var sum float64
		start := max(0, end-priorWindow)
		for _, o := range list[start:end] {
			sum += o.v
		}
		x[i], has[i] = sum/float64(end-start), true
	}
	return x, has
}

// fit returns the pooled (both arms) covariate mean and slope θ = cov(x, y) / var(x) of one stratum, over the
// sessions that have a covariate. θ is 0 when x does not vary.
func fit(on, off []pair) (xbar, theta float64) {
	var n, sx, sy float64
	for _, arm := range [][]pair{on, off} {
		for _, p := range arm {
			if p.has {
				n++
				sx += p.x
				sy += p.y
			}
		}
	}
	if n == 0 {
		return 0, 0
	}
	xbar, ybar := sx/n, sy/n
	var sxx, sxy float64
	for _, arm := range [][]pair{on, off} {
		for _, p := range arm {
			if p.has {
				sxx += (p.x - xbar) * (p.x - xbar)
				sxy += (p.x - xbar) * (p.y - ybar)
			}
		}
	}
	if sxx == 0 {
		return xbar, 0
	}
	return xbar, sxy / sxx
}

// adjMean is the mean of y - θ(x - xbar); a session with no covariate is left as it is.
func adjMean(ps []pair, xbar, theta float64) float64 {
	if len(ps) == 0 {
		return 0
	}
	var s float64
	for _, p := range ps {
		v := p.y
		if p.has {
			v -= theta * (p.x - xbar)
		}
		s += v
	}
	return s / float64(len(ps))
}

// cupedUsable applies the pre-registered rule: in the given strata, at least minCovered of sessions have a
// covariate and the pooled within-stratum correlation of covariate and metric is at least minCorr in absolute
// value. It looks only at x and y, never at the difference between arms.
func cupedUsable(on, off pairsByStratum, keys []string) bool {
	var total, covered int
	var sxx, syy, sxy float64
	for _, k := range keys {
		var n, sx, sy float64
		for _, arm := range [][]pair{on[k], off[k]} {
			for _, p := range arm {
				total++
				if p.has {
					covered++
					n++
					sx += p.x
					sy += p.y
				}
			}
		}
		if n == 0 {
			continue
		}
		xbar, ybar := sx/n, sy/n
		for _, arm := range [][]pair{on[k], off[k]} {
			for _, p := range arm {
				if p.has {
					sxx += (p.x - xbar) * (p.x - xbar)
					syy += (p.y - ybar) * (p.y - ybar)
					sxy += (p.x - xbar) * (p.y - ybar)
				}
			}
		}
	}
	if total == 0 || float64(covered) < minCovered*float64(total) || sxx == 0 || syy == 0 {
		return false
	}
	return math.Abs(sxy/math.Sqrt(sxx*syy)) >= minCorr
}

// cupedDiff is diff on adjusted values, with θ fitted per stratum on these very values. A relative change is
// divided by the unadjusted off-arm mean.
func cupedDiff(on, off pairsByStratum, keys []string, w []float64, relative bool) (float64, bool) {
	var d, base, wt float64
	for i, k := range keys {
		xbar, theta := fit(on[k], off[k])
		d += w[i] * (adjMean(on[k], xbar, theta) - adjMean(off[k], xbar, theta))
		base += w[i] * adjMean(off[k], 0, 0)
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

func resamplePairs(r *rand.Rand, x []pair) []pair {
	out := make([]pair, len(x))
	for i := range out {
		out[i] = x[r.Intn(len(x))]
	}
	return out
}

// bootstrapCUPED is bootstrap for the adjusted statistic, over the same strata and weights. Pairs are
// resampled per arm within stratum and θ is re-fitted in every resample, so the range carries the uncertainty
// of the fit.
func bootstrapCUPED(on, off pairsByStratum, keys []string, w []float64, relative bool, seed int64) (est, lo, hi float64, ok bool) {
	if len(keys) == 0 {
		return 0, 0, 0, false
	}
	est, ok = cupedDiff(on, off, keys, w, relative)
	if !ok {
		return 0, 0, 0, false
	}
	r := rand.New(rand.NewSource(seed)) // #nosec G404 -- reproducible statistics, not security
	draws := make([]float64, 0, Resamples)
	for i := 0; i < Resamples; i++ {
		bon, boff := pairsByStratum{}, pairsByStratum{}
		for _, k := range keys {
			bon[k], boff[k] = resamplePairs(r, on[k]), resamplePairs(r, off[k])
		}
		if v, vok := cupedDiff(bon, boff, keys, w, relative); vok {
			draws = append(draws, v)
		}
	}
	if len(draws) < Resamples/2 {
		return est, 0, 0, false
	}
	sort.Float64s(draws)
	return est, quantile(draws, 0.025), quantile(draws, 0.975), true
}
