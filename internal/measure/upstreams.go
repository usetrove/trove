package measure

import (
	"context"
	"math"
	"sort"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/store"
)

// UpstreamRow is capacity and cost for one OpenAI-compatible server. For
// self-hosted and open-weight servers, how fast and how loaded the server is
// matters more than the price per token, so latency and time to first byte
// are reported next to cost.
//
// Latency, time to first byte and throughput are measured at the gateway.
// "TTFT" is the time until the first response byte reached the gateway, not the
// first generated token, and is only recorded for streamed responses.
type UpstreamRow struct {
	Name         string `json:"name"`
	Requests     int    `json:"requests"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`

	LatencyP50MS Figure `json:"latency_p50_ms"`
	LatencyP95MS Figure `json:"latency_p95_ms"`
	// TTFT figures are absent when no streamed request was recorded.
	TTFTP50MS *Figure `json:"ttft_p50_ms,omitempty"`
	TTFTP95MS *Figure `json:"ttft_p95_ms,omitempty"`
	// OutputTokensPerSec is output tokens over total response time across requests
	// that reported output tokens (measured).
	OutputTokensPerSec Figure `json:"output_tokens_per_sec"`

	// CostUSD is priced with the user's own per-token rates (estimated). Absent
	// when no rates are configured for this server.
	CostUSD *Figure `json:"cost_usd,omitempty"`
	// GPU is set when gpu_hourly_usd is configured for this server.
	GPU *GPUEstimate `json:"gpu,omitempty"`
}

// GPUEstimate turns an hourly machine cost into a price per million tokens by
// dividing it by measured throughput. Both figures are estimates: the hourly cost
// is the user's number, and the result depends on which seconds are counted.
type GPUEstimate struct {
	HourlyUSD      string  `json:"hourly_usd"`
	Label          Label   `json:"label"` // always "estimated"
	BusySeconds    float64 `json:"busy_seconds"`
	SpanSeconds    float64 `json:"span_seconds"`
	Utilization    float64 `json:"utilization"`                 // busy / span, 0 to 1
	USDPerMTokBusy float64 `json:"usd_per_mtok_busy,omitempty"` // cost while serving requests
	USDPerMTokSpan float64 `json:"usd_per_mtok_span,omitempty"` // cost over the first-to-last-request span, idle time included
	Note           string  `json:"note"`
}

// GPUNote explains how to read the GPU-hour figures.
const GPUNote = "Estimated: your hourly cost divided by measured tokens. Busy time counts only the seconds a request was in flight; " +
	"the span figure also pays for idle time between the first and last request. Overlapping requests share the same seconds."

type upstreamAcc struct {
	lat, ttft        []float64
	in, out          int64
	outTok, outMS    float64 // requests that reported output: tokens, and the time they took
	cost             float64
	priced, unpriced int
	n                int
	intervals        [][2]int64
	first, last      int64
}

// UpstreamStats reports capacity and cost for each OpenAI-compatible server
// that handled traffic in w, sorted by name.
func UpstreamStats(ctx context.Context, db *store.DB, w Window, cfg config.Config) ([]UpstreamRow, error) {
	conf := map[string]config.CompatUpstream{}
	for _, c := range cfg.Upstreams.Compat {
		conf[c.Name] = c
	}
	rows, err := db.QueryContext(ctx, `SELECT upstream, ts, latency_ms, ttft_ms, input_uncached_tokens, cache_read_tokens,
		output_tokens, cost_usd FROM requests
		WHERE ts >= ? AND ts < ? AND upstream != '' AND status_code > 0 AND status_code < 400 ORDER BY upstream, ts`,
		w.From.UnixMilli(), w.To.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	by := map[string]*upstreamAcc{}
	for rows.Next() {
		var name string
		var ts, lat, inTok, cacheRead, outTok int64
		var ttft *int64
		var cost *float64
		if err := rows.Scan(&name, &ts, &lat, &ttft, &inTok, &cacheRead, &outTok, &cost); err != nil {
			return nil, err
		}
		a := by[name]
		if a == nil {
			a = &upstreamAcc{first: ts}
			by[name] = a
		}
		a.n++
		a.lat = append(a.lat, float64(lat))
		if ttft != nil {
			a.ttft = append(a.ttft, float64(*ttft))
		}
		a.in += inTok + cacheRead
		a.out += outTok
		if outTok > 0 && lat > 0 {
			a.outTok += float64(outTok)
			a.outMS += float64(lat)
		}
		if cost != nil {
			a.cost += *cost
			a.priced++
		} else {
			a.unpriced++
		}
		a.intervals = append(a.intervals, [2]int64{ts, ts + lat})
		if end := ts + lat; end > a.last {
			a.last = end
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	names := make([]string, 0, len(by))
	for n := range by {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]UpstreamRow, 0, len(names))
	for _, n := range names {
		a := by[n]
		r := UpstreamRow{Name: n, Requests: a.n, InputTokens: a.in, OutputTokens: a.out,
			LatencyP50MS: Figure{Value: percentile(a.lat, 50), Label: Measured, N: len(a.lat)},
			LatencyP95MS: Figure{Value: percentile(a.lat, 95), Label: Measured, N: len(a.lat)},
		}
		if len(a.ttft) > 0 {
			p50 := Figure{Value: percentile(a.ttft, 50), Label: Measured, N: len(a.ttft)}
			p95 := Figure{Value: percentile(a.ttft, 95), Label: Measured, N: len(a.ttft)}
			r.TTFTP50MS, r.TTFTP95MS = &p50, &p95
		}
		if a.outMS > 0 {
			r.OutputTokensPerSec = Figure{Value: math.Round(a.outTok/(a.outMS/1000)*100) / 100, Label: Measured, N: a.n}
		} else {
			r.OutputTokensPerSec = Figure{Label: Measured}
		}
		c, hasConf := conf[n]
		if hasConf && c.InputPerMTok != "" {
			f := Figure{Value: roundUSD(a.cost), Label: Estimated, N: a.priced, Unpriced: a.unpriced}
			r.CostUSD = &f
		}
		if hasConf && c.GPUHourlyUSD != "" {
			r.GPU = gpuEstimate(c.GPUHourlyUSD, a.in+a.out, a.intervals, a.first, a.last)
		}
		out = append(out, r)
	}
	return out, nil
}

// gpuEstimate computes the GPU-hour figures. Tokens are input plus output: a
// server spends GPU time on both. Busy time is the union of request intervals.
func gpuEstimate(hourly string, tokens int64, intervals [][2]int64, first, last int64) *GPUEstimate {
	h, err := pricing.ParseDec(hourly)
	if err != nil {
		return nil
	}
	sort.Slice(intervals, func(i, j int) bool { return intervals[i][0] < intervals[j][0] })
	var busyMS, curEnd int64
	curStart := int64(-1)
	for _, iv := range intervals {
		switch {
		case curStart < 0:
			curStart, curEnd = iv[0], iv[1]
		case iv[0] <= curEnd:
			curEnd = max(curEnd, iv[1])
		default:
			busyMS += curEnd - curStart
			curStart, curEnd = iv[0], iv[1]
		}
	}
	if curStart >= 0 {
		busyMS += curEnd - curStart
	}
	g := &GPUEstimate{HourlyUSD: hourly, Label: Estimated, Note: GPUNote,
		BusySeconds: float64(busyMS) / 1000, SpanSeconds: float64(last-first) / 1000}
	if g.SpanSeconds > 0 {
		g.Utilization = math.Min(1, g.BusySeconds/g.SpanSeconds)
	}
	if v, ok := pricing.GPUHourUSDPerMTok(h, tokens, g.BusySeconds); ok {
		g.USDPerMTokBusy = math.Round(v*1e4) / 1e4
	}
	if v, ok := pricing.GPUHourUSDPerMTok(h, tokens, g.SpanSeconds); ok {
		g.USDPerMTokSpan = math.Round(v*1e4) / 1e4
	}
	return g
}

// percentile is the nearest-rank percentile of vs (0 for no values).
func percentile(vs []float64, p float64) float64 {
	if len(vs) == 0 {
		return 0
	}
	s := append([]float64(nil), vs...)
	sort.Float64s(s)
	rank := int(math.Ceil(p / 100 * float64(len(s))))
	return s[min(max(rank, 1), len(s))-1]
}
