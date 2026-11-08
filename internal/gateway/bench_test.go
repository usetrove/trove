package gateway

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/store"
)

type discardRecorder struct{}

func (discardRecorder) Enqueue(store.Request) bool { return true }

// benchRig returns the upstream URL and the gateway URL for the same fake upstream.
func benchRig(tb testing.TB, h http.Handler) (direct, via string) {
	up := httptest.NewServer(h)
	tb.Cleanup(up.Close)
	cfg := config.Default()
	cfg.Upstreams.Anthropic = up.URL
	s, err := New(Options{Config: cfg, Recorder: discardRecorder{}})
	if err != nil {
		tb.Fatal(err)
	}
	gw := httptest.NewServer(s.Handler())
	tb.Cleanup(gw.Close)
	return up.URL, gw.URL + "/anthropic"
}

var benchBody = []byte(`{"model":"claude-opus-5-5","system":"s","messages":[{"role":"user","content":"hi"}]}`)

// roundTrip returns total latency and time to the first response byte.
func roundTrip(tb testing.TB, c *http.Client, url string) (total, ttfb time.Duration) {
	req, _ := http.NewRequest("POST", url+"/v1/messages", bytes.NewReader(benchBody))
	req.Header.Set("x-api-key", fakeKey)
	t0 := time.Now()
	resp, err := c.Do(req)
	if err != nil {
		tb.Fatal(err)
	}
	one := make([]byte, 1)
	_, _ = resp.Body.Read(one)
	ttfb = time.Since(t0)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return time.Since(t0), ttfb
}

func median(d []time.Duration) time.Duration {
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	return d[len(d)/2]
}

func sample(tb testing.TB, url string, n int) (totals, ttfbs []time.Duration) {
	c := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 4}}
	for i := 0; i < 20; i++ { // warm connections
		roundTrip(tb, c, url)
	}
	for i := 0; i < n; i++ {
		a, b := roundTrip(tb, c, url)
		totals, ttfbs = append(totals, a), append(ttfbs, b)
	}
	return
}

// TestOverheadUnderBudget measures added latency and time to first byte of a
// 40-event stream versus a direct connection. The brief's budget is under 5 ms
// at p50. Timing tests are skipped with -short.
func TestOverheadUnderBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	stream := strings.Repeat("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"x\"}}\n\n", 40)
	direct, via := benchRig(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range strings.SplitAfter(stream, "\n\n") {
			_, _ = io.WriteString(w, ev)
			w.(http.Flusher).Flush()
		}
	}))
	dTot, dFirst := sample(t, direct, 300)
	vTot, vFirst := sample(t, via, 300)
	addedTotal := median(vTot) - median(dTot)
	addedFirst := median(vFirst) - median(dFirst)
	t.Logf("p50 total: direct %v, via gateway %v (added %v)", median(dTot), median(vTot), addedTotal)
	t.Logf("p50 time to first byte: direct %v, via gateway %v (added %v)", median(dFirst), median(vFirst), addedFirst)
	if addedTotal > 5*time.Millisecond || addedFirst > 5*time.Millisecond {
		t.Errorf("added p50 latency exceeds the 5 ms budget: total %v, first byte %v", addedTotal, addedFirst)
	}
}

func BenchmarkViaGateway(b *testing.B) {
	_, via := benchRig(b, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, `{"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	c := &http.Client{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		roundTrip(b, c, via)
	}
}

func BenchmarkDirect(b *testing.B) {
	direct, _ := benchRig(b, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, `{"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	c := &http.Client{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		roundTrip(b, c, direct)
	}
}
