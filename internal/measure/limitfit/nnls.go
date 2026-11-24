package limitfit

import "math"

// nnls solves min ||A x - b|| subject to x >= 0 with the Lawson-Hanson active-set method, working on
// the normal equations (the problems here have at most a dozen columns). Columns are scaled to unit
// length first, because token counts per type differ by orders of magnitude. A column of zeros gets 0.
func nnls(A [][]float64, b []float64) []float64 {
	n := 0
	if len(A) > 0 {
		n = len(A[0])
	}
	if n == 0 {
		return nil
	}
	G := make([][]float64, n)
	for i := range G {
		G[i] = make([]float64, n)
	}
	h := make([]float64, n)
	for r, row := range A {
		for j := 0; j < n; j++ {
			if row[j] == 0 {
				continue
			}
			h[j] += row[j] * b[r]
			for k := j; k < n; k++ {
				G[j][k] += row[j] * row[k]
			}
		}
	}
	symmetric(G)
	return nnlsGram(G, h)
}

// nnlsGram solves the same problem given G = AᵀA and h = Aᵀb. It scales the columns to unit length
// (from G's diagonal) first and leaves G and h unchanged.
func nnlsGram(G [][]float64, h []float64) []float64 {
	n := len(h)
	scale := make([]float64, n)
	for j := range scale {
		scale[j] = math.Sqrt(math.Max(0, G[j][j]))
	}
	Gs := make([][]float64, n)
	hs := make([]float64, n)
	for j := range Gs {
		Gs[j] = make([]float64, n)
		if scale[j] == 0 {
			continue
		}
		hs[j] = h[j] / scale[j]
		for k := range Gs[j] {
			if scale[k] != 0 {
				Gs[j][k] = G[j][k] / (scale[j] * scale[k])
			}
		}
	}
	y := nnlsNormal(Gs, hs, scale)
	for j := range y {
		if scale[j] > 0 {
			y[j] /= scale[j]
		}
	}
	return y
}

// nnlsNormal runs Lawson-Hanson on G y = h. Columns with zero scale stay out of the problem.
func nnlsNormal(G [][]float64, h, scale []float64) []float64 {
	n := len(h)
	const tol = 1e-12
	x := make([]float64, n)
	passive := make([]bool, n)
	grad := func() []float64 {
		w := make([]float64, n)
		for j := 0; j < n; j++ {
			w[j] = h[j]
			for k := 0; k < n; k++ {
				w[j] -= G[j][k] * x[k]
			}
		}
		return w
	}
	for iter := 0; iter < 3*n+10; iter++ {
		w := grad()
		t, best := -1, tol
		for j := 0; j < n; j++ {
			if !passive[j] && scale[j] > 0 && w[j] > best {
				t, best = j, w[j]
			}
		}
		if t < 0 {
			break
		}
		passive[t] = true
		for inner := 0; inner < 3*n+10; inner++ {
			z := solvePassive(G, h, passive)
			ok := true
			for j := 0; j < n; j++ {
				if passive[j] && z[j] <= tol {
					ok = false
				}
			}
			if ok {
				copy(x, z)
				break
			}
			alpha := math.Inf(1)
			for j := 0; j < n; j++ {
				if passive[j] && z[j] <= tol && x[j]-z[j] > 0 {
					alpha = math.Min(alpha, x[j]/(x[j]-z[j]))
				}
			}
			if math.IsInf(alpha, 1) {
				alpha = 0
			}
			for j := 0; j < n; j++ {
				x[j] += alpha * (z[j] - x[j])
				if passive[j] && x[j] <= tol {
					passive[j], x[j] = false, 0
				}
			}
		}
	}
	return x
}

// solvePassive solves the unconstrained least-squares problem on the passive columns by Cholesky,
// with a tiny ridge for columns that are nearly collinear. Other entries are 0.
func solvePassive(G [][]float64, h []float64, passive []bool) []float64 {
	var idx []int
	for j, p := range passive {
		if p {
			idx = append(idx, j)
		}
	}
	m := len(idx)
	L := make([][]float64, m)
	for i := range L {
		L[i] = make([]float64, m)
	}
	for a := 0; a < m; a++ {
		for c := 0; c <= a; c++ {
			s := G[idx[a]][idx[c]]
			if a == c {
				s += 1e-10 // columns are unit length, so this is a relative ridge
			}
			for k := 0; k < c; k++ {
				s -= L[a][k] * L[c][k]
			}
			if a == c {
				if s <= 0 {
					s = 1e-12
				}
				L[a][a] = math.Sqrt(s)
			} else {
				L[a][c] = s / L[c][c]
			}
		}
	}
	y := make([]float64, m)
	for a := 0; a < m; a++ {
		s := h[idx[a]]
		for k := 0; k < a; k++ {
			s -= L[a][k] * y[k]
		}
		y[a] = s / L[a][a]
	}
	for a := m - 1; a >= 0; a-- {
		for k := a + 1; k < m; k++ {
			y[a] -= L[k][a] * y[k]
		}
		y[a] /= L[a][a]
	}
	out := make([]float64, len(passive))
	for a, j := range idx {
		out[j] = y[a]
	}
	return out
}
