package analyze

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/usetrove/trove/internal/pricing"
)

// Options configures an Analyzer. Zero values pick the defaults below.
type Options struct {
	Pricing *pricing.Table // nil: findings carry tokens but no dollars
	Logger  *slog.Logger   // must already redact; nil discards

	OversizedToolTokens   int64   // a single tool result above this is oversized (default 10000)
	ContextFileMinTokens  int64   // context_file_weight needs at least this many tokens (default 500)
	UnusedToolMinRequests int     // requests observed before a tool counts as unused (default 5)
	DriftMinRequests      int     // requests before long_session_drift can fire (default 10)
	DriftContextTokens    int64   // prompt size at which drift is considered (default 100000)
	SmallInputTokens      int64   // expensive_model_small_task: input at or below this is small (default 4000)
	SmallOutputTokens     int64   // and output at or below this (default 500)
	FrontierInputUSDPerM  float64 // models at or above this input price are "frontier" (default 5)

	MaxSessions int           // sessions kept in memory (default 64)
	IdleTimeout time.Duration // sessions idle longer than this are forgotten (default 2h)
	MaxBody     int           // request bodies larger than this are not analyzed (default 16 MiB)
}

func (o *Options) defaults() {
	setI := func(p *int64, v int64) {
		if *p <= 0 {
			*p = v
		}
	}
	setI(&o.OversizedToolTokens, 10000)
	setI(&o.ContextFileMinTokens, 500)
	setI(&o.DriftContextTokens, 100000)
	setI(&o.SmallInputTokens, 4000)
	setI(&o.SmallOutputTokens, 500)
	if o.UnusedToolMinRequests <= 0 {
		o.UnusedToolMinRequests = 5
	}
	if o.DriftMinRequests <= 0 {
		o.DriftMinRequests = 10
	}
	if o.FrontierInputUSDPerM <= 0 {
		o.FrontierInputUSDPerM = 5
	}
	if o.MaxSessions <= 0 {
		o.MaxSessions = 64
	}
	if o.IdleTimeout <= 0 {
		o.IdleTimeout = 2 * time.Hour
	}
	if o.MaxBody <= 0 {
		o.MaxBody = 16 << 20
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
}

// Input is one finished request/response pair.
type Input struct {
	Provider   string
	Path       string // route without the provider prefix
	Body       []byte // the request body, as forwarded
	SessionKey string // already hashed; "" means no session
	Start      time.Time

	Model       string // as the provider reported it; empty falls back to the request's
	ServiceTier string // pricing tier name ("" for standard)
	Batch       bool

	Reported        bool // the provider sent usage
	Usage           pricing.Usage
	CacheWriteTotal int64 // all cache writes, even when their TTL split is unknown
}

// Output is what to store on the request row. All three are JSON.
type Output struct {
	Estimates []byte
	Findings  []byte
	Shadow    []byte
}

type sessionState struct {
	last       time.Time
	snaps      []*snapshot
	requests   int
	toolsInAll map[string]struct{}
	called     map[string]struct{}
	totals     []int64
	struggle   []int
	lastMetric int
}

// Analyzer holds the bounded in-memory session state. It is safe for concurrent use.
type Analyzer struct {
	opts Options
	cal  *Calibrator

	mu       sync.Mutex
	sessions map[string]*sessionState
}

// New returns an Analyzer.
func New(opts Options) *Analyzer {
	opts.defaults()
	return &Analyzer{opts: opts, cal: NewCalibrator(), sessions: map[string]*sessionState{}}
}

// Analyze inspects one request. It returns nil if the request is not one it
// understands or if analysis failed (the failure is logged, redacted, and
// skipped): analysis never affects the request and never returns an error.
func (a *Analyzer) Analyze(in Input) (out *Output) {
	defer func() {
		if r := recover(); r != nil {
			a.opts.Logger.Error("request analysis failed and was skipped; the request was not affected", "panic", fmt.Sprint(r))
			out = nil
		}
	}()
	if len(in.Body) == 0 || len(in.Body) > a.opts.MaxBody {
		return nil
	}
	p, ok := Parse(in.Provider, in.Path, in.Body)
	if !ok {
		return nil
	}
	fam := family(p.Provider, p.Model)
	var total int64
	if in.Reported {
		total = in.Usage.UncachedInput + in.CacheWriteTotal + in.Usage.CacheRead
	}
	e := estimate(p, total, a.cal.Ratio(fam))
	if total > 0 {
		var bytes int
		for _, s := range p.Segments {
			bytes += s.Bytes
		}
		a.cal.Observe(fam, bytes, total)
	}

	b := a.billingFor(in, p)
	findings, claimed := a.statelessFindings(p, e, b, in)

	if in.SessionKey != "" {
		findings = append(findings, a.stateful(in, p, e, b, claimed, total)...)
	}

	res := &Output{}
	var err error
	if res.Estimates, err = json.Marshal(e); err != nil {
		return nil
	}
	if len(findings) > 0 {
		sort.SliceStable(findings, func(i, j int) bool { return findings[i].Tokens > findings[j].Tokens })
		if res.Findings, err = json.Marshal(findings); err != nil {
			return nil
		}
	}
	if sh := shadowFor(findings); len(sh.Optimizers) > 0 {
		if res.Shadow, err = json.Marshal(sh); err != nil {
			return nil
		}
	}
	return res
}

func (a *Analyzer) billingFor(in Input, p *Parsed) billing {
	model := in.Model
	if model == "" {
		model = p.Model
	}
	req := pricing.Request{Provider: p.Provider, Model: model, Time: in.Start, Batch: in.Batch, ServiceTier: in.ServiceTier, Usage: in.Usage}
	ok := in.Reported && (in.CacheWriteTotal == 0 || in.Usage.CacheWrite != nil)
	return billing{req: req, ok: ok, read: in.Usage.CacheRead, wr: in.CacheWriteTotal}
}

// stateful runs everything that compares this request with the session's
// earlier ones, then records this request in the session.
func (a *Analyzer) stateful(in Input, p *Parsed, e *Estimate, b billing, claimed map[int]bool, total int64) []Finding {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.evict(in.Start)

	st := a.sessions[in.SessionKey]
	if st == nil {
		st = &sessionState{toolsInAll: nil, called: map[string]struct{}{}}
		a.sessions[in.SessionKey] = st
	}
	st.last = in.Start
	st.requests++

	// Tools defined in every request so far, and tools ever called.
	cur := map[string]struct{}{}
	for _, s := range p.Segments {
		switch {
		case s.Kind == KindToolDef && s.Tool != "":
			cur[s.Tool] = struct{}{}
		case s.Kind == KindToolCall && s.Tool != "":
			st.called[s.Tool] = struct{}{}
		}
	}
	if st.toolsInAll == nil {
		st.toolsInAll = cur
	} else {
		for t := range st.toolsInAll {
			if _, ok := cur[t]; !ok {
				delete(st.toolsInAll, t)
			}
		}
	}
	if total > 0 {
		st.totals = append(st.totals, total)
		if len(st.totals) > 8 {
			st.totals = st.totals[1:]
		}
	}
	metric := struggleMetric(p)
	st.struggle = append(st.struggle, max(0, metric-st.lastMetric))
	if len(st.struggle) > 8 {
		st.struggle = st.struggle[1:]
	}
	st.lastMetric = metric

	var fs []Finding

	// Cache analysis against the best-matching earlier request. Concurrent
	// sub-agents share a session, so the most similar prompt is the right one.
	var best *snapshot
	bestLCP := -1
	for _, sn := range st.snaps {
		if l := commonPrefix(sn, p); l >= bestLCP {
			best, bestLCP = sn, l
		}
	}
	if ca := a.analyzeCache(best, p, e, in.Start, in.Reported && total > 0, in.Usage.CacheRead); ca != nil && ca.Miss != nil {
		fs = append(fs, a.missFinding(b, ca, e))
	}
	if total > 0 {
		st.snaps = append(st.snaps, takeSnapshot(p, e, in.Start, total, in.Usage.CacheRead+in.CacheWriteTotal))
		if len(st.snaps) > snapshotsPerSess {
			st.snaps = st.snaps[1:]
		}
	}

	return append(fs, a.sessionFindings(st, p, e, b, claimed, total)...)
}

// missFinding prices a cache miss as the extra cost of billing the missed
// tokens at the write or input price instead of the cache-read price.
func (a *Analyzer) missFinding(b billing, ca *CacheAnalysis, _ *Estimate) Finding {
	m := ca.Miss
	detail := map[string]any{"reason": m.Reason, "expected_read_tokens": ca.ExpectedRead, "reported_read_tokens": ca.ReportedRead}
	for k, v := range m.Detail {
		detail[k] = v
	}
	f := Finding{Detector: DetCacheMiss, Tokens: m.Tokens, Confidence: "medium", Label: "estimated", Detail: detail}
	if m.Reason == ReasonUnknown || m.Reason == ReasonNoMarkers {
		f.Confidence = "low"
	}
	if tbl := a.opts.Pricing; tbl != nil && b.ok {
		hyp := b.req
		hyp.Usage.CacheWrite = copyMap(b.req.Usage.CacheWrite)
		left := m.Tokens
		ttls := make([]string, 0, len(hyp.Usage.CacheWrite))
		for t := range hyp.Usage.CacheWrite {
			ttls = append(ttls, t)
		}
		sort.Strings(ttls)
		for _, t := range ttls { // missed tokens were re-written first, then billed as plain input
			take := min(left, hyp.Usage.CacheWrite[t])
			hyp.Usage.CacheWrite[t] -= take
			left -= take
		}
		take := min(left, hyp.Usage.UncachedInput)
		hyp.Usage.UncachedInput -= take
		hyp.Usage.CacheRead += (m.Tokens - left) + take
		before, after := tbl.Cost(b.req), tbl.Cost(hyp)
		if before.Status == pricing.Priced && after.Status == pricing.Priced {
			v := before.Total.Sub(after.Total).Float64()
			f.USD = &v
		}
	}
	return f
}

func copyMap(m map[string]int64) map[string]int64 {
	if m == nil {
		return nil
	}
	out := make(map[string]int64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// evict forgets idle sessions and, if still over the cap, the least recently
// active ones. The caller holds a.mu.
func (a *Analyzer) evict(now time.Time) {
	for k, s := range a.sessions {
		if now.Sub(s.last) > a.opts.IdleTimeout {
			delete(a.sessions, k)
		}
	}
	for len(a.sessions) >= a.opts.MaxSessions {
		var oldest string
		var at time.Time
		for k, s := range a.sessions {
			if oldest == "" || s.last.Before(at) {
				oldest, at = k, s.last
			}
		}
		delete(a.sessions, oldest)
	}
}

// Sessions reports how many sessions are held in memory (for tests and doctor).
func (a *Analyzer) Sessions() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.sessions)
}
