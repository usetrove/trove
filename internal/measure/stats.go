package measure

import "math"

// Verdict is the honest outcome of comparing two groups of sessions.
type Verdict string

const (
	// NotEnoughData: one side has fewer sessions than the minimum. No conclusion
	// of any kind may be drawn or shown.
	NotEnoughData Verdict = "not_enough_data"
	// NoRegression: nothing worse was detected. It does NOT mean quality is
	// unchanged; with a small sample, real harm can hide.
	NoRegression Verdict = "no_regression_detected"
	// Regression: the treatment is worse by more than the threshold and more
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
// relIncrease (0.30 = 30%) and by more than chance (Welch, one-sided 95%).
// When the reference mean is zero a relative change is undefined; then an
// absolute rise of at least 0.5 per session is required instead.
func compareMeans(treated, ref []float64, minN int, relIncrease float64) Comparison {
	c := Comparison{Verdict: NotEnoughData, NTreated: len(treated), NRef: len(ref)}
	if len(treated) < minN || len(ref) < minN {
		return c
	}
	c.Treated, c.Ref = mean(treated), mean(ref)
	diff := c.Treated - c.Ref
	se := math.Sqrt(sampleVar(treated)/float64(len(treated)) + sampleVar(ref)/float64(len(ref)))
	big := false
	if c.Ref == 0 {
		c.Change = diff
		big = diff >= 0.5
	} else {
		c.Change = diff / c.Ref
		big = c.Change >= relIncrease
	}
	c.Verdict = NoRegression
	if big && diff > z95*se {
		c.Verdict = Regression
	}
	return c
}

// wilsonLower is the lower end of the 95% Wilson interval for k of n.
func wilsonLower(k, n int) float64 {
	if n == 0 {
		return 0
	}
	p, nn := float64(k)/float64(n), float64(n)
	z2 := 1.96 * 1.96
	return (p + z2/(2*nn) - 1.96*math.Sqrt(p*(1-p)/nn+z2/(4*nn*nn))) / (1 + z2/nn)
}

// compareRateToLimit flags treated when even the low end of its 95% interval
// is above limit (a fraction, 0.25 = 25%). The reference plays no part.
func compareRateToLimit(k, n, minN int, limit float64) Comparison {
	c := Comparison{Verdict: NotEnoughData, NTreated: n}
	if n < minN {
		return c
	}
	c.Treated = float64(k) / float64(n)
	c.Verdict = NoRegression
	if wilsonLower(k, n) > limit {
		c.Verdict = Regression
	}
	return c
}

// compareRates flags treated when its rate is at least minRise (absolute,
// 0.20 = 20 points) above ref's and the rise is beyond chance (two-proportion
// z test, one-sided 95%).
func compareRates(k1, n1, k2, n2, minN int, minRise float64) Comparison {
	c := Comparison{Verdict: NotEnoughData, NTreated: n1, NRef: n2}
	if n1 < minN || n2 < minN {
		return c
	}
	p1, p2 := float64(k1)/float64(n1), float64(k2)/float64(n2)
	c.Treated, c.Ref, c.Change = p1, p2, p1-p2
	c.Verdict = NoRegression
	pool := float64(k1+k2) / float64(n1+n2)
	se := math.Sqrt(pool * (1 - pool) * (1/float64(n1) + 1/float64(n2)))
	if c.Change >= minRise && c.Change > z95*se {
		c.Verdict = Regression
	}
	return c
}
