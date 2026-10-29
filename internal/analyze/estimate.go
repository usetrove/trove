package analyze

import (
	"sort"
	"sync"

	"github.com/usetrove/trove/internal/pricing"
)

// defaultBytesPerToken is the starting ratio before a model family has been
// observed. It is only a prior: every estimate is rescaled to the provider's
// reported total, and the ratio then learns from what the provider reports.
const defaultBytesPerToken = 3.5

// Calibrator learns bytes-per-token per model family from provider-reported
// totals. It is safe for concurrent use.
type Calibrator struct {
	mu    sync.Mutex
	ratio map[string]float64
}

// NewCalibrator returns a calibrator with no observations.
func NewCalibrator() *Calibrator { return &Calibrator{ratio: map[string]float64{}} }

// Ratio returns the current bytes-per-token estimate for fam.
func (c *Calibrator) Ratio(fam string) float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r, ok := c.ratio[fam]; ok {
		return r
	}
	return defaultBytesPerToken
}

// Observe folds in one request: bytes of segment content against the tokens
// the provider reported for the whole prompt. An exponential average keeps one
// odd request from swinging the ratio.
func (c *Calibrator) Observe(fam string, bytes int, tokens int64) {
	if bytes <= 0 || tokens <= 0 {
		return
	}
	r := float64(bytes) / float64(tokens)
	if r < 1 || r > 12 { // implausible: images or other content not in the segments
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.ratio[fam]; ok {
		r = 0.7*old + 0.3*r
	}
	c.ratio[fam] = r
}

// Estimate is the per-request token breakdown. Every number in it is an
// estimate; when Calibrated is true the parts sum exactly to the total the
// provider reported.
type Estimate struct {
	Label         string           `json:"label"` // always "estimated"
	Calibrated    bool             `json:"calibrated"`
	TotalTokens   int64            `json:"total_tokens"`
	BytesPerToken float64          `json:"bytes_per_token"`
	Categories    map[string]int64 `json:"categories"`

	seg []int64 // tokens per segment, aligned with Parsed.Segments
}

// estimate apportions total tokens over the segments by byte share (largest
// remainder, so integers sum exactly). With total == 0 (usage not reported) it
// falls back to the prior ratio and says so through Calibrated=false.
func estimate(p *Parsed, total int64, ratio float64) *Estimate {
	e := &Estimate{Label: "estimated", BytesPerToken: ratio, Categories: map[string]int64{}, seg: make([]int64, len(p.Segments))}
	var bytes int
	for _, s := range p.Segments {
		bytes += s.Bytes
	}
	if bytes == 0 {
		return e
	}
	if total > 0 {
		e.Calibrated, e.TotalTokens = true, total
		e.BytesPerToken = float64(bytes) / float64(total)
		apportion(e.seg, p.Segments, total, bytes)
	} else {
		for i, s := range p.Segments {
			e.seg[i] = int64(float64(s.Bytes)/ratio + 0.5)
			e.TotalTokens += e.seg[i]
		}
	}
	for i, s := range p.Segments {
		if e.seg[i] > 0 {
			e.Categories[categoryKey(s)] += e.seg[i]
		}
	}
	return e
}

func categoryKey(s Segment) string {
	if s.Category != CatToolResults {
		return s.Category
	}
	if s.Tool == "" {
		return CatToolResults + ":unknown"
	}
	return CatToolResults + ":" + s.Tool
}

func apportion(out []int64, segs []Segment, total int64, bytes int) {
	type rem struct {
		i int
		f float64
	}
	rems := make([]rem, 0, len(segs))
	var used int64
	for i, s := range segs {
		exact := float64(total) * float64(s.Bytes) / float64(bytes)
		out[i] = int64(exact)
		used += out[i]
		rems = append(rems, rem{i, exact - float64(out[i])})
	}
	sort.SliceStable(rems, func(a, b int) bool { return rems[a].f > rems[b].f })
	for k := 0; used < total && k < len(rems); k++ {
		out[rems[k].i]++
		used++
	}
}

// cumulative returns offsets so that segment i occupies tokens [off[i], off[i+1]).
func (e *Estimate) cumulative() []int64 {
	off := make([]int64, len(e.seg)+1)
	for i, t := range e.seg {
		off[i+1] = off[i] + t
	}
	return off
}

// span is a stretch of the prompt, in tokens from its start.
type span struct{ start, tokens int64 }

// billing describes how the provider billed the prompt: it is laid out as
// cache reads, then cache writes, then uncached input.
type billing struct {
	req  pricing.Request
	ok   bool // dollars can be computed (priced model, write split known)
	read int64
	wr   int64
}

// removalFor maps spans of the prompt onto the billing categories they were
// charged under, so a token removed from the cached part saves the cache-read
// price and one removed from the uncached part saves the input price.
func (b billing) removalFor(spans []span) map[string]int64 {
	rem := map[string]int64{}
	ttls := make([]string, 0, len(b.req.Usage.CacheWrite))
	for ttl := range b.req.Usage.CacheWrite {
		ttls = append(ttls, ttl)
	}
	sort.Strings(ttls)
	for _, sp := range spans {
		lo, hi := sp.start, sp.start+sp.tokens
		inRead := overlap(lo, hi, 0, b.read)
		inWrite := overlap(lo, hi, b.read, b.read+b.wr)
		rem[pricing.CatCacheRead] += inRead
		for _, ttl := range ttls { // writes are removed 5m-first; the split inside a write span is not known
			if inWrite == 0 {
				break
			}
			take := min(inWrite, b.req.Usage.CacheWrite[ttl])
			rem[pricing.CatCacheWrite(ttl)] += take
			inWrite -= take
		}
		rem[pricing.CatInput] += (hi - lo) - overlap(lo, hi, 0, b.read+b.wr)
	}
	return rem
}

func overlap(a0, a1, b0, b1 int64) int64 {
	lo, hi := max(a0, b0), min(a1, b1)
	if hi > lo {
		return hi - lo
	}
	return 0
}
