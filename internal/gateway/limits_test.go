package gateway

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// A client that stalls while sending its request is cut off, but a response stream that runs far longer than the
// read timeout is not: the timeout covers reading the request only (finding F-7).
func TestAStalledRequestIsCutOffButALongStreamIsNot(t *testing.T) {
	defer func(old time.Duration) { requestReadTimeout = old }(requestReadTimeout)
	requestReadTimeout = 300 * time.Millisecond

	g := newRig(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: ping\ndata: {\"type\":\"ping\"}\n\n"))
		w.(http.Flusher).Flush()
		time.Sleep(900 * time.Millisecond) // three times the read timeout
		_, _ = w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))

	// A request that promises a body and never sends it.
	u, _ := url.Parse(g.gw.URL)
	conn, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, _ = conn.Write([]byte("POST /anthropic/v1/messages HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\nx-api-key: sk-ant-api03-stalledclient0123456789\r\n\r\n{"))
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 512)
	start := time.Now()
	n, _ := conn.Read(buf) // the server answers or closes once its read timeout passes
	if time.Since(start) > 3*time.Second {
		t.Fatalf("the stalled request was held for %v", time.Since(start))
	}
	if n > 0 && strings.Contains(string(buf[:n]), "200 OK") {
		t.Fatalf("a request that never finished was answered as if it had: %q", buf[:n])
	}

	// A complete request whose response outlasts the timeout several times over still gets every byte.
	req, _ := http.NewRequest("POST", g.gw.URL+"/anthropic/v1/messages", strings.NewReader(`{}`))
	for k, v := range anthropicHdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	all, err := io.ReadAll(resp.Body)
	if err != nil || !strings.Contains(string(all), "message_stop") {
		t.Fatalf("a long stream was cut by the request read timeout: err=%v body=%q", err, all)
	}
}

// Large request bodies held at the same moment are capped: one over the budget is refused with a plain 503 the
// agent can retry, and is accepted once the first is done. A request alone is never refused for its size.
func TestConcurrentLargeBodiesAreCappedAndRetriable(t *testing.T) {
	defer func(old int64) { maxBufferedBodies = old }(maxBufferedBodies)
	maxBufferedBodies = 1 << 20

	release := make(chan struct{})
	var once sync.Once
	arrived := make(chan struct{})
	g := newRig(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		once.Do(func() { close(arrived) })
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"type":"message","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	body := []byte(`{"p":"` + strings.Repeat("x", 800<<10) + `"}`) // ~800 KB each, two do not fit in 1 MiB

	first := make(chan int, 1)
	go func() {
		r, _ := do2(g.gw.URL+"/anthropic/v1/messages", body)
		first <- r
	}()
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("the first request never reached the upstream")
	}
	if got, msg := do2(g.gw.URL+"/anthropic/v1/messages", body); got != http.StatusServiceUnavailable || !strings.Contains(msg, "send the request again") {
		t.Fatalf("a second large request while the first is held: %d %q", got, msg)
	}
	close(release)
	if code := <-first; code != http.StatusOK {
		t.Fatalf("the first request: %d", code)
	}
	if code, _ := do2(g.gw.URL+"/anthropic/v1/messages", body); code != http.StatusOK {
		t.Fatalf("after the first finished the budget must be free again: %d", code)
	}
	// The budget is released when the handler returns, which is just after the response reaches the client.
	deadline := time.Now().Add(5 * time.Second)
	for g.srvBodyBytes() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := g.srvBodyBytes(); got != 0 {
		t.Fatalf("the body budget leaked %d bytes", got)
	}
}

// do2 posts body with an API key and returns the status and the response text.
func do2(url string, body []byte) (int, string) {
	req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	for k, v := range anthropicHdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (r *rig) srvBodyBytes() int64 { return r.srv.bodyBytes.Load() }
