// loadtest sends concurrent requests to one URL and prints latency percentiles. It is QA tooling for the benchmark
// record (qa/benchmarks/RECORD.md): it makes no provider call, because the URL is the local fake upstream or the local
// gateway. Output is one TSV line: concurrency, requests, p50_ms, p90_ms, p99_ms, errors.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"
)

func main() {
	url := flag.String("url", "", "URL to POST to")
	conc := flag.Int("c", 1, "concurrent workers")
	per := flag.Int("n", 50, "requests per worker")
	key := flag.String("key", "", "value for x-api-key (a fake in the accepted shape)")
	bodyFile := flag.String("body", "", "request body file")
	flag.Parse()
	if *url == "" || *bodyFile == "" {
		fmt.Fprintln(os.Stderr, "usage: loadtest -url URL -body FILE [-c N] [-n N] [-key K]")
		os.Exit(2)
	}
	body, err := os.ReadFile(*bodyFile) //nolint:gosec // QA tooling reads its own fixture
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// Loopback only: the dialer refuses any other address, so a mistyped or remote URL fails instead of connecting.
	dialer := &net.Dialer{Timeout: 30 * time.Second}
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		MaxIdleConnsPerHost: *conc,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
				return nil, fmt.Errorf("loadtest only connects to loopback addresses (127.0.0.1 or ::1), not %q", host)
			}
			return dialer.DialContext(ctx, network, addr)
		},
	}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	var mu sync.Mutex
	var lat []time.Duration
	errs := 0
	var wg sync.WaitGroup
	for w := 0; w < *conc; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < *per; i++ {
				req, _ := http.NewRequest("POST", *url, bytes.NewReader(body))
				req.Header.Set("content-type", "application/json")
				req.Header.Set("anthropic-version", "2023-06-01")
				if *key != "" {
					req.Header.Set("x-api-key", *key)
				}
				start := time.Now()
				resp, err := client.Do(req)
				d := time.Since(start)
				ok := err == nil && resp.StatusCode == 200
				if resp != nil {
					_, _ = io.Copy(io.Discard, resp.Body)
					_ = resp.Body.Close()
				}
				mu.Lock()
				if ok {
					lat = append(lat, d)
				} else {
					errs++
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	pct := func(p float64) float64 {
		if len(lat) == 0 {
			return 0
		}
		idx := int(float64(len(lat)-1) * p)
		return float64(lat[idx].Microseconds()) / 1000
	}
	fmt.Printf("%d\t%d\t%.2f\t%.2f\t%.2f\t%d\n", *conc, *conc**per, pct(0.50), pct(0.90), pct(0.99), errs)
}
