package regress

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 26, 15, 0, 0, 0, time.UTC)

// hist builds synthetic histories, deterministically.
type hist struct {
	rng  *rand.Rand
	reqs []Request
	ivs  []Interval
}

func newHist() *hist { return &hist{rng: rand.New(rand.NewPCG(1, 2))} }

// day adds n requests on day d (0 = today, 1 = yesterday...) for version v over sessions, with thinking
// around mean (± spread), a share withThinking of them thinking at all, and a rebuild rate.
func (h *hist) day(d int, v string, n, sessions int, mean, spread float64, withThinking, rebuilds float64) {
	h.dayOn(d, v, "claude-opus-5-5", "high", n, sessions, mean, spread, withThinking, rebuilds)
}

func (h *hist) dayOn(d int, v, model, effort string, n, sessions int, mean, spread float64, withThinking, rebuilds float64) {
	start := time.Date(now.Year(), now.Month(), now.Day()-d, 1, 0, 0, 0, time.UTC).UnixMilli()
	for i := 0; i < n; i++ {
		r := Request{TS: start + int64(i)*1000, Session: fmt.Sprintf("%s-d%d-s%d", v, d, i%sessions), Version: v,
			Model: model, Effort: effort, Split: true, ThinkingMeasured: true}
		if h.rng.Float64() < withThinking {
			r.Thinking = int64(mean + spread*(2*h.rng.Float64()-1))
		}
		r.Unexplained = h.rng.Float64() < rebuilds
		h.reqs = append(h.reqs, r)
	}
}

func (h *hist) baseline(v string, days int) {
	for d := days + 1; d >= 2; d-- {
		h.day(d, v, 60, 3, 1000, 300, 0.5, 0.01)
	}
}

func (h *hist) run() Result { return Compute(Data{Requests: h.reqs, Intervals: h.ivs, Now: now}) }

func find(r Result, kind, key, metric string) *Check {
	for i := range r.Checks {
		c := &r.Checks[i]
		if c.Kind == kind && c.Key == key && c.Metric == metric {
			return c
		}
	}
	return nil
}

func TestRealClientRegression(t *testing.T) {
	h := newHist()
	h.baseline("2.1.88", 20)
	for d := 1; d >= 0; d-- {
		h.day(d, "2.1.89", 150, 4, 1700, 300, 0.5, 0.01) // the new release thinks 70% more
		h.day(d, "2.1.88", 40, 2, 1000, 300, 0.5, 0.01)  // the old one, still running elsewhere, does not
	}
	r := h.run()
	var got *Alert
	for i := range r.Alerts {
		if r.Alerts[i].Kind == KindVersion && r.Alerts[i].Metric == MetricThinkTokens {
			got = &r.Alerts[i]
		}
	}
	if got == nil {
		t.Fatalf("no version alert on thinking tokens: %+v", r.Alerts)
	}
	if got.Cause != CauseClient || got.Version != "2.1.89" || got.Previous != "2.1.88" || got.N < MinRequests {
		t.Fatalf("alert = %+v", got)
	}
	for _, want := range []string{"Claude Code 2.1.89: thinking per request (claude-opus-5-5, high effort) up", "tokens vs 5", "300 requests in 8 sessions, measured)."} {
		if !strings.Contains(got.Sentence, want) {
			t.Errorf("sentence %q lacks %q", got.Sentence, want)
		}
	}
	for _, want := range []string{"pin Claude Code 2.1.88", "bash -s 2.1.88", "DISABLE_AUTOUPDATER", SetupDocs} {
		if !strings.Contains(got.Advice, want) {
			t.Errorf("advice %q lacks %q", got.Advice, want)
		}
	}
	// The other metrics did not move, and the day alert for the same version is folded into it.
	for _, a := range r.Alerts {
		if a.Metric != MetricThinkTokens {
			t.Errorf("unexpected alert %s", a.Sentence)
		}
		if a.Kind == KindDay && a.Cause == CauseClient && a.Version == "2.1.89" {
			t.Errorf("day alert repeats the version alert: %s", a.Sentence)
		}
	}
}

func TestNoiseDoesNotAlert(t *testing.T) {
	h := newHist()
	// A noisy baseline: daily means swing between 600 and 1,400.
	for d := 21; d >= 2; d-- {
		mean := 600 + 800*h.rng.Float64()
		h.day(d, "2.1.88", 60, 3, mean, 500, 0.3+0.4*h.rng.Float64(), 0.02*h.rng.Float64())
	}
	for d := 1; d >= 0; d-- {
		h.day(d, "2.1.89", 150, 4, 600+800*h.rng.Float64(), 500, 0.3+0.4*h.rng.Float64(), 0.02*h.rng.Float64())
	}
	r := h.run()
	if len(r.Alerts) != 0 {
		t.Fatalf("noise alerted: %+v", r.Alerts)
	}
	if c := find(r, KindVersion, "2.1.89", MetricThinkTokens); c == nil || c.Status != StatusNormal {
		t.Fatalf("version check = %+v", c)
	}
}

func TestTooLittleData(t *testing.T) {
	h := newHist()
	h.baseline("2.1.88", 20)
	h.day(0, "2.1.89", 50, 2, 3000, 100, 1, 0.2) // a big jump, but 50 requests in 2 sessions
	h.day(0, "2.1.88", 40, 2, 1000, 300, 0.5, 0.01)
	r := h.run()
	if c := find(r, KindVersion, "2.1.89", MetricThinkTokens); c == nil || c.Status != StatusTooLittleData {
		t.Fatalf("version check = %+v", c)
	}
	for _, a := range r.Alerts {
		if a.Kind == KindVersion {
			t.Errorf("alerted on too little data: %s", a.Sentence)
		}
	}
	// Today has 90 requests: under the minimum too.
	if c := find(r, KindDay, "2026-11-26", MetricThinkTokens); c == nil || c.Status != StatusTooLittleData {
		t.Fatalf("day check = %+v", c)
	}
}

func TestNoBaseline(t *testing.T) {
	h := newHist()
	h.baseline("2.1.88", 3) // three days: under MinBaselineBlocks
	h.day(0, "2.1.89", 250, 4, 3000, 100, 1, 0.01)
	r := h.run()
	if c := find(r, KindVersion, "2.1.89", MetricThinkTokens); c == nil || c.Status != StatusNoBaseline {
		t.Fatalf("version check = %+v", c)
	}
	if len(r.Alerts) != 0 {
		t.Fatalf("alerted without a baseline: %+v", r.Alerts)
	}
}

func TestServerSpikeNeedsTwoVersions(t *testing.T) {
	h := newHist()
	h.baseline("2.1.88", 10)
	h.baseline("2.1.89", 10) // both versions in use all along
	h.day(1, "2.1.88", 60, 3, 1000, 300, 0.5, 0.01)
	h.day(1, "2.1.89", 60, 3, 1000, 300, 0.5, 0.01)
	h.day(0, "2.1.88", 120, 3, 1000, 300, 0.95, 0.01) // today both think far more often
	h.day(0, "2.1.89", 120, 3, 1000, 300, 0.95, 0.01)
	r := h.run()
	var day *Alert
	for i := range r.Alerts {
		if r.Alerts[i].Kind == KindDay && r.Alerts[i].Metric == MetricThinkShare {
			day = &r.Alerts[i]
		}
	}
	if day == nil || day.Cause != CauseServer || day.Key != "2026-11-26" {
		t.Fatalf("day alert = %+v (all %+v)", day, r.Alerts)
	}
	if !strings.Contains(day.Advice, "drop effort one level") || strings.Contains(day.Advice, "pin") {
		t.Errorf("advice = %q", day.Advice)
	}
	if !strings.HasPrefix(day.Sentence, "On 2026-11-26: requests with thinking (claude-opus-5-5, high effort) up") {
		t.Errorf("sentence = %q", day.Sentence)
	}
}

func TestOneActiveVersionIsUnclear(t *testing.T) {
	h := newHist()
	h.baseline("2.1.88", 20)
	h.day(1, "2.1.88", 60, 3, 1000, 300, 0.5, 0.01)
	h.day(0, "2.1.88", 240, 4, 1000, 300, 0.95, 0.01)
	r := h.run()
	if len(r.Alerts) == 0 {
		t.Fatal("no alert")
	}
	for _, a := range r.Alerts { // thinking more often also raises thinking per request
		if a.Kind != KindDay || a.Cause != CauseUnclear || !strings.HasPrefix(a.Advice, "Cause unclear: only one Claude Code version was active") {
			t.Fatalf("alert = %+v", a)
		}
	}
}

func TestLimitMetricIsEstimatedFromAPIPrices(t *testing.T) {
	h := newHist()
	h.baseline("2.1.88", 20)
	for d := 1; d >= 0; d-- {
		h.day(d, "2.1.89", 150, 4, 1000, 300, 0.5, 0.01)
		h.day(d, "2.1.88", 40, 2, 1000, 300, 0.5, 0.01)
	}
	add := func(d int, v string, n int, delta float64) {
		start := time.Date(now.Year(), now.Month(), now.Day()-d, 2, 0, 0, 0, time.UTC).UnixMilli()
		for i := 0; i < n; i++ {
			h.ivs = append(h.ivs, Interval{Start: start + int64(i)*600_000, End: start + int64(i+1)*600_000, Delta: delta,
				OETokens: 100_000 * (0.9 + 0.2*h.rng.Float64()), Version: v, Sessions: []string{fmt.Sprintf("%s-%d", v, i%3)}})
		}
	}
	for d := 21; d >= 2; d-- {
		add(d, "2.1.88", 3, 3)
	}
	add(1, "2.1.89", 6, 6) // the meter moves twice as fast for the same tokens
	add(0, "2.1.89", 6, 6)
	r := h.run()
	c := find(r, KindVersion, "2.1.89", MetricLimitPerKOE)
	if c == nil || c.Status != StatusAlert || c.Label != LabelEstimated || c.Basis != BasisAPIPrices {
		t.Fatalf("check = %+v", c)
	}
	for _, a := range r.Alerts {
		if a.Metric == MetricLimitPerKOE && !strings.Contains(a.Sentence, "12 meter intervals") || a.Metric == MetricLimitPerKOE && !strings.Contains(a.Sentence, "estimated from API prices") {
			t.Errorf("sentence = %q", a.Sentence)
		}
	}
}

func TestDeterministicInAnyOrder(t *testing.T) {
	h := newHist()
	h.baseline("2.1.88", 20)
	for d := 1; d >= 0; d-- {
		h.day(d, "2.1.89", 150, 4, 1700, 300, 0.5, 0.01)
		h.day(d, "2.1.88", 40, 2, 1000, 300, 0.5, 0.01)
	}
	a := fmt.Sprintf("%+v", h.run())
	for i, j := 0, len(h.reqs)-1; i < j; i, j = i+1, j-1 {
		h.reqs[i], h.reqs[j] = h.reqs[j], h.reqs[i]
	}
	if b := fmt.Sprintf("%+v", h.run()); a != b {
		t.Fatal("result depends on input order")
	}
}

// Moving to a model or effort that thinks more is the user's choice, not a regression: thinking is compared
// within the most common model and effort.
func TestModelOrEffortSwitchDoesNotAlert(t *testing.T) {
	h := newHist()
	for d := 21; d >= 2; d-- {
		h.dayOn(d, "2.1.88", "claude-sonnet-5-5", "medium", 60, 3, 400, 100, 0.5, 0.01)
		h.dayOn(d, "2.1.88", "claude-opus-5-5", "high", 30, 2, 1500, 300, 0.9, 0.01)
	}
	for d := 1; d >= 0; d-- {
		h.dayOn(d, "2.1.89", "claude-opus-5-5", "high", 150, 4, 1500, 300, 0.9, 0.01)
		h.dayOn(d, "2.1.88", "claude-sonnet-5-5", "medium", 40, 2, 400, 100, 0.5, 0.01)
	}
	r := h.run()
	for _, a := range r.Alerts {
		if a.Metric == MetricThinkTokens || a.Metric == MetricThinkShare {
			t.Errorf("alerted on a model switch: %s", a.Sentence)
		}
	}
	c := find(r, KindVersion, "2.1.89", MetricThinkTokens)
	if c == nil || c.Status != StatusNormal || c.Stratum != "claude-opus-5-5, high effort" {
		t.Fatalf("check = %+v", c)
	}
}
