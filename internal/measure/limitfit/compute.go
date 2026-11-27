package limitfit

import (
	"math"
	"sort"
)

// Gates (docs/limit-fit.md). Below the price gate nothing is shown in percent.
const (
	PriceIntervals    = 10 // clean intervals for the API-price scale
	PriceWindows      = 2  // distinct reset windows for it
	FitIntervals      = 40 // clean intervals for weights per token type
	FitWindows5h      = 4
	FitWindowsWeek    = 2
	FamilyShare       = 0.20 // a family gets its own multiplier when it is at least this share of tokens...
	FamilyIntervals   = 15   // ...in at least this many intervals
	RegimeDays        = 7    // rolling window for regime checks
	regimeBootstrap   = 200
	inputDeterminedAt = 0.5  // input = 1 ratios are shown when the input weight's interval is within ±50%
	RegimeMinChange   = 1.25 // a regime change also needs the scale to move by at least 25% either way
	LookbackDays      = 56   // only the last eight weeks of meter readings are fitted
)

// Tiers.
const (
	TierNone   = "none"       // not enough data: nothing in percent
	TierPrices = "api_prices" // one scale on API-equivalent dollars: "estimated from API prices"
	TierFit    = "fit"        // weights per token type: "estimated from your meter"
)

// Weight is a token type's weight: percentage points of the limit per million tokens.
type Weight struct {
	Type string `json:"type"`
	Range
}

// FamilyMult is a model family's multiplier on the type weights, relative to the reference family.
type FamilyMult struct {
	Family   string `json:"family"`
	Separate bool   `json:"separate"` // false: too little data, counted like the reference family
	Range
}

// MeterFit is what Trove learned about one meter. Every number in it is an estimate.
type MeterFit struct {
	Meter          string         `json:"meter"`
	Tier           string         `json:"tier"`
	CleanIntervals int            `json:"clean_intervals"`
	Windows        int            `json:"windows"`
	Trimmed        int            `json:"trimmed"`
	Dropped        map[string]int `json:"dropped,omitempty"` // intervals left out, by reason
	// NeedIntervals and NeedWindows are how many more clean intervals and reset windows the next tier
	// needs; HoursPerInterval (median) turns that into hours of use.
	NeedIntervals    int     `json:"need_intervals"`
	NeedWindows      int     `json:"need_windows"`
	HoursPerInterval float64 `json:"hours_per_interval"`

	PerUSD  *Range `json:"pct_per_api_usd,omitempty"` // % of the limit per API-equivalent dollar
	Outside *Range `json:"outside_share_pct,omitempty"`

	Weights         []Weight     `json:"weights,omitempty"` // % per million tokens, tier fit
	InputDetermined bool         `json:"input_determined"`
	Ratios          []Weight     `json:"ratios_to_input,omitempty"`
	Reference       string       `json:"reference_family,omitempty"`
	Families        []FamilyMult `json:"families,omitempty"`
	Profile         bool         `json:"borrows_5h_profile,omitempty"` // weekly weights = a scale on the 5h weights

	RegimeSince int64 `json:"regime_since,omitempty"` // unix ms: data before this is not used

	// Reps are the bootstrap rounds: NTypes weights, then the multipliers in Families order. They give
	// intervals for any token delta.
	Reps [][]float64 `json:"reps,omitempty"`
	// USDReps are the rounds' dollar scales.
	USDReps []float64 `json:"usd_reps,omitempty"`
}

// Data is everything Compute needs.
type Data struct {
	Samples        []Sample
	Requests       []Request
	HistoryThrough int64 // unix ms
	Now            int64
}

// Result is the fit for both meters.
type Result struct {
	ComputedAt int64    `json:"computed_at"`
	Samples    int      `json:"samples"`
	LastSample int64    `json:"last_sample"`
	FiveHour   MeterFit `json:"five_hour"`
	Week       MeterFit `json:"seven_day"`
	// Mix is this user's split of context tokens by type (output is 0), to turn a count of context
	// tokens into a delta by type. TopModel is the model with the most tokens.
	Mix      [NTypes]float64 `json:"context_mix"`
	TopModel string          `json:"top_model"`
}

// Compute fits both meters.
func Compute(d Data) Result {
	res := Result{ComputedAt: d.Now, Samples: len(d.Samples)}
	for _, s := range d.Samples {
		res.LastSample = max(res.LastSample, s.TS)
	}
	d = recent(d, res.LastSample-LookbackDays*86_400_000)
	res.TopModel, res.Mix = usage(d.Requests)

	ivs5 := BuildIntervals(d.Samples, d.Requests, FiveHour, d.HistoryThrough)
	since := regimeSince(ivs5)
	res.FiveHour = fitMeter(FiveHour, ivs5, since, nil)
	ivsW := BuildIntervals(d.Samples, d.Requests, SevenDay, d.HistoryThrough)
	res.Week = fitMeter(SevenDay, ivsW, since, &res.FiveHour)
	return res
}

// recent keeps samples and requests from cutoff on.
func recent(d Data, cutoff int64) Data {
	out := Data{HistoryThrough: d.HistoryThrough, Now: d.Now}
	for _, s := range d.Samples {
		if s.TS >= cutoff {
			out.Samples = append(out.Samples, s)
		}
	}
	for _, q := range d.Requests {
		if q.TS >= cutoff {
			out.Requests = append(out.Requests, q)
		}
	}
	return out
}

func usage(reqs []Request) (string, [NTypes]float64) {
	byModel := map[string]int64{}
	var ctx [NTypes]float64
	for _, q := range reqs {
		for k, v := range q.Tokens {
			byModel[q.Model] += v
			if k != TOutput {
				ctx[k] += float64(v)
			}
		}
	}
	top, best := "", int64(-1)
	for m, n := range byModel {
		if n > best || n == best && m < top {
			top, best = m, n
		}
	}
	var sum float64
	for _, v := range ctx {
		sum += v
	}
	if sum > 0 {
		for k := range ctx {
			ctx[k] /= sum
		}
	}
	return top, ctx
}

// split sorts intervals into those used for fitting (clean, with tokens) and every clean one (for
// the outside share), counting what was dropped.
func split(ivs []Interval, since int64) (fit, all []*Interval, dropped map[string]int) {
	dropped = map[string]int{}
	for i := range ivs {
		iv := &ivs[i]
		if iv.End < since {
			continue
		}
		if !iv.Clean {
			dropped[iv.Why]++
			continue
		}
		all = append(all, iv)
		if iv.total() > 0 {
			fit = append(fit, iv)
		}
	}
	return fit, all, dropped
}

func windowsOf(ivs []*Interval) int {
	w := map[int64]bool{}
	for _, iv := range ivs {
		w[iv.Window] = true
	}
	return len(w)
}

func priced(ivs []*Interval) []*Interval {
	var out []*Interval
	for _, iv := range ivs {
		if iv.Priced && iv.USD > 0 {
			out = append(out, iv)
		}
	}
	return out
}

// separable returns the families with enough data for their own multiplier, and the reference family
// (the one with the most tokens).
func separable(ivs []*Interval) (ref string, sep []string, all []string) {
	tokens := map[string]float64{}
	enough := map[string]int{}
	for _, iv := range ivs {
		tot := iv.total()
		for _, ft := range iv.Tokens {
			var s float64
			for _, v := range ft.T {
				s += v
			}
			tokens[ft.Family] += s
			if tot > 0 && s/tot >= FamilyShare {
				enough[ft.Family]++
			}
		}
	}
	for f := range tokens {
		all = append(all, f)
	}
	sort.Strings(all)
	for _, f := range all {
		if ref == "" || tokens[f] > tokens[ref] {
			ref = f
		}
	}
	for _, f := range all {
		if f != ref && enough[f] >= FamilyIntervals {
			sep = append(sep, f)
		}
	}
	return ref, sep, all
}

func fitMeter(meter string, ivs []Interval, since int64, fiveHour *MeterFit) MeterFit {
	fitIvs, all, dropped := split(ivs, since)
	mf := MeterFit{Meter: meter, Tier: TierNone, CleanIntervals: len(fitIvs), Windows: windowsOf(fitIvs), Dropped: dropped, RegimeSince: since}
	if len(dropped) == 0 {
		mf.Dropped = nil
	}
	var spans []float64
	for _, iv := range fitIvs {
		spans = append(spans, float64(iv.End-iv.Start)/3600_000)
	}
	mf.HoursPerInterval = math.Round(median(spans)*100) / 100

	needWin := FitWindows5h
	if meter == SevenDay {
		needWin = FitWindowsWeek
	}
	pr := priced(fitIvs)
	if len(pr) < PriceIntervals || windowsOf(pr) < PriceWindows {
		mf.NeedIntervals = max(0, PriceIntervals-len(pr))
		mf.NeedWindows = max(0, PriceWindows-windowsOf(pr))
		return mf
	}
	// The API-price scale: always fitted once the price gate passes, so dollar figures convert too.
	ps := fitSpec{priceOnly: true}
	pfit := fitTrimmed(pr, ps)
	preps := bootstrap(pr, all, ps, Bootstrap)
	var scales, outs []float64
	for _, r := range preps {
		scales = append(scales, r.scale)
		outs = append(outs, r.outside)
	}
	per := percentiles(pfit.scale, scales)
	mf.PerUSD, mf.USDReps = &per, scales
	out := percentiles(outsideShare(all, ps, &pfit), outs)
	mf.Outside = &out
	mf.Trimmed = count(pfit.trimmed)
	mf.Tier = TierPrices

	if len(fitIvs) < FitIntervals || mf.Windows < needWin {
		mf.NeedIntervals = max(0, FitIntervals-len(fitIvs))
		mf.NeedWindows = max(0, needWin-mf.Windows)
		// The weekly meter can borrow the 5h profile and fit only its own scale.
		if meter == SevenDay && fiveHour != nil && fiveHour.Tier == TierFit {
			borrowProfile(&mf, fitIvs, all, fiveHour)
		}
		return mf
	}

	ref, sep, fams := separable(fitIvs)
	spec := fitSpec{separate: sep}
	f := fitTrimmed(fitIvs, spec)
	reps := bootstrap(fitIvs, all, spec, Bootstrap)
	mf.Tier, mf.Trimmed, mf.Reference = TierFit, count(f.trimmed), ref
	mf.NeedIntervals, mf.NeedWindows = 0, 0
	for k := 0; k < NTypes; k++ {
		var xs []float64
		for _, r := range reps {
			xs = append(xs, r.m.w[k])
		}
		mf.Weights = append(mf.Weights, Weight{Type: TypeNames[k], Range: percentiles(f.m.w[k], xs)})
	}
	for _, fam := range fams {
		fm := FamilyMult{Family: fam, Range: Range{Point: 1, Lo: 1, Hi: 1}}
		if indexOf(sep, fam) >= 0 {
			var xs []float64
			for _, r := range reps {
				xs = append(xs, r.m.mult[fam])
			}
			fm.Separate, fm.Range = true, percentiles(f.m.mult[fam], xs)
		} else if fam == ref {
			fm.Separate = true
		}
		mf.Families = append(mf.Families, fm)
	}
	for _, r := range reps {
		row := append([]float64(nil), r.m.w[:]...)
		for _, fm := range mf.Families {
			v, ok := r.m.mult[fm.Family]
			if !ok {
				v = 1
			}
			row = append(row, v)
		}
		mf.Reps = append(mf.Reps, row)
	}
	var outs2 []float64
	for _, r := range reps {
		outs2 = append(outs2, r.outside)
	}
	o2 := percentiles(outsideShare(all, spec, &f), outs2)
	mf.Outside = &o2
	setRatios(&mf)
	return mf
}

func count(bs []bool) int {
	n := 0
	for _, b := range bs {
		if b {
			n++
		}
	}
	return n
}

// borrowProfile fits the weekly meter as one scale on the 5h meter's type weights and multipliers.
// Its weights are that scale times the 5h weights, paired round by round with the 5h bootstrap.
func borrowProfile(mf *MeterFit, fitIvs, all []*Interval, five *MeterFit) {
	var prof [NTypes]float64
	for k, w := range five.Weights {
		prof[k] = w.Point
	}
	mult := map[string]float64{}
	for _, fm := range five.Families {
		mult[fm.Family] = fm.Point
	}
	spec := fitSpec{profile: &prof, profileMult: mult}
	f := fitTrimmed(fitIvs, spec)
	reps := bootstrap(fitIvs, all, spec, Bootstrap)
	mf.Tier, mf.Profile, mf.Trimmed = TierFit, true, count(f.trimmed)
	mf.Reference, mf.Families = five.Reference, five.Families
	for k := 0; k < NTypes; k++ {
		var xs []float64
		for j, r := range reps {
			w := prof[k]
			if j < len(five.Reps) {
				w = five.Reps[j][k]
			}
			xs = append(xs, r.scale*w)
		}
		mf.Weights = append(mf.Weights, Weight{Type: TypeNames[k], Range: percentiles(f.scale*prof[k], xs)})
	}
	for j, r := range reps {
		var row []float64
		if j < len(five.Reps) {
			row = append(row, five.Reps[j]...)
		} else {
			row = append(append(row, prof[:]...), make([]float64, len(five.Families))...)
			for i, fm := range five.Families {
				row[NTypes+i] = fm.Point
			}
		}
		for k := 0; k < NTypes; k++ {
			row[k] *= r.scale
		}
		mf.Reps = append(mf.Reps, row)
	}
	var outs []float64
	for _, r := range reps {
		outs = append(outs, r.outside)
	}
	o := percentiles(outsideShare(all, spec, &f), outs)
	mf.Outside = &o
	setRatios(mf)
}

// setRatios fills the input = 1 ratios when the input weight is pinned down well enough.
func setRatios(mf *MeterFit) {
	in := mf.Weights[TInput]
	if in.Point <= 0 || (in.Hi-in.Lo)/2 > inputDeterminedAt*in.Point {
		return
	}
	mf.InputDetermined = true
	for k := 0; k < NTypes; k++ {
		var xs []float64
		for _, r := range mf.Reps {
			if r[TInput] > 0 {
				xs = append(xs, r[k]/r[TInput])
			}
		}
		mf.Ratios = append(mf.Ratios, Weight{Type: TypeNames[k], Range: percentiles(mf.Weights[k].Point/in.Point, xs)})
	}
}

// regimeSince checks the 5h meter's API-price scale on rolling RegimeDays windows and returns the
// start of the newest window whose scale's interval does not overlap the previous window's and whose
// scale moved by at least RegimeMinChange, or 0 when nothing changed. Both meters use data from then
// on. The price scale is used because it is one well-determined number per week; it shifts a little
// with the week's mix of models and token types, which is why overlap alone is not enough.
func regimeSince(ivs []Interval) int64 {
	fit, all, _ := split(ivs, 0)
	pr := priced(fit)
	if len(pr) == 0 {
		return 0
	}
	latest := int64(0)
	for _, iv := range pr {
		latest = max(latest, iv.End)
	}
	span := int64(RegimeDays) * 86_400_000
	type win struct {
		start int64
		r     Range
		ok    bool
	}
	var wins []win // newest first
	for k := int64(0); ; k++ {
		hi, lo := latest-k*span, latest-(k+1)*span
		var in, inAll []*Interval
		for _, iv := range pr {
			if iv.End > lo && iv.End <= hi {
				in = append(in, iv)
			}
		}
		for _, iv := range all {
			if iv.End > lo && iv.End <= hi {
				inAll = append(inAll, iv)
			}
		}
		if hi < pr[0].End && len(in) == 0 {
			break
		}
		w := win{start: lo}
		if len(in) >= PriceIntervals && windowsOf(in) >= PriceWindows {
			ps := fitSpec{priceOnly: true}
			f := fitTrimmed(in, ps)
			var xs []float64
			for _, r := range bootstrap(in, inAll, ps, regimeBootstrap) {
				xs = append(xs, r.scale)
			}
			w.r, w.ok = percentiles(f.scale, xs), true
		}
		wins = append(wins, w)
		if k > 520 { // ten years of weeks: a guard, never reached in practice
			break
		}
	}
	for i := 0; i+1 < len(wins); i++ { // newest pair first
		newer, older := wins[i], wins[i+1]
		apart := newer.r.Lo > older.r.Hi || newer.r.Hi < older.r.Lo // the two intervals do not overlap
		big := newer.r.Point >= RegimeMinChange*older.r.Point || older.r.Point >= RegimeMinChange*newer.r.Point
		if newer.ok && older.ok && apart && big {
			return newer.start
		}
	}
	return 0
}
