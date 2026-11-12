package stress

import (
	"math"
	"math/rand/v2"
	"sort"
)

// driverShuffles is how many shuffles set each parameter's noise floor.
const driverShuffles = 40

// numericParams and categoricalParams are the parameters the ranking considers. A session's profile label is
// deliberately absent: the simulator gives it no behavior, so any spread it showed would be sampling noise.
var numericParams = map[string]func(SessionSpec) float64{
	"prefix_tokens":   func(s SessionSpec) float64 { return float64(s.PrefixTokens) },
	"session_turns":   func(s SessionSpec) float64 { return float64(s.Turns) },
	"output_median_b": func(s SessionSpec) float64 { return float64(s.OutMedianBytes) },
	"cache_expiry_p":  func(s SessionSpec) float64 { return s.ExpiryProb },
	"repeat_read_p":   func(s SessionSpec) float64 { return s.RepeatProb },
	"ansi_crlf_p":     func(s SessionSpec) float64 { return s.NoiseProb },
}

var categoricalParams = map[string]func(SessionSpec) string{
	"model":    func(s SessionSpec) string { return s.Model },
	"tool_mix": func(s SessionSpec) string { return s.Mix },
}

// drivers ranks the parameters by how strongly they move the estimated saving, and marks each one against its
// own noise floor. Numeric parameters get |Spearman rho|; categorical ones get the spread of group medians in
// percentage points. Groups with fewer than three sessions are ignored.
func drivers(rows []sampleRow, seed uint64) []Driver {
	var specs []SessionSpec
	var saved []float64
	for _, r := range rows {
		if !math.IsNaN(r.saved) {
			specs = append(specs, r.spec)
			saved = append(saved, r.saved)
		}
	}
	var out []Driver
	for name, f := range numericParams {
		stat := func(ys []float64) float64 {
			xs := make([]float64, len(specs))
			for i, s := range specs {
				xs[i] = f(s)
			}
			return math.Abs(spearman(xs, ys))
		}
		out = append(out, rank(name, "numeric", stat, saved, rng(seed, "shuffle", name)))
	}
	for name, f := range categoricalParams {
		stat := func(ys []float64) float64 {
			groups := map[string][]float64{}
			for i, s := range specs {
				groups[f(s)] = append(groups[f(s)], ys[i])
			}
			return groupSpread(groups)
		}
		out = append(out, rank(name, "categorical", stat, saved, rng(seed, "shuffle", name)))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Strength != out[j].Strength {
			return out[i].Strength > out[j].Strength
		}
		return out[i].Parameter < out[j].Parameter
	})
	for i := range out {
		out[i].Rank = i + 1
	}
	return out
}

// rank computes one parameter's strength on the real savings and its noise floor on shuffled savings.
func rank(name, kind string, stat func([]float64) float64, saved []float64, r *rand.Rand) Driver {
	d := Driver{Parameter: name, Kind: kind, Strength: round2(stat(saved))}
	noise := make([]float64, 0, driverShuffles)
	for i := 0; i < driverShuffles; i++ {
		ys := append([]float64(nil), saved...)
		r.Shuffle(len(ys), func(a, b int) { ys[a], ys[b] = ys[b], ys[a] })
		noise = append(noise, stat(ys))
	}
	sort.Float64s(noise)
	d.Noise = round2(quantile(noise, 0.95))
	d.Distinct = d.Strength > d.Noise
	return d
}

// groupSpread is the difference between the highest and lowest group medians, in percentage points.
func groupSpread(groups map[string][]float64) float64 {
	lo, hi := math.Inf(1), math.Inf(-1)
	n := 0
	for _, g := range groups {
		if len(g) < 3 {
			continue
		}
		n++
		m := quantile(sortedCopy(g), 0.5)
		lo, hi = math.Min(lo, m), math.Max(hi, m)
	}
	if n < 2 {
		return 0
	}
	return round1(hi - lo)
}
