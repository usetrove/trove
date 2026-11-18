package measure

import "math"

// Verdict is the honest outcome of comparing two groups of sessions.
type Verdict string

const (
	// NotEnoughData means one side has fewer sessions than the minimum. No conclusion
	// of any kind may be drawn or shown.
	NotEnoughData Verdict = "not_enough_data"
	// NoRegression means nothing worse was detected. It does NOT mean quality is
	// unchanged; with a small sample, real harm can hide.
	NoRegression Verdict = "no_regression_detected"
	// Regression means the treatment is worse by more than the threshold and more
	// than chance alone would explain at roughly 95% one-sided confidence.
	Regression Verdict = "regression"
)

// z95 is the one-sided 95% normal quantile.
const z95 = 1.645

// Comparison is the result of one rule applied to a treatment group and a reference.
type Comparison struct {
	Verdict  Verdict `json:"verdict"`
	Treated  float64 `json:"treated"`
	Ref      float64 `json:"reference"`
	Change   float64 `json:"change"` // relative for means, absolute points for rates
	NTreated int     `json:"n_treated"`
	NRef     int     `json:"n_reference"`
}

func mean(x []float64) float64 {
	if len(x) == 0 {
		return 0
	}
	var s float64
	for _, v := range x {
		s += v
	}
	return s / float64(len(x))
}

func sampleVar(x []float64) float64 {
	if len(x) < 2 {
		return 0
	}
	m := mean(x)
	var s float64
	for _, v := range x {
		s += (v - m) * (v - m)
	}
	return s / float64(len(x)-1)
}

// compareMeans asks whether treated has a higher mean than ref by at least
// relIncrease (0.30 = 30%) and by more than chance. Chance is judged by a
// sequential test (seq.go) because the check is repeated on a growing sample.
// When the reference mean is zero a relative change is undefined; then an
// absolute rise of at least 0.5 per session is required instead.
func compareMeans(treated, ref []float64, minN int, relIncrease float64) Comparison {
	c := Comparison{Verdict: NotEnoughData, NTreated: len(treated), NRef: len(ref)}
	if len(treated) < minN || len(ref) < minN {
		return c
	}
	return meansVerdict(c, mean(treated), mean(ref), sampleVar(treated), sampleVar(ref), relIncrease)
}

// meansVerdict fills c from summary statistics; c carries the sample sizes.
func meansVerdict(c Comparison, meanT, meanR, varT, varR, relIncrease float64) Comparison {
	c.Treated, c.Ref = meanT, meanR
	diff := c.Treated - c.Ref
	se := math.Sqrt(varT/float64(c.NTreated) + varR/float64(c.NRef))
	big := false
	tau := 0.5 // the mixture is tuned to the smallest rise the rule cares about
	if c.Ref == 0 {
		c.Change = diff
		big = diff >= 0.5
	} else {
		c.Change = diff / c.Ref
		big = c.Change >= relIncrease
		tau = relIncrease * math.Abs(c.Ref)
	}
	c.Verdict = NoRegression
	if big && seqMeanExceeds(diff, se, tau) {
		c.Verdict = Regression
	}
	return c
}

// compareRateToLimit flags treated when k of n is evidence, valid however
// often it is checked, that the rate is above limit (a fraction, 0.25 = 25%).
// The reference plays no part.
func compareRateToLimit(k, n, minN int, limit float64) Comparison {
	c := Comparison{Verdict: NotEnoughData, NTreated: n}
	if n < minN {
		return c
	}
	c.Treated = float64(k) / float64(n)
	c.Verdict = NoRegression
	if seqRateExceeds(k, n, limit) {
		c.Verdict = Regression
	}
	return c
}
