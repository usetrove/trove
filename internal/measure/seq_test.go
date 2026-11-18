package measure

import (
	"math"
	"math/rand/v2"
	"testing"
)

// slack is three Monte-Carlo standard errors above alpha for a given number of trials.
func slack(trials int) float64 {
	return seqAlpha + 3*math.Sqrt(seqAlpha*(1-seqAlpha)/float64(trials))
}

// The rate rules (re-fetch, hook re-reads, fetch-backs, unused advice) are
// checked after every new action. When the true rate sits exactly at the limit
// (the hardest case that is still "not worse"), the sequential test must roll
// back in at most alpha of histories, whether it is checked after every action
// or every tenth. The old rule is run alongside to show the test has teeth.
func TestSequentialRateFalseRollbackStaysNominal(t *testing.T) {
	const trials, minN, maxN = 400, 20, 300
	for _, limit := range []float64{0.12, 0.25, 0.5} {
		r := rand.New(rand.NewPCG(1, uint64(limit*1000)))
		var every, tenth, old int
		for range trials {
			var k int
			var hitEvery, hitTenth, hitOld bool
			for n := 1; n <= maxN; n++ {
				if r.Float64() < limit {
					k++
				}
				if n < minN {
					continue
				}
				if !hitOld && float64(k)/float64(n) >= limit { // the hook rule before 1.3
					hitOld = true
				}
				if hitEvery && hitTenth {
					continue
				}
				if compareRateToLimit(k, n, minN, limit).Verdict == Regression {
					hitEvery = true
					hitTenth = hitTenth || (n-minN)%10 == 0
				}
			}
			for _, b := range []struct {
				hit bool
				n   *int
			}{{hitEvery, &every}, {hitTenth, &tenth}, {hitOld, &old}} {
				if b.hit {
					*b.n++
				}
			}
		}
		fe, ft, fo := float64(every)/trials, float64(tenth)/trials, float64(old)/trials
		t.Logf("limit %.2f: false rollbacks checked every action %.3f, every tenth %.3f, old rule %.3f", limit, fe, ft, fo)
		if fe > slack(trials) || ft > slack(trials) {
			t.Errorf("limit %.2f: false-rollback rate %.3f / %.3f is above alpha %.2f", limit, fe, ft, seqAlpha)
		}
		if fo < 2*seqAlpha {
			t.Errorf("limit %.2f: the old rule only fired %.3f; the stress test is too weak to mean anything", limit, fo)
		}
	}
}

// A real regression is still caught: a rate twice the limit (or the limit plus
// 30 points) trips the rule within a few hundred actions nearly always.
func TestSequentialRateCatchesRealRegressions(t *testing.T) {
	const trials, minN, maxN = 200, 20, 300
	for _, limit := range []float64{0.12, 0.25, 0.5} {
		p := math.Min(2*limit, limit+0.3)
		r := rand.New(rand.NewPCG(2, uint64(limit*1000)))
		caught := 0
		for range trials {
			k := 0
			for n := 1; n <= maxN; n++ {
				if r.Float64() < p {
					k++
				}
				if n >= minN && seqRateExceeds(k, n, limit) {
					caught++
					break
				}
			}
		}
		if f := float64(caught) / trials; f < 0.95 {
			t.Errorf("limit %.2f, true rate %.2f: caught only %.3f of real regressions", limit, p, f)
		}
	}
}

func poisson(r *rand.Rand, lambda float64) float64 {
	l, k, p := math.Exp(-lambda), 0, 1.0
	for {
		p *= r.Float64()
		if p <= l {
			return float64(k)
		}
		k++
	}
}

// runningStats keeps a mean and variance without storing the sample.
type runningStats struct{ n, mean, m2 float64 }

func (s *runningStats) add(x float64) {
	s.n++
	d := x - s.mean
	s.mean += d / s.n
	s.m2 += d * (x - s.mean)
}

func (s *runningStats) variance() float64 {
	if s.n < 2 {
		return 0
	}
	return s.m2 / (s.n - 1)
}

// The mean rules (retries, tool errors) compare two growing groups of sessions.
// With both drawn from the same distribution, checked after every new pair of
// sessions, false rollbacks stay at alpha. The boundary is tested on its own,
// without the rule's relative-increase floor, at mixing scales from small to
// large, so that the floor cannot do the statistical test's job; the full rule
// at the default 30% floor is checked too.
func TestSequentialMeansFalseRollbackStaysNominal(t *testing.T) {
	const trials, minN, maxN = 1000, 20, 400
	dists := []struct {
		name string
		draw func(*rand.Rand) float64
	}{
		{"poisson(2)", func(r *rand.Rand) float64 { return poisson(r, 2) }},
		{"mostly zero, sometimes many", func(r *rand.Rand) float64 {
			if r.Float64() < 0.7 {
				return 0
			}
			return 1 + poisson(r, 4)
		}},
	}
	scales := []float64{0.1, 0.3, 1} // tau as a share of the reference mean
	for _, d := range dists {
		r := rand.New(rand.NewPCG(3, uint64(len(d.name))))
		hits := make([]int, len(scales))
		var rule, old int
		for range trials {
			var on, ref runningStats
			hit := make([]bool, len(scales))
			var hitRule, hitOld bool
			for n := 1; n <= maxN; n++ {
				on.add(d.draw(r))
				ref.add(d.draw(r))
				if n < minN {
					continue
				}
				diff := on.mean - ref.mean
				se := math.Sqrt(on.variance()/float64(n) + ref.variance()/float64(n))
				for i, s := range scales {
					if !hit[i] && ref.mean > 0 && seqMeanExceeds(diff, se, s*ref.mean) {
						hit[i] = true
					}
				}
				if meansVerdict(Comparison{NTreated: n, NRef: n}, on.mean, ref.mean, on.variance(), ref.variance(), 0.30).Verdict == Regression {
					hitRule = true
				}
				if diff > 1.645*se { // the significance test before 1.3, without the floor
					hitOld = true
				}
			}
			for i, h := range hit {
				if h {
					hits[i]++
				}
			}
			if hitRule {
				rule++
			}
			if hitOld {
				old++
			}
		}
		for i, s := range scales {
			f := float64(hits[i]) / trials
			t.Logf("%s, tau %.1f x mean: false rollbacks %.3f", d.name, s, f)
			if f > slack(trials) {
				t.Errorf("%s, tau %.1f x mean: false-rollback rate %.3f is above alpha %.2f", d.name, s, f, seqAlpha)
			}
		}
		fr, fo := float64(rule)/trials, float64(old)/trials
		t.Logf("%s: full rule %.3f, old test %.3f", d.name, fr, fo)
		if fr > slack(trials) {
			t.Errorf("%s: the full rule rolled back %.3f of clean histories", d.name, fr)
		}
		if fo < 2*seqAlpha {
			t.Errorf("%s: the old test only fired %.3f; the stress test is too weak to mean anything", d.name, fo)
		}
	}
}

// A real rise in retries is still caught.
func TestSequentialMeansCatchRealRegressions(t *testing.T) {
	const trials, minN, maxN, rel = 200, 20, 200, 0.30
	r := rand.New(rand.NewPCG(4, 4))
	caught := 0
	for range trials {
		var on, ref runningStats
		for n := 1; n <= maxN; n++ {
			on.add(poisson(r, 3))
			ref.add(poisson(r, 2))
			if n >= minN && meansVerdict(Comparison{NTreated: n, NRef: n}, on.mean, ref.mean, on.variance(), ref.variance(), rel).Verdict == Regression {
				caught++
				break
			}
		}
	}
	if f := float64(caught) / trials; f < 0.95 {
		t.Errorf("a 50%% rise in retries was caught in only %.3f of histories", f)
	}
}

func TestSeqRateEdges(t *testing.T) {
	for _, c := range []struct {
		k, n  int
		limit float64
		want  bool
	}{
		{0, 0, 0.25, false},
		{0, 100, 0.25, false},
		{100, 100, 1, false},  // a 100% limit can never be exceeded
		{1, 100, 0, true},     // a zero limit is broken by one bad outcome
		{30, 20, 0.25, true},  // more bad events than actions counts each action once
		{20, 20, 0.9, false},  // 20 of 20 is still plausible at a 90% rate
		{60, 60, 0.9, true},   // 60 of 60 is not
		{5, 20, 0.25, false},  // exactly at the limit is no evidence at all
		{60, 100, 0.25, true}, // far above it is
	} {
		if got := seqRateExceeds(c.k, c.n, c.limit); got != c.want {
			t.Errorf("seqRateExceeds(%d, %d, %.2f) = %v, want %v", c.k, c.n, c.limit, got, c.want)
		}
	}
}
