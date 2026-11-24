package limitfit

import (
	"encoding/json"
	"math"
	"math/rand/v2"
	"testing"
)

// sim describes a synthetic account whose meter follows known weights.
type sim struct {
	seed       uint64
	days       int
	w          [NTypes]float64 // true 5h weights, % per million tokens
	opusMult   float64         // true multiplier for opus (sonnet = 1)
	weekScale  float64         // weekly % = weekScale * 5h %
	drainPerH  float64         // chance per active hour of an outside burst
	quantum    float64         // meter resolution: 1 or 0.1
	offDays    map[int]bool    // days the status line was off: no samples
	unmetered  float64         // chance per request of a background API-key request that never moves the meter
	changeDay  int             // from this day on, every weight is multiplied by changeMult (0 = never)
	changeMult float64
}

var truth = [NTypes]float64{TInput: 2.0, TOutput: 12.0, TCacheRead: 0.25, TWrite5m: 2.5, TWrite1h: 4.0}

func baseSim(seed uint64) sim {
	return sim{seed: seed, days: 30, w: truth, opusMult: 1.8, weekScale: 0.08, drainPerH: 0.15, quantum: 1}
}

// prices per million tokens, sonnet-like; opus costs 5/3 as much.
var price = [NTypes]float64{3, 15, 0.3, 3.75, 6}

// run simulates the account and returns the data plus the true share of meter movement from outside.
func (s sim) run() (Data, float64) {
	rng := rand.New(rand.NewPCG(s.seed, 99))
	const hour, minute = int64(3600_000), int64(60_000)
	start := int64(1_790_000_000_000)
	var d Data
	five, week := 0.0, 0.0
	var fiveReset int64
	weekReset := start + 7*24*hour
	var outside, moved float64
	lastFive, lastWeek := -1.0, -1.0
	q := func(v float64) float64 { return math.Floor(v/s.quantum) * s.quantum }
	sess := 0
	for day := 0; day < s.days; day++ {
		mult := 1.0
		if s.changeDay > 0 && day >= s.changeDay {
			mult = s.changeMult
		}
		for sn := 0; sn < 2; sn++ { // two sessions a day
			sess++
			id := string(rune('a'+sess%26)) + string(rune('a'+sess/26%26)) + string(rune('a'+sess/676))
			metered := true
			fam, fm, pm := "claude-sonnet-5-5", 1.0, 1.0
			if rng.Float64() < 0.3 {
				fam, fm, pm = "claude-opus-5-5", s.opusMult, 5.0/3
			}
			t := start + int64(day)*24*hour + int64(9+sn*5)*hour + rng.Int64N(30)*minute
			ctx := 20_000.0
			for r := 0; r < 90+rng.IntN(60); r++ {
				t += minute/2 + rng.Int64N(2*minute)
				if t > weekReset {
					week, weekReset, lastWeek = 0, weekReset+7*24*hour, -1
				}
				if t > fiveReset {
					five, fiveReset, lastFive = 0, t+5*hour, -1
				}
				var tok [NTypes]int64
				tok[TInput] = 5 + rng.Int64N(400)
				tok[TOutput] = 100 + rng.Int64N(2500)
				tok[TCacheRead] = int64(ctx)
				w := 500 + rng.Int64N(4000)
				if rng.Float64() < 0.08 {
					w += 20_000 + rng.Int64N(40_000) // an occasional rebuild
				}
				if rng.Float64() < 0.6 {
					tok[TWrite1h] = w
				} else {
					tok[TWrite5m] = w
				}
				ctx += float64(w) * 0.6
				if ctx > 180_000 {
					ctx = 30_000 // compaction
				}
				var inc, usd float64
				for k, v := range tok {
					inc += s.w[k] * mult * fm * float64(v) / 1e6
					usd += price[k] * pm * float64(v) / 1e6
				}
				if rng.Float64() < s.drainPerH/60 { // claude.ai, another machine...
					b := 1 + rng.Float64()*4
					outside += b
					moved += b
					inc += b
				}
				moved += inc - 0 // includes the burst
				five += inc
				week += inc * s.weekScale
				d.Requests = append(d.Requests, Request{TS: t, Session: id, Model: fam, Tokens: tok, USD: usd, Priced: true})
				if rng.Float64() < s.unmetered {
					// A background job on an API key: its tokens never move the meter.
					d.Requests = append(d.Requests, Request{TS: t + 500, Session: "api-key-job", Model: fam, Tokens: tok, USD: usd, Priced: true})
				}
				if !metered || s.offDays[day] {
					continue
				}
				// The status line runs a moment after the response; the meter may lag a few seconds.
				f5, fw := q(five), q(week*10)/10
				if f5 == lastFive && fw == lastWeek {
					continue
				}
				lastFive, lastWeek = f5, fw
				fr, wr := fiveReset, weekReset
				d.Samples = append(d.Samples, Sample{TS: t + 2000 + rng.Int64N(3000), Session: id,
					FiveHour: ptrF(f5), FiveHourReset: &fr, Week: ptrF(fw), WeekReset: &wr})
			}
		}
	}
	d.HistoryThrough = start + int64(s.days+1)*24*hour
	d.Now = d.HistoryThrough
	// moved double counts bursts above; recompute the outside share as bursts / all movement.
	return d, outside / (moved - outside + outside) * 100
}

func ptrF(v float64) *float64 { return &v }

func TestRecoversKnownWeights(t *testing.T) {
	if testing.Short() {
		t.Skip("bootstrap fits take a few seconds")
	}
	covered := [NTypes]int{}
	seeds := []uint64{1, 2, 3, 4, 5}
	for _, seed := range seeds {
		d, _ := baseSim(seed).run()
		r := Compute(d)
		f := r.FiveHour
		if f.Tier != TierFit {
			t.Fatalf("seed %d: tier %s (%d clean intervals, %d windows, dropped %v)", seed, f.Tier, f.CleanIntervals, f.Windows, f.Dropped)
		}
		for k, w := range f.Weights {
			if w.Contains(truth[k]) {
				covered[k]++
			}
		}
		var opus *FamilyMult
		for i := range f.Families {
			if f.Families[i].Family == "opus" {
				opus = &f.Families[i]
			}
		}
		if f.Reference != "sonnet" || opus == nil || !opus.Separate || math.Abs(opus.Point-1.8) > 0.4 {
			t.Errorf("seed %d: families %+v (reference %s)", seed, f.Families, f.Reference)
		}
	}
	// The types that carry most of the meter must be recovered in nearly every seed. Input is a tiny
	// share of tokens in this workload, so its interval is allowed to miss more often.
	for _, k := range []int{TOutput, TCacheRead, TWrite1h} {
		if covered[k] < len(seeds)-1 {
			t.Errorf("%s: truth inside the 90%% interval in %d of %d seeds", TypeNames[k], covered[k], len(seeds))
		}
	}
}

func TestOutsideDrainIsItsOwnLine(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	s := baseSim(7)
	s.drainPerH = 1.0
	d, share := s.run()
	r := Compute(d)
	o := r.FiveHour.Outside
	if o == nil || math.Abs(o.Point-share) > 8 {
		t.Fatalf("outside share %+v, true %.1f%%", o, share)
	}
	quiet := baseSim(7)
	quiet.drainPerH = 0
	d, _ = quiet.run()
	if o := Compute(d).FiveHour.Outside; o == nil || o.Point > 5 {
		t.Errorf("no outside drain, but the share is %+v", o)
	}
}

func TestGates(t *testing.T) {
	cases := []struct {
		days int
		want string
	}{
		{1, TierNone},
		{5, TierPrices},
		{30, TierFit},
	}
	for _, c := range cases {
		s := baseSim(11)
		s.days = c.days
		d, _ := s.run()
		f := Compute(d).FiveHour
		if f.Tier != c.want {
			t.Errorf("%d days: tier %s, want %s (%d clean, %d windows)", c.days, f.Tier, c.want, f.CleanIntervals, f.Windows)
		}
		if c.want != TierFit && f.NeedIntervals+f.NeedWindows == 0 {
			t.Errorf("%d days: below the top tier the fit must say what it still needs", c.days)
		}
		if c.want == TierNone && (f.PerUSD != nil || len(f.Weights) > 0) {
			t.Errorf("%d days: nothing may be shown below the price gate", c.days)
		}
	}
	if f := Compute(Data{}).FiveHour; f.Tier != TierNone || f.NeedIntervals != PriceIntervals {
		t.Errorf("no data: %+v", f)
	}
}

// Days without the status line and sessions that never send the meter must be left out, not guessed.
func TestMissingWindowsAndUnmeteredSessions(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	s := baseSim(13)
	s.days = 40
	s.offDays = map[int]bool{3: true, 4: true, 10: true, 17: true, 18: true, 19: true}
	s.unmetered = 0.01
	d, _ := s.run()
	f := Compute(d).FiveHour
	if f.Dropped[whyUnknown] == 0 {
		t.Errorf("intervals with unmetered sessions should be dropped: %v", f.Dropped)
	}
	if f.Tier != TierFit || !f.Weights[TOutput].Contains(truth[TOutput]) || !f.Weights[TCacheRead].Contains(truth[TCacheRead]) {
		t.Errorf("fit with gaps: tier %s weights %+v", f.Tier, f.Weights)
	}
}

func TestRegimeChange(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	s := baseSim(17)
	s.days = 56
	s.changeDay, s.changeMult = 28, 1.8
	d, _ := s.run()
	r := Compute(d)
	f := r.FiveHour
	changeAt := int64(1_790_000_000_000) + 28*86_400_000
	if f.RegimeSince == 0 || math.Abs(float64(f.RegimeSince-changeAt)) > 8*86_400_000 {
		t.Fatalf("regime change not found near day 28: since %d (change at %d)", f.RegimeSince, changeAt)
	}
	if f.Tier != TierFit {
		t.Fatalf("after the change: tier %s, need %d", f.Tier, f.NeedIntervals)
	}
	// After the change the fit must describe the new regime, not a blend: within 20% of the new truth.
	for _, k := range []int{TOutput, TCacheRead, TWrite1h} {
		want := truth[k] * 1.8
		if w := f.Weights[k]; !w.Contains(want) && math.Abs(w.Point-want) > 0.2*want {
			t.Errorf("%s weight %+v should be the new regime's %.2f", TypeNames[k], w, want)
		}
	}
	// No change, no flag.
	calm := baseSim(17)
	calm.days = 56
	d, _ = calm.run()
	if since := Compute(d).FiveHour.RegimeSince; since != 0 {
		t.Errorf("false regime change at %d", since)
	}
}

func TestWeekBorrowsOrFits(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	d, _ := baseSim(19).run()
	r := Compute(d)
	w := r.Week
	if w.Tier != TierFit || len(w.Weights) != NTypes {
		t.Fatalf("week: tier %s", w.Tier)
	}
	want := truth[TOutput] * 0.08
	if !w.Weights[TOutput].Contains(want) && math.Abs(w.Weights[TOutput].Point-want) > 0.25*want {
		t.Errorf("week output weight %+v, want about %.2f", w.Weights[TOutput], want)
	}
}

func TestComputeIsDeterministic(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	d, _ := baseSim(23).run()
	a, _ := json.Marshal(Compute(d))
	// Shuffle the inputs: the result must not depend on their order.
	rng := rand.New(rand.NewPCG(5, 5))
	rng.Shuffle(len(d.Samples), func(i, j int) { d.Samples[i], d.Samples[j] = d.Samples[j], d.Samples[i] })
	rng.Shuffle(len(d.Requests), func(i, j int) { d.Requests[i], d.Requests[j] = d.Requests[j], d.Requests[i] })
	b, _ := json.Marshal(Compute(d))
	if string(a) != string(b) {
		t.Error("same data, different fit")
	}
}
