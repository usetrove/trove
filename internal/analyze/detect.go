package analyze

import (
	"sort"
	"strconv"

	"github.com/usetrove/trove/internal/pricing"
)

// Detector names.
const (
	DetDuplicate     = "duplicate_tool_result"
	DetNoisy         = "noisy_output"
	DetOversized     = "oversized_tool_output"
	DetUnusedTools   = "unused_tool_schemas"
	DetContextFiles  = "context_file_weight"
	DetExpensive     = "expensive_model_small_task"
	DetDrift         = "long_session_drift"
	DetCacheMiss     = "cache_miss"
	minDuplicateByte = 200
)

// Finding is one detected piece of waste or one cache miss. Tokens are
// estimates; USD is the estimated saving (or, for cache_miss, the extra cost)
// and is nil when the request could not be priced exactly. Findings overlap
// (a noisy output can also be oversized), so they must not be summed.
type Finding struct {
	Detector   string         `json:"detector"`
	Tokens     int64          `json:"tokens"`
	USD        *float64       `json:"usd"`
	Confidence string         `json:"confidence"` // high, medium or low
	Label      string         `json:"label"`      // always "estimated"
	Detail     map[string]any `json:"detail,omitempty"`

	spans []span
}

func (a *Analyzer) finding(b billing, det, conf string, tokens int64, spans []span, detail map[string]any) Finding {
	f := Finding{Detector: det, Tokens: tokens, Confidence: conf, Label: "estimated", Detail: detail, spans: spans}
	f.USD = a.price(b, spans)
	return f
}

// price returns the estimated dollars saved by removing spans from the prompt.
func (a *Analyzer) price(b billing, spans []span) *float64 {
	if !b.ok || a.opts.Pricing == nil || len(spans) == 0 {
		return nil
	}
	cf, err := a.opts.Pricing.CostWithoutAll(b.req, b.removalFor(spans))
	if err != nil || !cf.Valid {
		return nil
	}
	v := cf.Savings.Float64()
	return &v
}

// statelessFindings are the detectors that need only this request.
func (a *Analyzer) statelessFindings(p *Parsed, e *Estimate, b billing, in Input) (fs []Finding, claimed map[int]bool) {
	off := e.cumulative()
	claimed = map[int]bool{}
	seg := func(i int) span { return span{off[i], e.seg[i]} }

	// duplicate_tool_result: byte-identical to an earlier result in this conversation.
	first := map[hash]int{}
	var dup []span
	var dupTokens int64
	dupTools := map[string]int{}
	for i, s := range p.Segments {
		if s.Kind != KindToolResult || s.Bytes < minDuplicateByte {
			continue
		}
		if _, seen := first[s.Content]; !seen {
			first[s.Content] = i
			continue
		}
		dup = append(dup, seg(i))
		dupTokens += e.seg[i]
		dupTools[toolLabel(s.Tool)]++
		claimed[i] = true
	}
	if len(dup) > 0 {
		fs = append(fs, a.finding(b, DetDuplicate, "high", dupTokens, dup, map[string]any{"duplicates": len(dup), "tools": dupTools}))
	}

	// noisy_output and oversized_tool_output.
	var noise, over []span
	var noiseTokens, overTokens, largest int64
	var nb Noise
	noisyTools, overTools := map[string]int{}, map[string]int{}
	for i, s := range p.Segments {
		if s.Kind != KindToolResult || s.Bytes == 0 {
			continue
		}
		if n := measureNoise(s.text); n.Total > 0 {
			t := e.seg[i] * int64(n.Total) / int64(s.Bytes)
			if t > 0 {
				noise = append(noise, span{off[i], t})
				noiseTokens += t
				noisyTools[toolLabel(s.Tool)]++
				nb.ANSI += n.ANSI
				nb.Progr += n.Progr
				nb.Pass += n.Pass
				nb.Repea += n.Repea
			}
		}
		largest = max(largest, e.seg[i])
		if over1 := e.seg[i] - a.opts.OversizedToolTokens; over1 > 0 {
			over = append(over, span{off[i] + a.opts.OversizedToolTokens, over1})
			overTokens += over1
			overTools[toolLabel(s.Tool)]++
		}
	}
	if len(noise) > 0 {
		fs = append(fs, a.finding(b, DetNoisy, "medium", noiseTokens, noise, map[string]any{
			"results": len(noise), "tools": noisyTools,
			"ansi_bytes": nb.ANSI, "progress_bytes": nb.Progr, "passing_test_bytes": nb.Pass, "repeated_line_bytes": nb.Repea,
		}))
	}
	if len(over) > 0 {
		fs = append(fs, a.finding(b, DetOversized, "medium", overTokens, over, map[string]any{
			"results": len(over), "tools": overTools, "threshold_tokens": a.opts.OversizedToolTokens, "largest_tokens": largest,
			"note": "tokens counted are only those above the threshold",
		}))
	}

	// context_file_weight: context files are sent with every request.
	var ctx []span
	var ctxTokens int64
	for i, s := range p.Segments {
		if s.Category == CatContextFile {
			ctx = append(ctx, seg(i))
			ctxTokens += e.seg[i]
		}
	}
	if ctxTokens >= a.opts.ContextFileMinTokens && ctxTokens > 0 {
		fs = append(fs, a.finding(b, DetContextFiles, "medium", ctxTokens, ctx, map[string]any{
			"segments": len(ctx), "note": "detected by known markers; sent with every request",
		}))
	}

	if f, ok := a.expensiveSmall(p, e, b, in); ok {
		fs = append(fs, f)
	}
	return fs, claimed
}

func toolLabel(name string) string {
	if name == "" {
		return "unknown"
	}
	return name
}

// expensiveSmall suggests a cheaper model when a frontier model handled a turn
// with a small input and a small output. It is a suggestion with low
// confidence: Trove cannot know whether the cheaper model would have coped.
func (a *Analyzer) expensiveSmall(p *Parsed, e *Estimate, b billing, in Input) (Finding, bool) {
	tbl := a.opts.Pricing
	if tbl == nil || !b.ok || !e.Calibrated || e.TotalTokens > a.opts.SmallInputTokens || in.Usage.Output > a.opts.SmallOutputTokens || in.Usage.Output == 0 {
		return Finding{}, false
	}
	cur, _ := tbl.Lookup(p.Provider, b.req.Model, in.Start)
	if cur == nil {
		return Finding{}, false
	}
	curIn := decFloat(cur.Standard.Input)
	if curIn < a.opts.FrontierInputUSDPerM {
		return Finding{}, false
	}
	// The most capable model from the same provider that costs at most a third.
	var alt *pricing.Entry
	var altIn float64
	for _, en := range tbl.Entries() {
		if en.Provider != p.Provider || en.EffectiveFrom == nil || en.EffectiveFrom.After(in.Start) {
			continue
		}
		if v := decFloat(en.Standard.Input); v*3 <= curIn && v > altIn {
			alt, altIn = &en, v
		}
	}
	if alt == nil {
		return Finding{}, false
	}
	before := tbl.Cost(b.req)
	r2 := b.req
	r2.Model = alt.Model
	after := tbl.Cost(r2)
	if before.Status != pricing.Priced || after.Status != pricing.Priced {
		return Finding{}, false
	}
	usd := before.Total.Sub(after.Total).Float64()
	return Finding{
		Detector: DetExpensive, Tokens: e.TotalTokens + in.Usage.Output, USD: &usd, Confidence: "low", Label: "estimated",
		Detail: map[string]any{
			"input_tokens": e.TotalTokens, "output_tokens": in.Usage.Output, "alternative": alt.Model,
			"note": "suggestion only; assumes the cheaper model could do this task, which Trove cannot check",
		},
	}, true
}

func decFloat(d pricing.Dec) float64 {
	v, _ := strconv.ParseFloat(d.String(), 64)
	return v
}

// sessionFindings are the detectors that need the session's history.
func (a *Analyzer) sessionFindings(st *sessionState, p *Parsed, e *Estimate, b billing, claimed map[int]bool, total int64) []Finding {
	var fs []Finding
	off := e.cumulative()

	// unused_tool_schemas
	if st.requests >= a.opts.UnusedToolMinRequests {
		var spans []span
		var tokens int64
		var names []string
		for i, s := range p.Segments {
			if s.Kind != KindToolDef || s.Tool == "" {
				continue
			}
			_, inAll := st.toolsInAll[s.Tool]
			_, called := st.called[s.Tool]
			if inAll && !called {
				spans = append(spans, span{off[i], e.seg[i]})
				tokens += e.seg[i]
				names = append(names, s.Tool)
			}
		}
		if len(spans) > 0 {
			sort.Strings(names)
			fs = append(fs, a.finding(b, DetUnusedTools, "medium", tokens, spans, map[string]any{
				"tools": len(names), "tool_names": names, "requests_observed": st.requests,
			}))
		}
	}

	// long_session_drift
	if st.requests >= a.opts.DriftMinRequests && total >= a.opts.DriftContextTokens && rising(st.totals, 4) && strugglingMore(st.struggle) {
		var spans []span
		var tokens int64
		seen := map[hash]int{}
		for i, s := range p.Segments {
			switch {
			case s.Kind == KindToolResult && s.IsError && !claimed[i]:
				spans = append(spans, span{off[i], e.seg[i]})
				tokens += e.seg[i]
			case s.Kind == KindToolCall:
				seen[s.Content]++
				if seen[s.Content] > 1 {
					spans = append(spans, span{off[i], e.seg[i]})
					tokens += e.seg[i]
				}
			}
		}
		fs = append(fs, a.finding(b, DetDrift, "low", tokens, spans, map[string]any{
			"context_tokens": total, "requests_observed": st.requests, "recent_struggle_events": sumInts(st.struggle[len(st.struggle)-3:]),
			"note": "context is large and growing while errors and repeated tool calls increase; tokens counted are the failed and repeated exchanges",
		}))
	}
	return fs
}

func rising(v []int64, n int) bool {
	if len(v) < n {
		return false
	}
	v = v[len(v)-n:]
	for i := 1; i < len(v); i++ {
		if v[i] <= v[i-1] {
			return false
		}
	}
	return true
}

func sumInts(v []int) int {
	t := 0
	for _, x := range v {
		t += x
	}
	return t
}

// strugglingMore is true when the last three requests had at least three
// error or repeated-call events and no fewer than the three before them (the
// cumulative count keeps rising, so the session is not recovering).
func strugglingMore(s []int) bool {
	if len(s) < 6 {
		return false
	}
	recent, earlier := sumInts(s[len(s)-3:]), sumInts(s[len(s)-6:len(s)-3])
	return recent >= 3 && recent >= earlier
}

// struggleMetric counts the error results and repeated calls in a request's
// history. Its change between requests is the new struggle.
func struggleMetric(p *Parsed) int {
	n := 0
	seen := map[hash]int{}
	for _, s := range p.Segments {
		switch {
		case s.Kind == KindToolResult && s.IsError:
			n++
		case s.Kind == KindToolCall:
			seen[s.Content]++
			if seen[s.Content] > 1 {
				n++
			}
		}
	}
	return n
}
