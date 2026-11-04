package measure

import (
	"math"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
)

func TestUpstreamStatsCapacityAndGPUHour(t *testing.T) {
	db := openDB(t)
	cols := "upstream, latency_ms, ttft_ms, input_uncached_tokens, cache_read_tokens, output_tokens, cost_usd"
	// Three requests to "vllm". The first two overlap in time (busy time counts the
	// overlap once); the third comes 100 s later.
	addRequest(t, db, "s", t0, cols, "vllm", 4000, 200, 1000, 0, 400, 0.002)
	addRequest(t, db, "s", t0.Add(2*time.Second), cols, "vllm", 4000, 300, 1000, 0, 400, 0.002)
	addRequest(t, db, "s", t0.Add(100*time.Second), cols, "vllm", 2000, nil, 500, 500, 200, nil)
	// A built-in provider row and an error must not appear.
	addRequest(t, db, "s", t0, "upstream, latency_ms", "", 10)
	if _, err := db.Exec(`INSERT INTO requests(ts, session_key, status_code, upstream, latency_ms) VALUES(?, 's', 500, 'vllm', 99999)`, t0.UnixMilli()); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Upstreams.Compat = []config.CompatUpstream{{Name: "vllm", BaseURL: "http://10.0.0.5:8000",
		InputPerMTok: "1", OutputPerMTok: "2", GPUHourlyUSD: "3.60"}}
	rows, err := UpstreamStats(t.Context(), db, Window{From: t0.Add(-time.Hour), To: t0.Add(time.Hour)}, cfg)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows %v err %v", rows, err)
	}
	r := rows[0]
	if r.Requests != 3 || r.InputTokens != 3000 || r.OutputTokens != 1000 {
		t.Errorf("counts %+v", r)
	}
	if r.LatencyP50MS.Value != 4000 || r.LatencyP95MS.Value != 4000 || r.LatencyP50MS.Label != Measured {
		t.Errorf("latency %+v %+v", r.LatencyP50MS, r.LatencyP95MS)
	}
	if r.TTFTP50MS == nil || r.TTFTP50MS.Value != 200 || r.TTFTP50MS.N != 2 || r.TTFTP95MS.Value != 300 {
		t.Errorf("ttft %+v", r.TTFTP50MS)
	}
	// 1000 output tokens over (4+4+2) = 10 s of response time.
	if r.OutputTokensPerSec.Value != 100 || r.OutputTokensPerSec.Label != Measured {
		t.Errorf("throughput %+v", r.OutputTokensPerSec)
	}
	if r.CostUSD == nil || r.CostUSD.Label != Estimated || r.CostUSD.Value != 0.004 || r.CostUSD.Unpriced != 1 {
		t.Errorf("cost %+v", r.CostUSD)
	}
	g := r.GPU
	if g == nil || g.Label != Estimated {
		t.Fatalf("gpu %+v", g)
	}
	// Busy: [0,4s] and [2s,6s] merge to 6 s, plus [100s,102s] = 8 s. Span: 0 to 102 s.
	if g.BusySeconds != 8 || g.SpanSeconds != 102 {
		t.Errorf("busy %v span %v", g.BusySeconds, g.SpanSeconds)
	}
	// 4,000 tokens: input 3,000 + output 1,000. $3.60/h = $0.001/s.
	// busy: 0.008 / 4000 tokens = $2.00 per million; span: 0.102 / 4000 = $25.50 per million.
	if math.Abs(g.USDPerMTokBusy-2) > 1e-9 || math.Abs(g.USDPerMTokSpan-25.5) > 1e-9 {
		t.Errorf("per Mtok busy %v span %v", g.USDPerMTokBusy, g.USDPerMTokSpan)
	}
	if math.Abs(g.Utilization-8.0/102) > 1e-9 {
		t.Errorf("utilization %v", g.Utilization)
	}
}

func TestUpstreamStatsWithoutPricingConfigHasNoCost(t *testing.T) {
	db := openDB(t)
	addRequest(t, db, "s", t0, "upstream, latency_ms, output_tokens", "deepseek", 1000, 50)
	cfg := config.Default()
	rows, err := UpstreamStats(t.Context(), db, Window{From: t0.Add(-time.Hour), To: t0.Add(time.Hour)}, cfg)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows %v err %v", rows, err)
	}
	if rows[0].CostUSD != nil || rows[0].GPU != nil || rows[0].TTFTP50MS != nil {
		t.Errorf("figures appeared without configuration or data: %+v", rows[0])
	}
}

func TestPercentile(t *testing.T) {
	for _, tc := range []struct {
		vs   []float64
		p    float64
		want float64
	}{{nil, 50, 0}, {[]float64{5}, 95, 5}, {[]float64{1, 2, 3, 4}, 50, 2}, {[]float64{4, 1, 3, 2}, 95, 4}} {
		if got := percentile(tc.vs, tc.p); got != tc.want {
			t.Errorf("percentile(%v, %v) = %v, want %v", tc.vs, tc.p, got, tc.want)
		}
	}
}
