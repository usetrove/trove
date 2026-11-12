package stress

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/gateway"
	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/store"
)

// fakeKey is a key in the accepted console shape. It goes only to the fake upstream below, never to a network.
const fakeKey = "sk-ant-api03-FAKEKEYFORSTRESSTESTS0123456789abcdefghijk"

// fakeUpstream answers every request locally with a fixed, valid Messages response. It never dials out.
type fakeUpstream struct{}

func (fakeUpstream) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
		_ = req.Body.Close()
	}
	const body = `{"id":"msg_stress","type":"message","role":"assistant","model":"claude-sonnet-5-5",` +
		`"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn",` +
		`"usage":{"input_tokens":10,"output_tokens":2}}`
	return &http.Response{
		StatusCode: 200, Status: "200 OK", Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header:  http.Header{"Content-Type": []string{"application/json"}},
		Body:    io.NopCloser(strings.NewReader(body)),
		Request: req, ContentLength: int64(len(body)),
	}, nil
}

// countingRecorder accepts every row and counts them.
type countingRecorder struct{ n atomic.Int64 }

func (c *countingRecorder) Enqueue(store.Request) bool { c.n.Add(1); return true }

// newTestGateway starts the real gateway handler on a loopback listener, with the fake upstream.
func newTestGateway(rt http.RoundTripper, rec gateway.Recorder) (*httptest.Server, error) {
	gw, err := gateway.New(gateway.Options{Config: config.Default(), Recorder: rec, Transport: rt})
	if err != nil {
		return nil, err
	}
	return httptest.NewServer(gw.Handler()), nil
}

// messageRequest is the small request the gateway load sends.
func messageRequest(base string) (*http.Request, error) {
	body := []byte(`{"model":"claude-sonnet-5-5","max_tokens":64,"messages":[{"role":"user","content":"Reply with the word ok."}]}`)
	req, err := newRequest(http.MethodPost, base+"/anthropic/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", fakeKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

// percentiles returns p50 and p99 in milliseconds of a set of durations.
func percentiles(ds []time.Duration) (p50, p99 float64) {
	if len(ds) == 0 {
		return 0, 0
	}
	ms := make([]float64, len(ds))
	for i, d := range ds {
		ms[i] = float64(d) / float64(time.Millisecond)
	}
	sort.Float64s(ms)
	return quantile(ms, 0.5), quantile(ms, 0.99)
}

// heapMB forces a collection and reports the live heap, so growth is measured on retained memory only.
func heapMB() float64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return float64(m.HeapAlloc) / (1 << 20)
}

// settledGoroutines waits briefly for goroutines started by finished work to exit, then reports the count.
func settledGoroutines(target int) int {
	n := runtime.NumGoroutine()
	for i := 0; i < 40 && n > target; i++ {
		time.Sleep(50 * time.Millisecond)
		n = runtime.NumGoroutine()
	}
	return n
}

// load is layer 4. Latencies and memory are measured numbers, not savings, and each one says so.
func (e *env) load() LayerResult {
	lr := LayerResult{Name: LayerLoad}
	goBefore := runtime.NumGoroutine()
	heapBefore := heapMB()

	e.loadEngine(&lr)
	e.loadGateway(&lr)
	e.loadSQLite(&lr)

	// Leak check: after all the load has finished, goroutines must return to where they started.
	goAfter := settledGoroutines(goBefore + 5)
	lr.Measured = append(lr.Measured, Measure{Name: "goroutines_before", Value: float64(goBefore), Unit: "count", Label: "measured"},
		Measure{Name: "goroutines_after", Value: float64(goAfter), Unit: "count", Label: "measured"})
	heapAfter := heapMB()
	lr.Measured = append(lr.Measured, Measure{Name: "heap_retained_after_load", Value: heapAfter - heapBefore, Unit: "MiB", Label: "measured"})
	var leak []Violation
	switch {
	case goAfter > goBefore+5:
		leak = append(leak, Violation{"goroutine_growth",
			fmt.Sprintf("goroutines went from %d to %d and did not settle", goBefore, goAfter)})
	case heapAfter-heapBefore > 128:
		leak = append(leak, Violation{"memory_growth",
			fmt.Sprintf("%.0f MiB retained after load; the limit is 128 MiB", heapAfter-heapBefore)})
	}
	e.record(&lr, "leak-and-memory", leak, nil)

	e.soak(&lr)
	raceNote(&lr)
	lr.Notes = append(lr.Notes, fmt.Sprintf("%d concurrent sessions for %s; latencies are measured on this machine", e.p.concurrency, e.p.loadDur))
	finish(&lr)
	return lr
}

// loadEngine runs concurrent sessions through the engine and checks every output.
func (e *env) loadEngine(lr *LayerResult) {
	eng := newCustomEngine(allNames(), &memStore{}, PricingDate)
	deadline := time.Now().Add(e.p.loadDur)
	var mu sync.Mutex
	var lat []time.Duration
	var vs []Violation
	var wg sync.WaitGroup
	for w := 0; w < e.p.concurrency; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			spec := sampleSpec(e.opts.Seed, 1000+w, 20)
			bodies := buildBodies(spec, genTurns(spec, e.opts.Seed+uint64(w))) //nolint:gosec // w is a small non-negative worker index
			key := fmt.Sprintf("load-%d", w)
			for i := 0; time.Now().Before(deadline) && len(bodies) > 0; i++ {
				body := bodies[i%len(bodies)]
				t0 := time.Now()
				res := eng.TransformRequest(context.Background(), optimize.RequestIn{
					Provider: "anthropic", Path: "/v1/messages", Header: http.Header{}, Body: body,
				}, optimize.Session{Key: key, Agent: customAgent, TroveRun: true})
				d := time.Since(t0)
				mu.Lock()
				lat = append(lat, d)
				if i%8 == 0 { // checking every request would slow the load; one in eight still covers many turns
					vs = append(vs, CheckRequest(body, res.Body, declaredOverheadTokens(res))...)
				}
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()
	p50, p99 := percentiles(lat)
	lr.Measured = append(lr.Measured,
		Measure{Name: "engine_transform_p50", Value: round2(p50), Unit: "ms", Label: "measured"},
		Measure{Name: "engine_transform_p99", Value: round2(p99), Unit: "ms", Label: "measured"},
		Measure{Name: "engine_transforms", Value: float64(len(lat)), Unit: "count", Label: "measured"})
	e.record(lr, "engine-concurrent-sessions", vs, nil)
}

// loadGateway sends concurrent requests through the real gateway handler and compares them with the same
// upstream call made directly. The overhead is measured; it is not asserted, because it depends on the machine.
func (e *env) loadGateway(lr *LayerResult) {
	rec := &countingRecorder{}
	srv, err := newTestGateway(fakeUpstream{}, rec)
	if err != nil {
		e.record(lr, "gateway-concurrent", []Violation{{"gateway_start", err.Error()}}, nil)
		return
	}
	defer srv.Close()
	client := loopbackClient(15*time.Second, e.p.concurrency)
	deadline := time.Now().Add(e.p.loadDur)
	var mu sync.Mutex
	var lat []time.Duration
	var errs int
	var wg sync.WaitGroup
	for w := 0; w < e.p.concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				req, err := messageRequest(srv.URL)
				if err != nil {
					mu.Lock()
					errs++
					mu.Unlock()
					return
				}
				t0 := time.Now()
				resp, err := client.Do(req)
				d := time.Since(t0)
				ok := err == nil && resp.StatusCode == http.StatusOK
				if resp != nil {
					_, _ = io.Copy(io.Discard, resp.Body)
					_ = resp.Body.Close()
				}
				mu.Lock()
				lat = append(lat, d)
				if !ok {
					errs++
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	gp50, gp99 := percentiles(lat)

	// Baseline: the same upstream call without the gateway, measured the same way.
	var base []time.Duration
	for i := 0; i < 200; i++ {
		req, _ := messageRequest("http://fake.invalid")
		t0 := time.Now()
		resp, err := fakeUpstream{}.RoundTrip(req)
		base = append(base, time.Since(t0))
		if err == nil {
			_ = resp.Body.Close()
		}
	}
	bp50, _ := percentiles(base)

	lr.Measured = append(lr.Measured,
		Measure{Name: "gateway_p50", Value: round2(gp50), Unit: "ms", Label: "measured"},
		Measure{Name: "gateway_p99", Value: round2(gp99), Unit: "ms", Label: "measured"},
		Measure{Name: "gateway_p50_overhead_vs_direct", Value: round2(gp50 - bp50), Unit: "ms", Label: "measured"},
		Measure{Name: "gateway_requests", Value: float64(len(lat)), Unit: "count", Label: "measured"},
		Measure{Name: "gateway_recorded_rows", Value: float64(rec.n.Load()), Unit: "count", Label: "measured"})
	var vs []Violation
	if errs > 0 {
		vs = append(vs, Violation{"gateway_errors", fmt.Sprintf("%d of %d requests did not return 200", errs, len(lat))})
	}
	e.record(lr, "gateway-concurrent-sessions", vs, nil)
}

// loadSQLite writes rows from many goroutines through the real writer and checks that every row is accounted
// for: written, dropped by design when the buffer is full, or lost to a database error (which must be zero).
func (e *env) loadSQLite(lr *LayerResult) {
	dir := filepath.Join(e.dir, "sqlite")
	db, err := store.Open(dir)
	if err != nil {
		e.record(lr, "sqlite-contention", []Violation{{"store_open", err.Error()}}, nil)
		return
	}
	w := store.NewWriter(db, store.WriterOptions{})
	const workers, per = 8, 500
	var wg sync.WaitGroup
	for g := 0; g < workers; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < per; i++ {
				w.Enqueue(store.Request{TS: time.Now(), SessionKey: fmt.Sprintf("s%d", g), Agent: "claude_code",
					Provider: "anthropic", Model: "claude-sonnet-5-5", Endpoint: "/v1/messages", StatusCode: 200})
			}
		}(g)
	}
	wg.Wait()
	_ = w.Close(context.Background())
	_ = db.Close()
	st := w.Stats()
	total := uint64(workers * per)
	lr.Measured = append(lr.Measured,
		Measure{Name: "sqlite_rows_written", Value: float64(st.Written), Unit: "count", Label: "measured"},
		Measure{Name: "sqlite_rows_dropped", Value: float64(st.Dropped), Unit: "count", Label: "measured"},
		Measure{Name: "sqlite_rows_failed", Value: float64(st.Failed), Unit: "count", Label: "measured"})
	var vs []Violation
	if st.Written+st.Dropped+st.Failed != total {
		vs = append(vs, Violation{"sqlite_accounting", fmt.Sprintf("%d rows in, %d accounted for", total, st.Written+st.Dropped+st.Failed)})
	}
	if st.Failed > 0 {
		vs = append(vs, Violation{"sqlite_write_errors", fmt.Sprintf("%d rows were lost to database errors", st.Failed)})
	}
	e.record(lr, "sqlite-contention", vs, nil)
}

// soak runs mixed engine and gateway traffic for the configured duration, samples memory and goroutines each
// second, and fails only on a sustained heap-growth trend. A soak shorter than a minute is too short to judge a
// trend, so it is reported as measured only.
func (e *env) soak(lr *LayerResult) {
	dur := e.p.soak
	if dur <= 0 {
		return
	}
	eng := newCustomEngine(allNames(), &memStore{}, PricingDate)
	rec := &countingRecorder{}
	srv, err := newTestGateway(fakeUpstream{}, rec)
	if err != nil {
		e.record(lr, "soak", []Violation{{"gateway_start", err.Error()}}, nil)
		return
	}
	defer srv.Close()
	client := loopbackClient(10*time.Second, 0)

	var samples []heapPoint
	var vs []Violation
	start := time.Now()
	deadline := start.Add(dur)
	nextSample := start
	spec := sampleSpec(e.opts.Seed, 4242, 20)
	bodies := buildBodies(spec, genTurns(spec, e.opts.Seed))
	var iter int
	for time.Now().Before(deadline) && len(bodies) > 0 {
		body := bodies[iter%len(bodies)]
		key := fmt.Sprintf("soak-%d", iter%16)
		res := eng.TransformRequest(context.Background(), optimize.RequestIn{
			Provider: "anthropic", Path: "/v1/messages", Header: http.Header{}, Body: body,
		}, optimize.Session{Key: key, Agent: customAgent, TroveRun: true})
		if iter%50 == 0 {
			vs = append(vs, CheckRequest(body, res.Body, declaredOverheadTokens(res))...)
		}
		if req, err := messageRequest(srv.URL); err == nil {
			if resp, err := client.Do(req); err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					vs = append(vs, Violation{"soak_gateway_status", fmt.Sprintf("status %d during soak", resp.StatusCode)})
				}
			} else {
				vs = append(vs, Violation{"soak_gateway_error", "a gateway request failed during soak"})
			}
		}
		iter++
		if now := time.Now(); !now.Before(nextSample) {
			samples = append(samples, heapPoint{now.Sub(start).Minutes(), heapMB()})
			nextSample = now.Add(time.Second)
		}
	}
	slope := heapSlope(samples)
	lr.Measured = append(lr.Measured,
		Measure{Name: "soak_iterations", Value: float64(iter), Unit: "count", Label: "measured"},
		Measure{Name: "soak_duration", Value: round2(time.Since(start).Minutes()), Unit: "min", Label: "measured"},
		Measure{Name: "soak_heap_trend", Value: round2(slope), Unit: "MiB/min", Label: "measured"})
	if dur >= time.Minute && slope > 2 {
		vs = append(vs, Violation{"heap_growth_trend",
			fmt.Sprintf("heap grows %.2f MiB/min over a %s soak; the limit is 2 MiB/min", slope, dur)})
	} else if dur < time.Minute {
		lr.Notes = append(lr.Notes, fmt.Sprintf("soak of %s is too short to judge a memory trend; its trend is measured only", dur))
	}
	e.record(lr, "soak-mixed-traffic", vs, nil)
}

// heapPoint is one heap sample: minutes since the soak started, and live heap in MiB.
type heapPoint struct{ t, mb float64 }

// heapSlope is the least-squares slope (MiB per minute) of the samples after the first fifth of the run,
// which is warm-up. It returns 0 when there are too few samples to fit a line.
func heapSlope(s []heapPoint) float64 {
	if len(s) < 4 {
		return 0
	}
	s = s[len(s)/5:]
	var mt, mm float64
	for _, p := range s {
		mt += p.t
		mm += p.mb
	}
	n := float64(len(s))
	mt, mm = mt/n, mm/n
	var num, den float64
	for _, p := range s {
		num += (p.t - mt) * (p.mb - mm)
		den += (p.t - mt) * (p.t - mt)
	}
	if den == 0 {
		return 0
	}
	return num / den
}

// raceNote records that the race detector is not run from inside this command. It is always skipped, never a
// pass: -race needs cgo, and running `go test` from within the command would recurse into this package. The
// note says whether a C compiler was found, which is all this command can know.
func raceNote(lr *LayerResult) {
	if _, err := exec.LookPath("gcc"); err != nil {
		lr.Notes = append(lr.Notes, "race detector: skipped; no C compiler on PATH (-race needs cgo). Run `go test -race ./internal/stress` where one exists")
		return
	}
	lr.Notes = append(lr.Notes, "race detector: skipped by this command (it does not run go test). Run `go test -race ./internal/stress` separately")
}
