package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/gateway/usage"
	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/store"
)

// Keep-warm pings (plan item 4.3; approved with its limits in docs/decisions/keep-warm.md).
//
// The provider's default prompt cache lives 5 minutes, counted from the start of the request that
// wrote or last read it, and every read restarts that lifetime. When an API-key session goes quiet,
// the next request after the cache expired writes the whole prefix again at 1.25x the input price.
// A ping re-reads the prefix (0.1x, or less on newer models) shortly before it expires.
//
// Provider details verified 2026-11-17 against
// https://platform.claude.com/docs/en/build-with-claude/prompt-caching and
// https://platform.claude.com/docs/en/about-claude/pricing: a read refreshes the 5-minute lifetime;
// the lifetime runs from the request's start; "max_tokens": 0 is the documented pre-warm request and
// bills no output; it is refused with stream true, extended thinking, structured outputs or a forced
// tool choice; max_tokens and stream are not among the settings that invalidate the cache.
//
// What is held: in "on" mode only, for one session at a time, the forwarded body and the request's
// credential headers, in process memory. They are dropped after the last ping, on a failed ping, at
// the daily cap, when the guardrail trips, when the session's next request replaces them, when the
// behavior is turned off, and when the gateway stops. They are never logged, stored or printed
// (warmSlot redacts itself). In shadow mode nothing but a time, a token count and a model is held.

const (
	// warmEvery is how long after a request (or ping) starts the next ping is sent: 30 seconds
	// inside the 5-minute lifetime.
	warmEvery = 4*time.Minute + 30*time.Second
	// warmLife is the provider's default cache lifetime.
	warmLife = 5 * time.Minute
	// warmMaxSlots and warmMaxBytes bound what is held in memory at once.
	warmMaxSlots = 64
	warmMaxBytes = 64 << 20
	// warmMinPingsToJudge is how many pings the guardrail waits for before it compares cost and savings.
	warmMinPingsToJudge = 6
	// warmPingTimeout bounds one ping.
	warmPingTimeout = 60 * time.Second
)

// credHeaders are the request headers a ping copies from the session's own request. The credential
// is needed to send it; the version and beta headers change how the prompt is read, so they must
// match for the cache to be hit. User-Agent is not copied: a ping says it is Trove (rule 3).
var credHeaders = []string{"X-Api-Key", "Authorization", "Anthropic-Version", "Anthropic-Beta", "Content-Type"}

// warmSlot is one session's pending ping. It holds a credential and prompt text, so it never prints them.
type warmSlot struct {
	key        string // the session's base key: a session split by the idle gap keeps it
	sessionKey string // the key its rows are recorded under
	rt         *route
	path       string
	query      string
	cred       http.Header
	body       []byte // the ping body
	cached     int64  // prefix tokens the last request wrote or read
	model      string
	start      time.Time // start of the last request or ping
	lastReal   time.Time // start of the session's last real request
	pings      int
	stop       func() bool
}

const slotRedacted = "[redacted keep-warm session]"

func (warmSlot) String() string             { return slotRedacted }
func (warmSlot) GoString() string           { return slotRedacted }
func (warmSlot) LogValue() slog.Value       { return slog.StringValue(slotRedacted) }
func (warmSlot) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, slotRedacted) }

// warmShadow is what shadow mode keeps about a session: no key, no prompt.
type warmShadow struct {
	start  time.Time
	cached int64
	model  string
}

// warmLedger keeps 24 hours of ping costs (measured) and savings (estimated) for the daily cap and
// the spend guardrail. In memory only.
type warmLedger struct {
	costs, savings []warmAmount
}

type warmAmount struct {
	at  time.Time
	usd float64
}

func (l *warmLedger) prune(now time.Time) {
	cut := now.Add(-24 * time.Hour)
	keep := func(xs []warmAmount) []warmAmount {
		i := 0
		for i < len(xs) && !xs[i].at.After(cut) {
			i++
		}
		return xs[i:]
	}
	l.costs, l.savings = keep(l.costs), keep(l.savings)
}

func (l *warmLedger) ping(at time.Time, usd float64) { l.costs = append(l.costs, warmAmount{at, usd}) }
func (l *warmLedger) saved(at time.Time, usd float64) {
	l.savings = append(l.savings, warmAmount{at, usd})
}

// tripped reports whether, over the last 24 hours and after enough pings to judge, pings cost more
// than they are estimated to have saved.
func (l *warmLedger) tripped(now time.Time) bool {
	l.prune(now)
	if len(l.costs) < warmMinPingsToJudge {
		return false
	}
	return sum(l.costs) > sum(l.savings)
}

// spentToday is what pings cost in now's local day.
func (l *warmLedger) spentToday(now time.Time) float64 {
	l.prune(now)
	day := store.DayKey(now)
	t := 0.0
	for _, c := range l.costs {
		if store.DayKey(c.at) == day {
			t += c.usd
		}
	}
	return t
}

func sum(xs []warmAmount) float64 {
	t := 0.0
	for _, x := range xs {
		t += x.usd
	}
	return t
}

// keepWarm holds the pending pings and the shadow notes.
type keepWarm struct {
	s     *Server
	after func(time.Duration, func()) func() bool // time.AfterFunc; replaceable in tests

	mu      sync.Mutex
	slots   map[string]*warmSlot
	shadow  map[string]warmShadow
	ledger  warmLedger
	bytes   int
	stopped bool
	warned  bool
}

func newKeepWarm(s *Server) *keepWarm {
	return &keepWarm{
		s: s, slots: map[string]*warmSlot{}, shadow: map[string]warmShadow{},
		after: func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop },
	}
}

// state is keep_warm's current state.
func (k *keepWarm) state() string {
	if k == nil || k.s.opts.Optimizer == nil {
		return config.StateOff
	}
	return k.s.opts.Optimizer.GatewayState(config.OptimizerKeepWarm)
}

// eligibleRoute reports whether pings may be sent on a route: the provider's own API (where the
// documented API key is the credential), the Messages endpoint, POST.
func eligibleRoute(r *http.Request, f *flight) bool {
	return f.rt.label == "anthropic" && f.provider == "anthropic" && f.usagePath == "/v1/messages" && r.Method == http.MethodPost
}

// pingBody returns the body of a ping for a request, or a reason it may not be pinged. Every field
// but max_tokens and stream keeps its exact bytes.
func pingBody(body []byte) ([]byte, string) {
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &top) != nil {
		return nil, "not_json"
	}
	if !bytes.Contains(body, []byte(`"cache_control"`)) {
		return nil, "nothing_cached"
	}
	if oneHour.Match(body) {
		return nil, "one_hour_lifetime" // the decision allows the 5-minute lifetime only
	}
	var th struct {
		Type string `json:"type"`
	}
	if raw, ok := top["thinking"]; ok && json.Unmarshal(raw, &th) == nil && th.Type == "enabled" {
		return nil, "extended_thinking"
	}
	var tc struct {
		Type string `json:"type"`
	}
	if raw, ok := top["tool_choice"]; ok && json.Unmarshal(raw, &tc) == nil && (tc.Type == "tool" || tc.Type == "any") {
		return nil, "forced_tool"
	}
	if _, ok := top["output_format"]; ok {
		return nil, "structured_output"
	}
	if raw, ok := top["output_config"]; ok {
		var oc map[string]json.RawMessage
		if json.Unmarshal(raw, &oc) == nil {
			if _, ok := oc["format"]; ok {
				return nil, "structured_output"
			}
		}
	}
	top["max_tokens"] = json.RawMessage("0")
	delete(top, "stream")
	out, err := json.Marshal(top)
	if err != nil {
		return nil, "not_json"
	}
	return out, ""
}

// baseKey is a session key without the idle-gap generation suffix (see session.Tracker), so a request
// that arrives after a long gap still finds what keep_warm noted about the session before it.
func baseKey(k string) string {
	if i := strings.IndexByte(k, '~'); i >= 0 {
		return k[:i]
	}
	return k
}

var oneHour = regexp.MustCompile(`"ttl"\s*:\s*"1h"`)

// arrive runs when a real request of a session reaches the gateway. It cancels the session's pending
// ping (the request itself refreshes the cache) and, in shadow mode, returns the estimate for what
// pinging would have saved since the session's last request. It never fails a request.
func (k *keepWarm) arrive(sessionKey string, at time.Time) (est *warmEstimate) {
	defer func() {
		if recover() != nil {
			est = nil
		}
	}()
	if k == nil || sessionKey == "" {
		return nil
	}
	sessionKey = baseKey(sessionKey)
	k.mu.Lock()
	defer k.mu.Unlock()
	if sl, ok := k.slots[sessionKey]; ok {
		k.dropLocked(sl)
		if sl.pings > 0 && at.Sub(sl.lastReal) > warmLife {
			// The cache would have expired without the pings. The saving is booked when this
			// request's row shows the cache read (see landed).
			return &warmEstimate{onPinged: true, cached: sl.cached, model: sl.model}
		}
		return nil
	}
	sh, ok := k.shadow[sessionKey]
	if !ok {
		return nil
	}
	delete(k.shadow, sessionKey)
	return &warmEstimate{gap: at.Sub(sh.start), cached: sh.cached, model: sh.model, maxPings: k.s.opts.Config.Gateway.KeepWarmMaxPings}
}

// warmEstimate carries, from a request's arrival to its row, what keep_warm has to say about it.
type warmEstimate struct {
	onPinged bool // "on": pings kept the cache warm across a gap longer than its lifetime
	gap      time.Duration
	cached   int64
	model    string
	maxPings int
}

// shadowUSD is what pinging would have saved across gap (negative: what it would have cost). ok is
// false when the cache was still warm, so pings would have changed nothing, or prices are unknown.
func (k *keepWarm) shadowUSD(e *warmEstimate, at time.Time) (usd float64, tokens int64, ok bool) {
	if e.gap <= warmLife || e.cached <= 0 {
		return 0, 0, false
	}
	write, read, priced := k.prices(e.model, e.cached, at)
	if !priced {
		return 0, 0, false
	}
	if e.gap <= time.Duration(e.maxPings)*warmEvery+warmLife {
		n := int64((e.gap - warmLife + warmEvery - 1) / warmEvery) // pings needed to bridge the gap
		return write - read - float64(n)*read, e.cached, true
	}
	return -float64(e.maxPings) * read, 0, true // the cache expired anyway; the pings were wasted
}

// prices returns what n prefix tokens cost written at the 5-minute lifetime and read, at list price.
func (k *keepWarm) prices(model string, n int64, at time.Time) (write, read float64, ok bool) {
	t := k.s.opts.Pricing
	if t == nil || model == "" {
		return 0, 0, false
	}
	w := t.Cost(pricing.Request{Provider: "anthropic", Model: model, Time: at, Usage: pricing.Usage{CacheWrite: map[string]int64{"5m": n}}})
	r := t.Cost(pricing.Request{Provider: "anthropic", Model: model, Time: at, Usage: pricing.Usage{CacheRead: n}})
	if w.Status != pricing.Priced || r.Status != pricing.Priced {
		return 0, 0, false
	}
	return w.Total.Float64(), r.Total.Float64(), true
}

// landed runs in the recording goroutine once a real request's response is known. In "on" mode it
// books the estimated saving of pings that kept the cache warm and arms the next ping; in shadow
// mode it notes the session's time, prefix size and model. cred is nil unless keep_warm was on.
func (k *keepWarm) landed(f *flight, res usage.Result, cred http.Header) {
	if k == nil || f.sessionKey == "" {
		return
	}
	key := baseKey(f.sessionKey)
	cached := res.CacheRead + res.CacheWriteTotal
	now := k.s.opts.Now()
	if f.warm != nil && f.warm.onPinged && res.CacheRead > 0 {
		if w, r, ok := k.prices(res.Model, min(res.CacheRead, f.warm.cached), now); ok {
			k.mu.Lock()
			k.ledger.saved(now, w-r)
			k.mu.Unlock()
		}
	}
	if cached <= 0 {
		return
	}
	body, why := pingBody(f.reqBody)
	if why != "" {
		return
	}
	switch k.state() {
	case config.StateShadow:
		k.mu.Lock()
		defer k.mu.Unlock()
		if len(k.shadow) >= 10000 {
			for key, v := range k.shadow {
				if now.Sub(v.start) > time.Hour {
					delete(k.shadow, key)
				}
			}
		}
		k.shadow[key] = warmShadow{start: f.start, cached: cached, model: res.Model}
	case config.StateOn:
		if cred == nil {
			return
		}
		k.mu.Lock()
		defer k.mu.Unlock()
		if k.stopped || len(k.slots) >= warmMaxSlots || k.bytes+len(body) > warmMaxBytes {
			return
		}
		if old, ok := k.slots[key]; ok {
			k.dropLocked(old)
		}
		sl := &warmSlot{
			key: key, sessionKey: f.sessionKey, rt: f.rt, path: f.path, query: f.query, cred: cred, body: body,
			cached: cached, model: res.Model, start: f.start, lastReal: f.start,
		}
		k.slots[key] = sl
		k.bytes += len(body)
		k.armLocked(sl, now)
	}
}

func (k *keepWarm) armLocked(sl *warmSlot, now time.Time) {
	d := sl.start.Add(warmEvery).Sub(now)
	if d < 0 {
		d = 0
	}
	sl.stop = k.after(d, func() { k.fire(sl) })
}

// dropLocked forgets a slot and cancels its ping. Dropping the references is all Go allows; the
// memory is not wiped (docs/decisions/keep-warm.md).
func (k *keepWarm) dropLocked(sl *warmSlot) {
	if sl.stop != nil {
		sl.stop()
	}
	if k.slots[sl.key] == sl {
		delete(k.slots, sl.key)
		k.bytes -= len(sl.body)
	}
	sl.cred, sl.body = nil, nil
}

// fire sends one ping for sl, if it is still wanted, and arms the next.
func (k *keepWarm) fire(sl *warmSlot) {
	defer func() {
		if rec := recover(); rec != nil {
			k.s.log.Error("a keep-warm ping failed inside Trove; no more pings for that session")
			k.mu.Lock()
			k.dropLocked(sl)
			k.mu.Unlock()
		}
	}()
	k.mu.Lock()
	if k.slots[sl.key] != sl || k.stopped {
		k.mu.Unlock()
		return
	}
	now := k.s.opts.Now()
	limit := k.s.opts.Config.Gateway.KeepWarmDailyCapUSD
	switch {
	case k.state() != config.StateOn:
		k.dropLocked(sl)
		k.mu.Unlock()
		return
	case k.ledger.spentToday(now) >= limit:
		k.dropLocked(sl)
		k.warnOnceLocked("keep-warm pings stopped for today: they reached the daily cap (gateway.keep_warm_daily_cap_usd)")
		k.mu.Unlock()
		return
	case k.ledger.tripped(now):
		k.dropLocked(sl)
		k.warnOnceLocked("keep-warm pings stopped: over the last 24 hours they cost more than they are estimated to have saved")
		k.mu.Unlock()
		return
	}
	rt, path, query, body := sl.rt, sl.path, sl.query, sl.body
	cred := sl.cred.Clone()
	k.mu.Unlock()

	row, ok := k.send(sl.sessionKey, rt, path, query, cred, body, now)
	k.s.opts.Recorder.Enqueue(row)

	k.mu.Lock()
	defer k.mu.Unlock()
	if row.CostUSD != nil {
		k.ledger.ping(now, *row.CostUSD)
		k.s.spend.note(*row.CostUSD)
	}
	if k.slots[sl.key] != sl {
		return // a real request replaced the slot while the ping was out
	}
	sl.pings++
	sl.start = now
	if !ok || row.CostUSD == nil || sl.pings >= k.s.opts.Config.Gateway.KeepWarmMaxPings {
		// A failed or unpriced ping is not retried: an unpriced one cannot be held to the cap.
		k.dropLocked(sl)
		return
	}
	k.armLocked(sl, k.s.opts.Now())
}

func (k *keepWarm) warnOnceLocked(msg string) {
	if !k.warned {
		k.warned = true
		k.s.log.Warn(msg)
	}
}

// send makes one ping and returns its usage row. It follows no redirects and reads at most 1 MB.
func (k *keepWarm) send(sessionKey string, rt *route, path, query string, cred http.Header, body []byte, start time.Time) (store.Request, bool) {
	f := &flight{provider: rt.dialect, rt: rt, path: path, usagePath: "/v1/messages", start: start,
		reqHeader: http.Header{"User-Agent": {k.userAgent()}}, reqBody: body, sessionKey: sessionKey, keyed: true}
	ctx, cancel := context.WithTimeout(context.Background(), warmPingTimeout)
	defer cancel()
	u := *rt.upstream
	u.Path = strings.TrimSuffix(u.Path, "/") + path
	u.RawPath = ""
	u.RawQuery = query
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return k.failedRow(f, "keep_warm_build"), false
	}
	for _, h := range credHeaders {
		if v := cred.Values(h); len(v) > 0 {
			req.Header[h] = v
		}
	}
	req.Header.Set("User-Agent", k.userAgent())
	client := &http.Client{Transport: k.s.transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return k.failedRow(f, "keep_warm_unreachable"), false
	}
	defer func() { _ = resp.Body.Close() }()
	buf, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	end := k.s.opts.Now()
	var res usage.Result
	if x := usage.New(rt.dialect, "/v1/messages", false); x != nil {
		res = extract(x, buf, strings.ToLower(resp.Header.Get("Content-Encoding")), true)
	}
	row := k.s.buildRow(f, res, resp.StatusCode, false, time.Time{}, end)
	k.label(&row)
	return row, resp.StatusCode < 400
}

func (k *keepWarm) failedRow(f *flight, class string) store.Request {
	row := k.s.buildRow(f, usage.Result{}, 0, false, time.Time{}, k.s.opts.Now())
	row.ErrorClass = class
	k.label(&row)
	return row
}

func (k *keepWarm) label(row *store.Request) {
	row.Agent, row.AgentVersion = config.OptimizerKeepWarm, clip(k.s.opts.Version, 40)
	row.OptimizationsApplied = optimize.AppliedJSON([]optimize.Report{{Name: config.OptimizerKeepWarm, State: config.StateOn}}, nil)
}

func (k *keepWarm) userAgent() string {
	v := k.s.opts.Version
	if v == "" {
		v = "dev"
	}
	return "trove-keep-warm/" + v
}

// held is how many sessions have a pending ping (tests).
func (k *keepWarm) held() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.slots)
}

// stop cancels every pending ping and drops every held key and prompt. The gateway calls it when it stops.
func (k *keepWarm) stop() {
	if k == nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.stopped = true
	for _, sl := range k.slots {
		k.dropLocked(sl)
	}
	k.shadow = map[string]warmShadow{}
}
