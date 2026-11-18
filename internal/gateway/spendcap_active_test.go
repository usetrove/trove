package gateway

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/gateway/agent"
)

// capRigOn is a rig with spend_cap "on", the given verified agents, and a warning sink.
func capRigOn(t *testing.T, up *capUpstream, l *ledgerOf, budget config.Budget, verified []string, warns *warnSink) *rig {
	t.Helper()
	engine := engineWith(map[string]string{config.OptimizerSpendCap: "on"})
	return newRigWith(t, up.handler(), func(o *Options) {
		engine(o)
		o.Config.Budget = budget
		o.SpendLedger = l.fn
		o.CapRefuses = func(a string) bool {
			for _, v := range verified {
				if v == a {
					return true
				}
			}
			return false
		}
		if warns != nil {
			o.OnSpendWarning = warns.add
		}
	})
}

type warnSink struct {
	mu  sync.Mutex
	got []SpendWarning
}

func (w *warnSink) add(x SpendWarning) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.got = append(w.got, x)
}

func (w *warnSink) all() []SpendWarning {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]SpendWarning{}, w.got...)
}

// Plan 8.1: past the cap, an agent whose refused-request probe passed is refused with a
// plain-English error in the provider's own error shape, and nothing reaches the provider.
func TestSpendCapOnRefusesAVerifiedAgentPastTheCap(t *testing.T) {
	l := newLedgerOf(100)
	up := &capUpstream{}
	g := capRigOn(t, up, l, dailyCap(10), []string{agent.ClaudeCode}, nil)
	seeded(t, g, l)
	before := up.calls.Load()
	statuses, bodies := sendN(t, g, 2)
	for i, s := range statuses {
		if s != 402 {
			t.Fatalf("request %d: status %d, want 402", i, s)
		}
		var e struct {
			Type  string `json:"type"`
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(bodies[i], &e); err != nil || e.Type != "error" || e.Error.Type != "billing_error" {
			t.Fatalf("body %q is not an Anthropic error (%v)", bodies[i], err)
		}
		for _, want := range []string{"Trove spend cap", "$10.00", "trove budget set", "spend_cap"} {
			if !strings.Contains(e.Error.Message, want) {
				t.Errorf("message %q lacks %q", e.Error.Message, want)
			}
		}
	}
	if up.calls.Load() != before {
		t.Errorf("a refused request reached the provider")
	}
	assertNoSecrets(t, g)
}

// Until a probe has shown how an agent handles a refusal, "on" only warns and marks.
func TestSpendCapOnNeverRefusesAnUnverifiedAgent(t *testing.T) {
	l := newLedgerOf(100)
	up := &capUpstream{}
	g := capRigOn(t, up, l, dailyCap(10), []string{agent.Codex}, nil)
	seeded(t, g, l)
	statuses, _ := sendN(t, g, 2)
	for _, s := range statuses {
		if s != 200 {
			t.Fatalf("an unverified agent was refused with %d", s)
		}
	}
	if _, flagged := flaggedCap(g); len(flagged) != 2 {
		t.Errorf("want 2 marked, got %d", len(flagged))
	}
}

// Under the cap, and with a stale or unread ledger, nothing is refused.
func TestSpendCapOnFailsOpenWhenUnsure(t *testing.T) {
	l := newLedgerOf(1)
	up := &capUpstream{}
	g := capRigOn(t, up, l, dailyCap(10), []string{agent.ClaudeCode}, nil)
	seeded(t, g, l)
	if statuses, _ := sendN(t, g, 2); statuses[0] != 200 || statuses[1] != 200 {
		t.Errorf("refused under the cap: %v", statuses)
	}

	// A ledger that has never been read: the first requests go through.
	l2 := newLedgerOf(100)
	g2 := capRigOn(t, &capUpstream{}, l2, dailyCap(10), []string{agent.ClaudeCode}, nil)
	if statuses, _ := sendN(t, g2, 1); statuses[0] != 200 {
		t.Errorf("refused before the ledger was read: %v", statuses)
	}

	c := newSpendCap(dailyCap(10), l.fn, time.Now)
	c.seeded, c.spent, c.from, c.to = true, 100, c.now().Add(-1), c.now().Add(spendStaleAfter*10)
	c.seedAt = c.now().Add(-spendStaleAfter - 1)
	if c.refusable() {
		t.Error("a ledger figure older than spendStaleAfter must not refuse")
	}
	c.seedAt = c.now()
	if !c.refusable() {
		t.Error("a fresh figure past the cap must be refusable")
	}
}

// Warn at warn_at_percent, then at the cap: once each per period.
func TestSpendCapOnWarnsAt80PercentThenAtTheCap(t *testing.T) {
	l := newLedgerOf(0.0075)
	up := &capUpstream{}
	w := &warnSink{}
	g := capRigOn(t, up, l, dailyCap(0.02), nil, w) // warn at $0.016
	seeded(t, g, l)
	sendN(t, g, 4) // totals on arrival: .0075, .0125, .0175, .0225
	got := w.all()
	if len(got) != 2 || got[0].Level != WarnNear || got[1].Level != WarnReached {
		t.Fatalf("warnings = %+v, want near then reached", got)
	}
	if got[0].CapUSD != 0.02 || got[0].Percent < 80 || got[1].Percent < 100 {
		t.Errorf("warnings = %+v", got)
	}
	if !strings.Contains(got[0].Text, "estimated") || !strings.Contains(got[1].Text, "trove budget set") {
		t.Errorf("warning texts: %q / %q", got[0].Text, got[1].Text)
	}
	sendN(t, g, 1)
	if len(w.all()) != 2 {
		t.Errorf("a warning repeated within the period: %+v", w.all())
	}
}

// The OpenAI route answers in OpenAI's error shape: a spend-limit 429 that is not a rate limit.
func TestSpendCapRefusalShapePerProvider(t *testing.T) {
	st, body := capRefusal("openai", "$12.00", "$10.00", "today")
	var e struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if st != 429 || json.Unmarshal(body, &e) != nil || e.Error.Type != "insufficient_quota" || e.Error.Code != "trove_spend_cap_reached" {
		t.Errorf("openai refusal %d %s", st, body)
	}
	if st, _ := capRefusal("anthropic", "$12.00", "$10.00", "today"); st != 402 {
		t.Errorf("anthropic refusal status %d", st)
	}
}
