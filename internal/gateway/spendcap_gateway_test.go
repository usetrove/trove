package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/analyze"
	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/store"
)

// One request on claude-opus-5-5 with the usage in dedupReply costs 1000 uncached input tokens at
// $4, 3000 cache reads at $0.20 and 20 output tokens at $20 per million: $0.005.
const requestCostUSD = 0.005

var (
	capBody    = []byte(`{"model":"claude-opus-5-5","max_tokens":64,"messages":[{"role":"user","content":"CAPSENTINEL %d"}]}`)
	warmupBody = []byte(`{"warmup":true}`)
)

// capUpstream answers a warm-up request with an error, which costs nothing, and every other
// request with a priced reply. It counts the calls.
type capUpstream struct{ calls atomic.Int64 }

func (u *capUpstream) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		u.calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(b), "warmup") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"warm-up"}}`)
			return
		}
		_, _ = io.WriteString(w, dedupReply)
	})
}

// ledgerOf is a ledger that reports a fixed spend and signals when it was first read.
type ledgerOf struct {
	usd  float64
	read chan struct{}
	once sync.Once
}

func newLedgerOf(usd float64) *ledgerOf { return &ledgerOf{usd: usd, read: make(chan struct{})} }

func (l *ledgerOf) fn(_ context.Context, _, _ time.Time) (float64, error) {
	l.once.Do(func() { close(l.read) })
	return l.usd, nil
}

func capRig(t *testing.T, up http.Handler, l *ledgerOf, budget config.Budget, states map[string]string) *rig {
	t.Helper()
	engine := engineWith(states)
	return newRigWith(t, up, func(o *Options) {
		engine(o)
		o.Config.Budget = budget
		if l != nil {
			o.SpendLedger = l.fn
		}
	})
}

// seeded sends a warm-up request, which starts the ledger read and costs nothing, and waits until
// the answer has been taken in.
func seeded(t *testing.T, g *rig, l *ledgerOf) {
	t.Helper()
	finish(t, post(msgURL(g), anthropicHdr, warmupBody))
	g.rec.wait(t)
	select {
	case <-l.read:
	case <-time.After(3 * time.Second):
		t.Fatal("the ledger was never read")
	}
	time.Sleep(100 * time.Millisecond)
}

func sendN(t *testing.T, g *rig, n int) (statuses []int, bodies [][]byte) {
	t.Helper()
	for i := 0; i < n; i++ {
		body := []byte(strings.Replace(string(capBody), "%d", string(rune('a'+i)), 1))
		r := finish(t, post(msgURL(g), anthropicHdr, body))[0]
		g.rec.wait(t)
		statuses, bodies = append(statuses, r.status), append(bodies, r.body)
	}
	return statuses, bodies
}

func flaggedCap(g *rig) (all, flagged []store.Request) {
	g.rec.mu.Lock()
	defer g.rec.mu.Unlock()
	all = append(all, g.rec.rows...)
	for _, r := range all {
		var sh analyze.Shadow
		if len(r.ShadowSavings) > 0 && json.Unmarshal(r.ShadowSavings, &sh) == nil {
			if _, ok := sh.Optimizers[config.OptimizerSpendCap]; ok {
				flagged = append(flagged, r)
			}
		}
	}
	return all, flagged
}

var dailyCap = func(usd float64) config.Budget {
	return config.Budget{Period: "daily", CapUSD: usd, WarnAtPercent: 80}
}

// The cap is already spent: later requests are marked with what they cost, and every one of them
// still goes to the provider and is answered. A shadow cap refuses nothing.
func TestSpendCapShadowMarksRequestsPastTheCapAndRefusesNothing(t *testing.T) {
	l := newLedgerOf(100)
	up := &capUpstream{}
	g := capRig(t, up.handler(), l, dailyCap(10), map[string]string{config.OptimizerSpendCap: "shadow"})
	seeded(t, g, l)
	statuses, bodies := sendN(t, g, 3)

	for i := range statuses {
		if statuses[i] != 200 || string(bodies[i]) != dedupReply {
			t.Errorf("request %d got %d %q; a shadow cap must not refuse or change anything", i, statuses[i], bodies[i])
		}
	}
	if up.calls.Load() != 4 { // the warm-up and three requests
		t.Errorf("the provider saw %d calls, want 4", up.calls.Load())
	}
	all, flagged := flaggedCap(g)
	if len(flagged) != 3 {
		t.Fatalf("want the 3 costly requests marked (not the warm-up), got %d of %d", len(flagged), len(all))
	}
	var sh analyze.Shadow
	_ = json.Unmarshal(flagged[0].ShadowSavings, &sh)
	e := sh.Optimizers[config.OptimizerSpendCap]
	if sh.Label != "estimated" || e.Tokens != 4020 || e.USD == nil || *e.USD != *flagged[0].CostUSD || *e.USD < requestCostUSD-1e-9 || *e.USD > requestCostUSD+1e-9 {
		t.Errorf("shadow entry %+v (label %q): want the request's 4020 tokens and its $%.3f cost", e, sh.Label, requestCostUSD)
	}
	for _, r := range all {
		if r.TokensRemoved != 0 {
			t.Errorf("shadow mode counted %d tokens as removed", r.TokensRemoved)
		}
	}
	assertNoSecrets(t, g)
}

func TestSpendCapShadowLeavesRequestsUnderTheCapAlone(t *testing.T) {
	l := newLedgerOf(1)
	up := &capUpstream{}
	g := capRig(t, up.handler(), l, dailyCap(10), map[string]string{config.OptimizerSpendCap: "shadow"})
	seeded(t, g, l)
	sendN(t, g, 3)
	if _, flagged := flaggedCap(g); len(flagged) != 0 {
		t.Errorf("%d requests marked although 1.02 of 10.00 was spent", len(flagged))
	}
}

// The gateway adds what it records to the ledger's figure, so a request that crosses the cap is
// noticed without waiting for the next ledger read. Only requests after the crossing are marked.
func TestSpendCapShadowFollowsSpendingAsItHappens(t *testing.T) {
	l := newLedgerOf(0)
	up := &capUpstream{}
	g := capRig(t, up.handler(), l, dailyCap(0.012), map[string]string{config.OptimizerSpendCap: "shadow"})
	seeded(t, g, l)
	sendN(t, g, 4) // totals on arrival: 0, .005, .010, .015
	all, flagged := flaggedCap(g)
	if len(flagged) != 1 {
		t.Fatalf("want only the 4th costly request marked, got %d of %d", len(flagged), len(all))
	}
	if got := all[len(all)-1]; string(got.ShadowSavings) != string(flagged[0].ShadowSavings) {
		t.Errorf("the marked request is not the last one")
	}
}

func TestSpendCapDoesNothingWhenOffOrWithoutABudgetOrALedger(t *testing.T) {
	cases := map[string]struct {
		states map[string]string
		budget config.Budget
		ledger bool
	}{
		"state off":      {map[string]string{config.OptimizerSpendCap: "off"}, dailyCap(10), true},
		"state unset":    {nil, dailyCap(10), true},
		"no budget cap":  {map[string]string{config.OptimizerSpendCap: "shadow"}, dailyCap(0), true},
		"no ledger":      {map[string]string{config.OptimizerSpendCap: "shadow"}, dailyCap(10), false},
		"another one on": {map[string]string{config.OptimizerInflightDedup: "shadow"}, dailyCap(10), true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			l := newLedgerOf(100)
			up := &capUpstream{}
			var lg *ledgerOf
			if c.ledger {
				lg = l
			}
			g := capRig(t, up.handler(), lg, c.budget, c.states)
			finish(t, post(msgURL(g), anthropicHdr, warmupBody))
			g.rec.wait(t)
			time.Sleep(100 * time.Millisecond)
			sendN(t, g, 2)
			if _, flagged := flaggedCap(g); len(flagged) != 0 {
				t.Errorf("%s: %d requests marked", name, len(flagged))
			}
		})
	}
}

// "on" does what shadow does: there is no active cap yet.
func TestSpendCapOnBehavesAsShadow(t *testing.T) {
	l := newLedgerOf(100)
	up := &capUpstream{}
	g := capRig(t, up.handler(), l, dailyCap(10), map[string]string{config.OptimizerSpendCap: "on"})
	seeded(t, g, l)
	statuses, _ := sendN(t, g, 2)
	for _, s := range statuses {
		if s != 200 {
			t.Fatalf("a request was refused with %d; the cap has no active mode", s)
		}
	}
	if _, flagged := flaggedCap(g); len(flagged) != 2 {
		t.Errorf("want 2 marked, got %d", len(flagged))
	}
}

// A row can carry both behaviors' estimates.
func TestGatewayShadowEntriesMergeOnOneRow(t *testing.T) {
	cost := 0.005
	row := store.Request{InputUncachedTokens: 1000, CacheReadTokens: 3000, OutputTokens: 20, CostUSD: &cost,
		ShadowSavings: []byte(`{"label":"estimated","optimizers":{"duplicate_result_collapser":{"tokens":500,"usd":0.001}}}`)}
	s := &Server{}
	s.addGatewayShadow(&row, config.OptimizerInflightDedup)
	s.addGatewayShadow(&row, config.OptimizerSpendCap)
	var sh analyze.Shadow
	if err := json.Unmarshal(row.ShadowSavings, &sh); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"duplicate_result_collapser", config.OptimizerInflightDedup, config.OptimizerSpendCap} {
		if _, ok := sh.Optimizers[n]; !ok {
			t.Errorf("the row lost the %s entry: %s", n, row.ShadowSavings)
		}
	}
	if sh.Optimizers["duplicate_result_collapser"].Tokens != 500 {
		t.Error("an existing entry was changed")
	}
	// A request with no usage has nothing to say.
	empty := store.Request{}
	s.addGatewayShadow(&empty, config.OptimizerSpendCap)
	if len(empty.ShadowSavings) != 0 {
		t.Errorf("a request with no billed tokens got a shadow entry: %s", empty.ShadowSavings)
	}
}

func TestSpendCapKeepsKeysAndPromptsOutOfRowsAndLogs(t *testing.T) {
	l := newLedgerOf(100)
	up := &capUpstream{}
	g := capRig(t, up.handler(), l, dailyCap(10), map[string]string{config.OptimizerSpendCap: "shadow"})
	seeded(t, g, l)
	sendN(t, g, 2)
	all, _ := flaggedCap(g)
	for _, r := range all {
		raw, _ := json.Marshal(r)
		for _, secret := range []string{fakeKey, "CAPSENTINEL"} {
			if strings.Contains(string(raw), secret) {
				t.Errorf("%q reached a stored row: %s", secret, raw)
			}
		}
	}
	assertNoSecrets(t, g)
	if strings.Contains(g.logs.String(), "CAPSENTINEL") {
		t.Errorf("prompt text reached the logs:\n%s", g.logs.String())
	}
}
