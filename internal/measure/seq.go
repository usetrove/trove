package measure

import "math"

// Sequential, always-valid tests for the rollback checks.
//
// The guardrails run every few minutes and again at every session end, so the
// same growing sample is tested over and over. A fixed-sample test (a z-test at
// 95%, a Wilson bound, or a bare "rate >= limit") checked that often will
// eventually fire on noise alone: its false-rollback rate grows with the
// number of looks. The tests below are built on test martingales, so by
// Ville's inequality the chance that they EVER fire when the optimizer is not
// worse is at most seqAlpha, however often and whenever they are checked.
// See docs/measurement.md ("Sequential rollback checks").

// seqAlpha is the most a rollback check may fire, over its whole life, when the
// optimizer is in fact not worse (one-sided).
const seqAlpha = 0.05

// seqRateGrid is the number of alternative rates the Bernoulli mixture averages over.
const seqRateGrid = 50

// seqRateExceeds reports whether k "bad" outcomes in n actions are evidence,
// valid at every n, that the true rate is above limit (a fraction).
//
// It is a mixture likelihood-ratio test: the average, over alternatives q
// spread evenly over (limit, 1), of (q/limit)^k ((1-q)/(1-limit))^(n-k). For
// every such q and every true rate p <= limit the ratio is a nonnegative
// supermartingale, so its average is too, and it reaches 1/seqAlpha with
// probability at most seqAlpha. The result is exact: no normal approximation.
func seqRateExceeds(k, n int, limit float64) bool {
	if n <= 0 || k <= 0 || limit >= 1 {
		return false
	}
	if k > n {
		k = n // more "bad" events than actions: count each action once
	}
	if limit <= 0 {
		return true // any bad outcome breaks a zero limit
	}
	kf, mf := float64(k), float64(n-k)
	logs := make([]float64, seqRateGrid)
	top := math.Inf(-1)
	for j := range logs {
		q := limit + (1-limit)*float64(j+1)/float64(seqRateGrid+1)
		l := kf * math.Log(q/limit)
		if mf > 0 {
			l += mf * math.Log((1-q)/(1-limit))
		}
		logs[j] = l
		top = math.Max(top, l)
	}
	var s float64
	for _, l := range logs {
		s += math.Exp(l - top)
	}
	logMix := top + math.Log(s/float64(seqRateGrid))
	return logMix >= math.Log(1/seqAlpha)
}

// seqMeanBound is the smallest difference in means (treated minus reference)
// that counts as beyond chance at every look, given the standard error of the
// difference se and a mixing scale tau (the size of effect the test is tuned
// for). It is the normal-mixture sequential probability ratio test (Robbins
// 1970; Johari, Pekelis and Walsh 2017, "Always valid inference"): with a
// N(0, tau^2) mixture over the effect, the likelihood ratio reaches
// 1/seqAlpha exactly when
//
//	diff >= sqrt( se^2 (se^2 + tau^2) / tau^2 * (2 ln(1/alpha) + ln(1 + tau^2/se^2)) ).
//
// The variance is estimated from the data, so the guarantee is asymptotic;
// the stress test in seq_test.go checks it holds at the sample sizes used here.
func seqMeanBound(se, tau float64) float64 {
	if se <= 0 {
		return 0
	}
	if tau <= 0 {
		tau = se
	}
	v, t2 := se*se, tau*tau
	return math.Sqrt(v * (v + t2) / t2 * (2*math.Log(1/seqAlpha) + math.Log1p(t2/v)))
}

// seqMeanExceeds reports whether diff is beyond chance at every look.
func seqMeanExceeds(diff, se, tau float64) bool {
	if se <= 0 {
		return diff > 0 // no spread at all: the difference is not noise
	}
	return diff > seqMeanBound(se, tau)
}
