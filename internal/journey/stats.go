package journey

import (
	"math"
	"math/rand/v2"
	"sort"
)

// bootstrapResamples is the number of seeded resamples for a median's interval.
const bootstrapResamples = 2000

// Median returns the median of xs, or 0 for an empty slice. xs is not modified.
func Median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := sortedCopy(xs)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// Quartiles returns the first and third quartiles (linear interpolation, the
// same definition as numpy's default). It returns zeros for an empty slice.
func Quartiles(xs []float64) (q1, q3 float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	s := sortedCopy(xs)
	return quantile(s, 0.25), quantile(s, 0.75)
}

// MedianCI is a percentile bootstrap 95% interval for the median of xs. The
// resampling is seeded, so the same inputs always give the same interval.
func MedianCI(xs []float64, r *rand.Rand) (lo, hi float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	if len(xs) == 1 {
		return xs[0], xs[0]
	}
	meds := make([]float64, bootstrapResamples)
	buf := make([]float64, len(xs))
	for i := range meds {
		for j := range buf {
			buf[j] = xs[r.IntN(len(xs))]
		}
		meds[i] = Median(buf)
	}
	sort.Float64s(meds)
	return quantile(meds, 0.025), quantile(meds, 0.975)
}

// Within reports whether a 95% interval includes zero, i.e. the difference
// cannot be told apart from noise at this sample size.
func Within(lo, hi float64) bool { return lo <= 0 && hi >= 0 }

func sortedCopy(xs []float64) []float64 {
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	return s
}

// quantile expects s sorted ascending.
func quantile(s []float64, p float64) float64 {
	if len(s) == 1 {
		return s[0]
	}
	h := p * float64(len(s)-1)
	lo := math.Floor(h)
	hi := math.Ceil(h)
	return s[int(lo)] + (h-lo)*(s[int(hi)]-s[int(lo)])
}
