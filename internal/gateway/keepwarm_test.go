package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/analyze"
	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/store"
)

// warmBody is a cached Claude request: one cache marker on the system prompt, 5-minute lifetime.
var warmBody = []byte(`{"model":"claude-opus-5-5","max_tokens":4096,"stream":true,"system":[{"type":"text","text":"WARMSENTINEL system","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"WARMSENTINEL hi"}]}`)

const (
	// The real request wrote 5000 tokens at the 5-minute lifetime; a ping reads them back.
	warmReply = `{"id":"msg_W","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"cache_creation_input_tokens":5000,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":5000,"ephemeral_1h_input_tokens":0},"output_tokens":20}}`
	pingReply = `{"id":"msg_P","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"stop_reason":"max_tokens","usage":{"input_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":5000,"output_tokens":0}}`
)

// warmUpstream answers a ping (max_tokens 0) with pingReply, or pingStatus when set, and any other
// request with warmReply. It keeps every request it saw.
type warmUpstream struct {
	mu         sync.Mutex
	seen       []seenReq
	pingStatus int
}

type seenReq struct {
	path, query string
	header      http.Header
	body        []byte
}

func (u *warmUpstream) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.seen = append(u.seen, seenReq{r.URL.Path, r.URL.RawQuery, r.Header.Clone(), b})
		status := u.pingStatus
		u.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		var m struct {
			MaxTokens *int `json:"max_tokens"`
		}
		_ = json.Unmarshal(b, &m)
		if m.MaxTokens != nil && *m.MaxTokens == 0 {
			if status != 0 {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"no"}}`)
				return
			}
			_, _ = io.WriteString(w, pingReply)
			return
		}
		_, _ = io.WriteString(w, warmReply)
	})
}

func (u *warmUpstream) pings() []seenReq {
	u.mu.Lock()
	defer u.mu.Unlock()
	var out []seenReq
	for _, s := range u.seen {
		if bytes.Contains(s.body, []byte(`"max_tokens":0`)) {
			out = append(out, s)
		}
	}
	return out
}

// fakeTimers stands in for time.AfterFunc so a test decides when a ping is due.
type fakeTimers struct {
	mu   sync.Mutex
	list []*fakeTimer
}

type fakeTimer struct {
	d       time.Duration
	f       func()
	stopped bool
}

func (ft *fakeTimers) after(d time.Duration, f func()) func() bool {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	t := &fakeTimer{d: d, f: f}
	ft.list = append(ft.list, t)
	return func() bool {
		ft.mu.Lock()
		defer ft.mu.Unlock()
		was := !t.stopped
		t.stopped = true
		return was
	}
}

func (ft *fakeTimers) live() []*fakeTimer {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	var out []*fakeTimer
	for _, t := range ft.list {
		if !t.stopped {
			out = append(out, t)
		}
	}
	return out
}

// fire runs the one pending timer, as the runtime would when it is due.
func (ft *fakeTimers) fire(t *testing.T) {
	t.Helper()
	l := ft.live()
	if len(l) != 1 {
		t.Fatalf("want exactly one pending ping, got %d", len(l))
	}
	ft.mu.Lock()
	l[0].stopped = true
	ft.mu.Unlock()
	l[0].f()
}

type warmClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *warmClock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *warmClock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func warmRig(t *testing.T, state string, tweak func(*Options)) (*rig, *warmUpstream, *fakeTimers, *warmClock) {
	t.Helper()
	up := &warmUpstream{}
	clk := &warmClock{t: time.Date(2026, 11, 17, 10, 0, 0, 0, time.Local)}
	g := newRigWith(t, up.handler(), func(o *Options) {
		engineWith(map[string]string{config.OptimizerKeepWarm: state})(o)
		o.Now = clk.now
		if tweak != nil {
			tweak(o)
		}
	})
	ft := &fakeTimers{}
	g.srv.warm.after = ft.after
	return g, up, ft, clk
}

func sendWarm(t *testing.T, g *rig, body []byte) store.Request {
	t.Helper()
	r := finish(t, post(msgURL(g)+"?beta=true", anthropicHdr, body))[0]
	if r.status != 200 {
		t.Fatalf("status %d", r.status)
	}
	return g.rec.wait(t)
}

func TestPingBodyKeepsThePromptAndAsksForNothing(t *testing.T) {
	out, why := pingBody(warmBody)
	if why != "" {
		t.Fatalf("an ordinary cached request was refused: %s", why)
	}
	var orig, ping map[string]json.RawMessage
	_ = json.Unmarshal(warmBody, &orig)
	if err := json.Unmarshal(out, &ping); err != nil {
		t.Fatal(err)
	}
	if string(ping["max_tokens"]) != "0" {
		t.Errorf("max_tokens = %s, want 0 (the documented pre-warm request)", ping["max_tokens"])
	}
	if _, ok := ping["stream"]; ok {
		t.Error("stream must be dropped: the provider refuses max_tokens 0 with stream true")
	}
	for k, v := range orig {
		if k == "max_tokens" || k == "stream" {
			continue
		}
		if !bytes.Equal(ping[k], v) {
			t.Errorf("%s changed:\n %s\n %s", k, v, ping[k])
		}
	}
	if len(ping) != len(orig)-1 {
		t.Errorf("fields added or lost: %d vs %d", len(ping), len(orig))
	}
}

func TestPingBodyRefusesWhatTheProviderWouldRejectOrWeMayNotPing(t *testing.T) {
	sys := `"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"x"}]`
	for name, body := range map[string]string{
		"extended thinking":  `{"model":"m","max_tokens":9,"thinking":{"type":"enabled","budget_tokens":2000},` + sys + `}`,
		"forced tool":        `{"model":"m","max_tokens":9,"tool_choice":{"type":"tool","name":"x"},` + sys + `}`,
		"any tool":           `{"model":"m","max_tokens":9,"tool_choice":{"type":"any"},` + sys + `}`,
		"structured output":  `{"model":"m","max_tokens":9,"output_format":{"type":"json_schema"},` + sys + `}`,
		"structured output2": `{"model":"m","max_tokens":9,"output_config":{"format":{"type":"json_schema"}},` + sys + `}`,
		"nothing cached":     `{"model":"m","max_tokens":9,"messages":[{"role":"user","content":"x"}]}`,
		"one-hour lifetime":  `{"model":"m","max_tokens":9,"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[]}`,
		"not json":           `{"model":`,
	} {
		if out, why := pingBody([]byte(body)); why == "" || out != nil {
			t.Errorf("%s: should not be pinged", name)
		}
	}
	for name, body := range map[string]string{
		"automatic caching": `{"model":"m","max_tokens":9,"cache_control":{"type":"ephemeral"},"messages":[{"role":"user","content":"x"}]}`,
		"adaptive thinking": `{"model":"m","max_tokens":9,"thinking":{"type":"adaptive"},` + sys + `}`,
		"auto tool choice":  `{"model":"m","max_tokens":9,"tool_choice":{"type":"auto"},` + sys + `}`,
	} {
		if _, why := pingBody([]byte(body)); why != "" {
			t.Errorf("%s: refused (%s)", name, why)
		}
	}
}

// The heart of 4.3: after a cached request, a ping goes out shortly before the 5-minute cache would
// expire, with the same key, endpoint and prompt, and it is recorded on the bill.
func TestKeepWarmOnPingsWithTheSameKeyEndpointAndPrompt(t *testing.T) {
	g, up, ft, clk := warmRig(t, config.StateOn, nil)
	first := sendWarm(t, g, warmBody)

	l := ft.live()
	if len(l) != 1 || l[0].d != warmEvery {
		t.Fatalf("want one ping due in %v, got %d timers", warmEvery, len(l))
	}
	clk.add(warmEvery)
	ft.fire(t)
	ping := g.rec.wait(t)

	ps := up.pings()
	if len(ps) != 1 {
		t.Fatalf("the provider saw %d pings, want 1", len(ps))
	}
	p := ps[0]
	want, _ := pingBody(warmBody)
	if !bytes.Equal(p.body, want) || p.path != "/v1/messages" || p.query != "beta=true" {
		t.Errorf("ping went to %s?%s with %s", p.path, p.query, p.body)
	}
	if p.header.Get("x-api-key") != fakeKey || p.header.Get("anthropic-version") != "2023-06-01" {
		t.Error("the ping must use the session's own key and API version")
	}
	if ua := p.header.Get("User-Agent"); !strings.HasPrefix(ua, "trove-keep-warm/") {
		t.Errorf("a ping must say it is Trove, not the agent (rule 3): User-Agent %q", ua)
	}
	if ping.Agent != config.OptimizerKeepWarm || ping.SessionKey != first.SessionKey || ping.CacheReadTokens != 5000 || ping.CostUSD == nil || *ping.CostUSD <= 0 {
		t.Errorf("ping row %+v: want it on the same session, with its cost", ping)
	}
	if !strings.Contains(string(ping.OptimizationsApplied), `"keep_warm"`) {
		t.Errorf("ping row must name the optimizer: %s", ping.OptimizationsApplied)
	}

	// A second ping, then none: the default is 2 per idle period.
	clk.add(warmEvery)
	ft.fire(t)
	g.rec.wait(t)
	if n := len(up.pings()); n != 2 {
		t.Fatalf("want 2 pings, got %d", n)
	}
	if n := len(ft.live()); n != 0 {
		t.Errorf("%d pings still pending after the cap of 2", n)
	}
	if n := g.srv.warm.held(); n != 0 {
		t.Errorf("%d sessions still held after the last ping; the key and prompt must be dropped", n)
	}
	assertNoSecrets(t, g)
	if strings.Contains(g.logs.String(), "WARMSENTINEL") {
		t.Errorf("prompt text reached the logs:\n%s", g.logs.String())
	}
}

func TestKeepWarmRealRequestReplacesThePendingPing(t *testing.T) {
	g, up, ft, clk := warmRig(t, config.StateOn, nil)
	sendWarm(t, g, warmBody)
	clk.add(time.Minute)
	sendWarm(t, g, warmBody)
	if n := len(ft.live()); n != 1 {
		t.Fatalf("want the old ping cancelled and one new one pending, got %d", n)
	}
	if g.srv.warm.held() != 1 || len(up.pings()) != 0 {
		t.Error("one session held, nothing pinged yet")
	}
}

func TestKeepWarmLeavesIneligibleRequestsAlone(t *testing.T) {
	for name, body := range map[string]string{
		"thinking": `{"model":"claude-opus-5-5","max_tokens":9,"thinking":{"type":"enabled","budget_tokens":2000},"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"x"}]}`,
		"1h":       `{"model":"claude-opus-5-5","max_tokens":9,"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[{"role":"user","content":"x"}]}`,
	} {
		g, _, ft, _ := warmRig(t, config.StateOn, nil)
		sendWarm(t, g, []byte(body))
		if len(ft.live()) != 0 || g.srv.warm.held() != 0 {
			t.Errorf("%s: armed a ping it may not send", name)
		}
	}
}

func TestKeepWarmFailedPingIsNotRetried(t *testing.T) {
	g, up, ft, clk := warmRig(t, config.StateOn, nil)
	up.pingStatus = http.StatusBadRequest
	sendWarm(t, g, warmBody)
	clk.add(warmEvery)
	ft.fire(t)
	g.rec.wait(t)
	if len(ft.live()) != 0 || g.srv.warm.held() != 0 {
		t.Error("a failed ping must drop the session, not retry")
	}
}

func TestKeepWarmDailyCapStopsPings(t *testing.T) {
	g, up, ft, clk := warmRig(t, config.StateOn, func(o *Options) { o.Config.Gateway.KeepWarmDailyCapUSD = 0.000001 })
	sendWarm(t, g, warmBody)
	clk.add(warmEvery)
	ft.fire(t) // the first ping spends past the tiny cap
	g.rec.wait(t)
	if len(ft.live()) == 1 {
		clk.add(warmEvery)
		ft.fire(t)
	}
	if n := len(up.pings()); n != 1 {
		t.Errorf("want pinging to stop once the day's cap is spent, got %d pings", n)
	}
	if g.srv.warm.held() != 0 {
		t.Error("the session must be dropped at the cap")
	}
}

func TestKeepWarmTurnedOffDropsAtTheNextPing(t *testing.T) {
	g, up, ft, clk := warmRig(t, config.StateOn, nil)
	sendWarm(t, g, warmBody)
	var off Options
	engineWith(map[string]string{config.OptimizerKeepWarm: config.StateOff})(&off)
	g.srv.opts.Optimizer = off.Optimizer // the user turned it off (or the kill switch is on)
	clk.add(warmEvery)
	ft.fire(t)
	if len(up.pings()) != 0 || g.srv.warm.held() != 0 {
		t.Error("with the kill switch on, nothing may be sent and nothing held")
	}
}

func TestKeepWarmStopDropsEverything(t *testing.T) {
	g, _, ft, _ := warmRig(t, config.StateOn, nil)
	sendWarm(t, g, warmBody)
	g.srv.warm.stop()
	if len(ft.live()) != 0 || g.srv.warm.held() != 0 {
		t.Error("stopping the gateway must cancel pings and drop held keys and prompts")
	}
}

// Shadow mode holds no key and no prompt and sends nothing. When the session's next request comes,
// the row says what pinging would have saved (or cost), labeled estimated.
func TestKeepWarmShadowEstimatesAndHoldsNothing(t *testing.T) {
	for _, tc := range []struct {
		gap      time.Duration
		positive bool
	}{{8 * time.Minute, true}, {40 * time.Minute, false}} {
		g, up, ft, clk := warmRig(t, config.StateShadow, nil)
		sendWarm(t, g, warmBody)
		if len(ft.live()) != 0 || g.srv.warm.held() != 0 {
			t.Fatal("shadow mode armed a ping or held a key")
		}
		clk.add(tc.gap)
		next := sendWarm(t, g, warmBody)
		if len(up.pings()) != 0 {
			t.Fatal("shadow mode sent a ping")
		}
		var sh analyze.Shadow
		_ = json.Unmarshal(next.ShadowSavings, &sh)
		e, ok := sh.Optimizers[config.OptimizerKeepWarm]
		if !ok || sh.Label != "estimated" || e.USD == nil || (*e.USD > 0) != tc.positive {
			t.Errorf("gap %v: shadow %s; want an estimated entry that is positive=%v", tc.gap, next.ShadowSavings, tc.positive)
		}
	}
}

func TestShadowWithinTheCacheLifetimeEstimatesNothing(t *testing.T) {
	g, _, _, clk := warmRig(t, config.StateShadow, nil)
	sendWarm(t, g, warmBody)
	clk.add(2 * time.Minute)
	next := sendWarm(t, g, warmBody)
	if strings.Contains(string(next.ShadowSavings), config.OptimizerKeepWarm) {
		t.Errorf("the cache was still warm, so pings would change nothing: %s", next.ShadowSavings)
	}
}

func TestWarmSlotNeverPrintsTheKeyOrPrompt(t *testing.T) {
	s := &warmSlot{cred: http.Header{"X-Api-Key": {fakeKey}}, body: []byte("WARMSENTINEL")}
	var logs bytes.Buffer
	slog.New(slog.NewTextHandler(&logs, nil)).Info("x", "slot", s, "v", *s)
	out := fmt.Sprintf("%v %+v %#v %s", s, s, s, s) + logs.String()
	for _, secret := range []string{fakeKey, "WARMSENTINEL"} {
		if strings.Contains(out, secret) {
			t.Errorf("%q printed: %s", secret, out)
		}
	}
}

// The spend guardrail: once pings have cost more than they are estimated to have saved, pinging
// stops for the rest of the window.
func TestWarmLedgerTripsWhenPingsCostMoreThanTheySave(t *testing.T) {
	now := time.Date(2026, 11, 17, 10, 0, 0, 0, time.Local)
	l := &warmLedger{}
	for i := 0; i < warmMinPingsToJudge-1; i++ {
		l.ping(now, 0.01)
	}
	if l.tripped(now) {
		t.Error("judged on too few pings")
	}
	l.ping(now, 0.01)
	if !l.tripped(now) {
		t.Error("pings cost money and saved nothing: must trip")
	}
	l2 := &warmLedger{}
	for i := 0; i < warmMinPingsToJudge; i++ {
		l2.ping(now, 0.01)
	}
	l2.saved(now, 1)
	if l2.tripped(now) {
		t.Error("savings above cost must not trip")
	}
	if l.tripped(now.Add(25 * time.Hour)) {
		t.Error("the window is 24 hours")
	}
}
