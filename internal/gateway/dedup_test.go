package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/analyze"
	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/store"
)

const dedupReply = `{"id":"msg_X","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1000,"cache_read_input_tokens":3000,"output_tokens":20}}`

var dedupBody = []byte(`{"model":"claude-opus-5-5","max_tokens":64,"messages":[{"role":"user","content":"hello DEDUPSENTINEL"}]}`)

// held is an upstream that holds every response until it is released, so that two
// requests really are in flight at once. With early set it sends the status line and
// headers first and holds only the body.
type held struct {
	mu      sync.Mutex
	calls   int
	bodies  [][]byte
	arrived chan struct{}
	early   chan struct{}
	release chan struct{}
	flush   bool
}

func newHeld(flush bool) *held {
	return &held{arrived: make(chan struct{}, 16), early: make(chan struct{}, 16), release: make(chan struct{}), flush: flush}
}

func (h *held) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		h.calls++
		h.bodies = append(h.bodies, b)
		h.mu.Unlock()
		h.arrived <- struct{}{}
		w.Header().Set("Content-Type", "application/json")
		if h.flush {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			h.early <- struct{}{}
		}
		<-h.release
		_, _ = io.WriteString(w, dedupReply)
	})
}

func (h *held) waitArrived(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-h.arrived:
		case <-time.After(5 * time.Second):
			t.Fatalf("upstream saw %d of %d requests", i, n)
		}
	}
}

func (h *held) callCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls
}

type postResult struct {
	status int
	body   []byte
	err    error
}

// post sends a request on its own goroutine, so the test can start another one while it is held.
func post(url string, hdr map[string]string, body []byte) <-chan postResult {
	out := make(chan postResult, 1)
	go func() {
		req, err := http.NewRequest("POST", url, bytes.NewReader(body))
		if err != nil {
			out <- postResult{err: err}
			return
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			out <- postResult{err: err}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		out <- postResult{status: resp.StatusCode, body: b}
	}()
	return out
}

func finish(t *testing.T, rs ...<-chan postResult) []postResult {
	t.Helper()
	var out []postResult
	for _, c := range rs {
		select {
		case r := <-c:
			if r.err != nil {
				t.Fatal(r.err)
			}
			out = append(out, r)
		case <-time.After(5 * time.Second):
			t.Fatal("a request did not finish")
		}
	}
	return out
}

// dedupRows waits for n rows and returns them with the shadow entry for in-flight dedup, if any.
func dedupRows(t *testing.T, g *rig, n int) (rows []store.Request, flagged []store.Request) {
	t.Helper()
	for i := 0; i < n; i++ {
		g.rec.wait(t)
	}
	g.rec.mu.Lock()
	defer g.rec.mu.Unlock()
	rows = append(rows, g.rec.rows...)
	for _, r := range rows {
		var sh analyze.Shadow
		if len(r.ShadowSavings) > 0 && json.Unmarshal(r.ShadowSavings, &sh) == nil {
			if _, ok := sh.Optimizers[config.OptimizerInflightDedup]; ok {
				flagged = append(flagged, r)
			}
		}
	}
	return rows, flagged
}

func shadowDedupOn() func(*Options) {
	return engineWith(map[string]string{config.OptimizerInflightDedup: "shadow"})
}

func msgURL(g *rig) string { return g.gw.URL + "/anthropic/v1/messages" }

// The first of two identical simultaneous requests is normal. The second is flagged, with what it
// cost. Both still go to the provider, and both clients get the provider's own answer.
func TestDedupShadowFlagsTheLaterOfTwoIdenticalRequestsAndSharesNothing(t *testing.T) {
	up := newHeld(false)
	g := newRigWith(t, up.handler(), shadowDedupOn())
	a := post(msgURL(g), anthropicHdr, dedupBody)
	up.waitArrived(t, 1)
	b := post(msgURL(g), anthropicHdr, dedupBody)
	up.waitArrived(t, 1)
	close(up.release)
	res := finish(t, a, b)

	if up.callCount() != 2 {
		t.Fatalf("the provider saw %d calls; shadow mode must not share a call", up.callCount())
	}
	for i, r := range res {
		if r.status != 200 || string(r.body) != dedupReply {
			t.Errorf("client %d got %d %q, want the provider's own reply", i, r.status, r.body)
		}
	}
	for i, body := range up.bodies {
		if !bytes.Equal(body, dedupBody) {
			t.Errorf("upstream body %d changed", i)
		}
	}
	rows, flagged := dedupRows(t, g, 2)
	if len(flagged) != 1 {
		t.Fatalf("want exactly one flagged request out of %d, got %d", len(rows), len(flagged))
	}
	var sh analyze.Shadow
	_ = json.Unmarshal(flagged[0].ShadowSavings, &sh)
	e := sh.Optimizers[config.OptimizerInflightDedup]
	if sh.Label != "estimated" || e.Tokens != 4020 || e.USD == nil || *e.USD <= 0 || flagged[0].CostUSD == nil || *e.USD != *flagged[0].CostUSD {
		t.Errorf("shadow entry %+v (label %q): want the request's 4020 billed tokens and its whole cost", e, sh.Label)
	}
	// Nothing was changed, so nothing counts as removed.
	for _, r := range rows {
		if r.TokensRemoved != 0 || len(r.OptimizationsApplied) > 0 && strings.Contains(string(r.OptimizationsApplied), "dedup") {
			t.Errorf("shadow mode changed something: removed=%d applied=%s", r.TokensRemoved, r.OptimizationsApplied)
		}
	}
	assertNoSecrets(t, g)
}

func TestDedupShadowIgnoresRequestsThatDifferInAnyPart(t *testing.T) {
	other := func(hdr map[string]string, k, v string) map[string]string {
		out := map[string]string{}
		for a, b := range hdr {
			out[a] = b
		}
		out[k] = v
		return out
	}
	cases := map[string]struct {
		hdr  map[string]string
		body []byte
	}{
		"another body":         {anthropicHdr, []byte(`{"model":"claude-opus-5-5","max_tokens":64,"messages":[{"role":"user","content":"a different prompt"}]}`)},
		"another api key":      {other(anthropicHdr, "x-api-key", "sk-ant-api03-ANOTHERKEYNOTTHESAME0123456789abcdef"), dedupBody},
		"another beta header":  {other(anthropicHdr, "anthropic-beta", "some-beta-2026"), dedupBody},
		"another api version":  {other(anthropicHdr, "anthropic-version", "2024-01-01"), dedupBody},
		"another content type": {other(anthropicHdr, "Content-Type", "application/json; charset=utf-8"), dedupBody},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			up := newHeld(false)
			g := newRigWith(t, up.handler(), shadowDedupOn())
			a := post(msgURL(g), anthropicHdr, dedupBody)
			up.waitArrived(t, 1)
			b := post(msgURL(g), c.hdr, c.body)
			up.waitArrived(t, 1)
			close(up.release)
			finish(t, a, b)
			if _, flagged := dedupRows(t, g, 2); len(flagged) != 0 {
				t.Errorf("%s: flagged as a duplicate", name)
			}
		})
	}
}

func TestDedupShadowIgnoresASequentialRepeat(t *testing.T) {
	up := newHeld(false)
	close(up.release) // answer at once
	g := newRigWith(t, up.handler(), shadowDedupOn())
	finish(t, post(msgURL(g), anthropicHdr, dedupBody))
	finish(t, post(msgURL(g), anthropicHdr, dedupBody))
	if _, flagged := dedupRows(t, g, 2); len(flagged) != 0 {
		t.Errorf("a repeat after the first finished was flagged")
	}
}

// Once the provider's response has started, joining would mean replaying a stream already in
// progress, so a duplicate that arrives then is not counted.
func TestDedupShadowIgnoresAnIdenticalRequestAfterTheFirstWasAnswered(t *testing.T) {
	up := newHeld(true)
	g := newRigWith(t, up.handler(), shadowDedupOn())
	a := post(msgURL(g), anthropicHdr, dedupBody)
	up.waitArrived(t, 1)
	<-up.early
	time.Sleep(200 * time.Millisecond) // let the gateway see the headers
	b := post(msgURL(g), anthropicHdr, dedupBody)
	up.waitArrived(t, 1)
	close(up.release)
	finish(t, a, b)
	if _, flagged := dedupRows(t, g, 2); len(flagged) != 0 {
		t.Errorf("a request that arrived after the first was answered was flagged")
	}
}

func TestDedupIsOffByDefault(t *testing.T) {
	for name, tweak := range map[string]func(*Options){
		"no engine":          nil,
		"engine, no state":   engineWith(nil),
		"state set to off":   engineWith(map[string]string{config.OptimizerInflightDedup: "off"}),
		"other optimizer on": engineWith(map[string]string{"noise_normalizer": "shadow"}),
	} {
		t.Run(name, func(t *testing.T) {
			up := newHeld(false)
			g := newRigWith(t, up.handler(), tweak)
			a := post(msgURL(g), anthropicHdr, dedupBody)
			up.waitArrived(t, 1)
			b := post(msgURL(g), anthropicHdr, dedupBody)
			up.waitArrived(t, 1)
			close(up.release)
			finish(t, a, b)
			if _, flagged := dedupRows(t, g, 2); len(flagged) != 0 {
				t.Errorf("flagged with dedup off")
			}
		})
	}
}

// "on" does the same as shadow today, because sharing a call is not built.
func TestDedupOnBehavesAsShadow(t *testing.T) {
	up := newHeld(false)
	g := newRigWith(t, up.handler(), engineWith(map[string]string{config.OptimizerInflightDedup: "on"}))
	a := post(msgURL(g), anthropicHdr, dedupBody)
	up.waitArrived(t, 1)
	b := post(msgURL(g), anthropicHdr, dedupBody)
	up.waitArrived(t, 1)
	close(up.release)
	finish(t, a, b)
	if up.callCount() != 2 {
		t.Fatalf("on must not share a call yet; the provider saw %d", up.callCount())
	}
	if _, flagged := dedupRows(t, g, 2); len(flagged) != 1 {
		t.Errorf("want one flagged request, got %d", len(flagged))
	}
}

// Neither the key nor its digest reaches a row or a log line, and the prompt text stays out too.
func TestDedupKeepsCredentialsAndPromptsOutOfRowsAndLogs(t *testing.T) {
	up := newHeld(false)
	g := newRigWith(t, up.handler(), shadowDedupOn())
	a := post(msgURL(g), anthropicHdr, dedupBody)
	up.waitArrived(t, 1)
	b := post(msgURL(g), anthropicHdr, dedupBody)
	up.waitArrived(t, 1)
	close(up.release)
	finish(t, a, b)
	rows, flagged := dedupRows(t, g, 2)
	if len(flagged) != 1 {
		t.Fatalf("flagged %d", len(flagged))
	}
	for _, r := range rows {
		raw, _ := json.Marshal(r)
		for _, secret := range []string{fakeKey, "DEDUPSENTINEL", "x-api-key"} {
			if strings.Contains(string(raw), secret) {
				t.Errorf("%q reached a stored row: %s", secret, raw)
			}
		}
	}
	assertNoSecrets(t, g)
	if strings.Contains(g.logs.String(), "DEDUPSENTINEL") {
		t.Errorf("prompt text reached the logs:\n%s", g.logs.String())
	}
}

// Many identical requests at once: all go upstream, all are answered, the book empties.
func TestDedupShadowUnderABurst(t *testing.T) {
	up := newHeld(false)
	g := newRigWith(t, up.handler(), shadowDedupOn())
	const n = 12
	var rs []<-chan postResult
	for i := 0; i < n; i++ {
		rs = append(rs, post(msgURL(g), anthropicHdr, dedupBody))
	}
	up.waitArrived(t, n)
	close(up.release)
	for _, r := range finish(t, rs...) {
		if r.status != 200 || string(r.body) != dedupReply {
			t.Errorf("a client got %d %q", r.status, r.body)
		}
	}
	_, flagged := dedupRows(t, g, n)
	if up.callCount() != n || len(flagged) != n-1 {
		t.Errorf("provider calls %d (want %d), flagged %d (want %d)", up.callCount(), n, len(flagged), n-1)
	}
}
