package limitfit

import (
	"math"
	"math/rand/v2"
	"testing"
)

func TestNNLS(t *testing.T) {
	cases := []struct {
		name string
		A    [][]float64
		b    []float64
		want []float64
	}{
		{"exact positive", [][]float64{{1, 0}, {0, 1}, {1, 1}}, []float64{2, 3, 5}, []float64{2, 3}},
		{"negative clamped", [][]float64{{1, 0}, {0, 1}}, []float64{2, -3}, []float64{2, 0}},
		{"zero column", [][]float64{{1, 0}, {2, 0}}, []float64{1, 2}, []float64{1, 0}},
		{"scales differ by 1e6", [][]float64{{1e6, 1}, {2e6, 3}, {5e5, 2}}, []float64{2 + 4, 4 + 12, 1 + 8}, []float64{2e-6, 4}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := nnls(c.A, c.b)
			for j := range c.want {
				if math.Abs(got[j]-c.want[j]) > 1e-6*math.Max(1, math.Abs(c.want[j])) {
					t.Errorf("x[%d] = %g, want %g", j, got[j], c.want[j])
				}
			}
		})
	}
}

// On random problems NNLS must satisfy the KKT conditions: x >= 0, gradient <= 0 where x = 0 and
// gradient ~ 0 where x > 0.
func TestNNLSOptimality(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for trial := 0; trial < 200; trial++ {
		m, n := 8+rng.IntN(20), 1+rng.IntN(6)
		A := make([][]float64, m)
		b := make([]float64, m)
		for i := range A {
			A[i] = make([]float64, n)
			for j := range A[i] {
				A[i][j] = rng.Float64() * math.Pow(10, float64(j))
			}
			b[i] = rng.NormFloat64() * 10
		}
		x := nnls(A, b)
		for j := 0; j < n; j++ {
			if x[j] < 0 {
				t.Fatalf("trial %d: negative x", trial)
			}
			g, norm := 0.0, 0.0
			for i := range A {
				r := b[i]
				for k := 0; k < n; k++ {
					r -= A[i][k] * x[k]
				}
				g += A[i][j] * r
				norm += A[i][j] * A[i][j]
			}
			g /= math.Sqrt(norm) // scale-free gradient
			if x[j] > 0 && math.Abs(g) > 1e-5 || x[j] == 0 && g > 1e-5 {
				t.Fatalf("trial %d: KKT fails at %d: x=%g g=%g", trial, j, x[j], g)
			}
		}
	}
}
