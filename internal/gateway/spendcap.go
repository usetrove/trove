package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/usetrove/trove/internal/config"
)

// Spend cap. Shadow mode marks; "on" also warns and, for verified agents, refuses.
//
// The budget (`trove budget set`) has a period (day, week or month) and a cap in
// dollars. A hard cap would refuse requests once the period's spending reaches it.
// This file only finds those requests. When spending in the current period has
// already reached the cap, a request that arrives is marked, and its cost is
// recorded as what a hard cap would have stopped. Nothing is refused: every request
// still goes to the provider and every client gets its answer.
//
// "Spent" is the same figure the report and the status line show: the list-price
// cost of successful requests in the ledger, over the same period (local time, weeks
// from Monday). The ledger is read when the gateway starts, when the period rolls over
// and every few minutes after that, so the figure survives a restart and does not
// drift. Between reads the gateway adds the cost of each request it records, so the
// check is a comparison of two numbers held in memory: it never waits for the
// database, and it cannot slow or fail a request.
//
// Until the ledger has been read once, nothing is marked. A request recorded while a
// read is in progress can be counted twice for a moment; for an estimate that errs
// toward marking slightly early, which is the safe side for a cap.

// Active mode ("on", plan 8.1). It warns once per period when spending reaches the budget's
// warn_at_percent and once when it reaches the cap (OnSpendWarning). Past the cap it refuses a
// request only when all of these hold, and otherwise forwards it as shadow mode would:
//   - the agent that sent it passed the refused-request probe (CapRefuses), so it is known to
//     show the error and stop rather than retry in a loop;
//   - the ledger has been read, and not longer ago than spendStaleAfter;
//   - the request arrived inside the period that figure belongs to.
// A refused request never reaches the provider and gets a plain-English error in the
// provider's own shape (capRefusal). Requests already in flight are never touched.

// SpendLedger returns what the ledger says was spent in [from, to), in dollars.
type SpendLedger func(ctx context.Context, from, to time.Time) (float64, error)

const (
	spendReseedEvery  = 5 * time.Minute
	spendRetryEvery   = 30 * time.Second // after a failed first read
	spendSeedDeadline = 2 * time.Second
	// spendStaleAfter is how old the ledger figure may be for the cap to refuse on it.
	spendStaleAfter = 2 * spendReseedEvery
)

// Spend warning levels.
const (
	WarnNear    = "near"    // spending reached warn_at_percent of the cap
	WarnReached = "reached" // spending reached the cap
)

// SpendWarning is one warning, sent once per level per period. Figures are estimated
// from list prices.
type SpendWarning struct {
	Level    string  `json:"level"`
	SpentUSD float64 `json:"spent_usd"`
	CapUSD   float64 `json:"cap_usd"`
	Percent  float64 `json:"percent"`
	Period   string  `json:"period"`
	Text     string  `json:"text"`
}

type spendCap struct {
	mu     sync.Mutex
	budget config.Budget
	ledger SpendLedger
	now    func() time.Time

	from, to time.Time
	spent    float64
	seeded   bool
	seedAt   time.Time
	seeding  bool
	pending  float64 // cost recorded while a ledger read was in progress

	// warnedNear and warnedReached are set once each per period; onWarn receives the warnings.
	warnedNear, warnedReached bool
	onWarn                    func(SpendWarning)
	active                    func() bool // spend_cap is "on"; warnings are sent only then
}

func newSpendCap(b config.Budget, ledger SpendLedger, now func() time.Time) *spendCap {
	return &spendCap{budget: b, ledger: ledger, now: now}
}

// over reports whether spending in the current budget period has reached the cap.
// It never waits: if the ledger has not been read yet, or the period just rolled
// over, it answers from what it has and starts a read in the background.
func (c *spendCap) over() bool {
	if c == nil || c.budget.CapUSD <= 0 || c.ledger == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.now()
	if c.seeded && !t.Before(c.to) {
		// A new period began: it starts at zero, and the ledger will confirm.
		c.from, c.to = c.budget.Window(t)
		c.spent = 0
		c.warnedNear, c.warnedReached = false, false
	}
	wait := spendReseedEvery
	if !c.seeded {
		wait = spendRetryEvery
	}
	if !c.seeding && (c.seedAt.IsZero() || t.Sub(c.seedAt) >= wait) {
		c.seeding, c.pending = true, 0
		from, to := c.budget.Window(t)
		go c.seed(from, to)
	}
	return c.seeded && c.spent >= c.budget.CapUSD
}

// seed reads the ledger and replaces the running total with it.
func (c *spendCap) seed(from, to time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), spendSeedDeadline)
	defer cancel()
	usd, err := c.ledger(ctx, from, to)
	c.mu.Lock()
	c.seeding = false
	c.seedAt = c.now() // on failure too, so a broken database is not retried on every request
	if err != nil {
		c.mu.Unlock()
		return
	}
	if !c.from.Equal(from) {
		c.warnedNear, c.warnedReached = false, false
	}
	c.from, c.to = from, to
	c.spent, c.seeded = usd+c.pending, true
	c.pending = 0
	ws := c.crossedLocked()
	c.mu.Unlock()
	c.send(ws)
}

// note adds the cost of a request that was just recorded.
func (c *spendCap) note(usd float64) {
	if c == nil || usd <= 0 {
		return
	}
	c.mu.Lock()
	if c.seeding {
		c.pending += usd
	}
	if c.seeded {
		c.spent += usd
	}
	ws := c.crossedLocked()
	c.mu.Unlock()
	c.send(ws)
}

// crossedLocked returns the warnings spending has newly earned this period. c.mu is held.
func (c *spendCap) crossedLocked() []SpendWarning {
	if c.onWarn == nil || !c.seeded || c.budget.CapUSD <= 0 || c.active == nil || !c.active() {
		return nil
	}
	pct := 100 * c.spent / c.budget.CapUSD
	mk := func(level, text string) SpendWarning {
		return SpendWarning{Level: level, SpentUSD: c.spent, CapUSD: c.budget.CapUSD, Percent: pct, Period: c.budget.Period, Text: text}
	}
	spent, capUSD, when := dollars(c.spent), dollars(c.budget.CapUSD), periodWords(c.budget.Period)
	var out []SpendWarning
	if !c.warnedNear && pct >= float64(c.budget.WarnAtPercent) && pct < 100 {
		c.warnedNear = true
		out = append(out, mk(WarnNear, fmt.Sprintf("Trove: ~%s of your %s budget spent %s (%.0f%%, estimated at list prices).", spent, capUSD, when, pct)))
	}
	if !c.warnedReached && pct >= 100 {
		c.warnedNear, c.warnedReached = true, true
		out = append(out, mk(WarnReached, fmt.Sprintf("Trove: your %s budget is spent %s (~%s, estimated). To keep going, raise it with `trove budget set <amount>`.", capUSD, when, spent)))
	}
	return out
}

// send hands warnings to onWarn outside the lock. A broken notifier never affects a request.
func (c *spendCap) send(ws []SpendWarning) {
	for _, w := range ws {
		func() {
			defer func() { _ = recover() }()
			c.onWarn(w)
		}()
	}
}

// refusable reports whether a hard cap may refuse a request now: the ledger figure is
// fresh, it belongs to the current period, and it has reached the cap.
func (c *spendCap) refusable() bool {
	if c == nil || c.budget.CapUSD <= 0 {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.now()
	return c.seeded && t.Sub(c.seedAt) <= spendStaleAfter && !t.Before(c.from) && t.Before(c.to) && c.spent >= c.budget.CapUSD
}

// figures returns the spent and cap amounts and the period in words, for the refusal message.
func (c *spendCap) figures() (spent, capUSD, when string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return dollars(c.spent), dollars(c.budget.CapUSD), periodWords(c.budget.Period)
}

func dollars(v float64) string { return fmt.Sprintf("$%.2f", v) }

func periodWords(p string) string {
	switch p {
	case "daily":
		return "today"
	case "weekly":
		return "this week"
	}
	return "this month"
}

// capRefusal is the response to a refused request, in the provider's error shape. Anthropic's
// documented 402 billing_error is not a transient error; OpenAI documents spend limits as 429
// with type insufficient_quota and says retrying will not help (both checked 2026-11-18 on
// platform.claude.com/docs/en/api/errors and developers.openai.com/api/docs/guides/error-codes).
// How each agent reacts is not documented; the refused-request probe checks it.
func capRefusal(shape, spent, capUSD, when string) (int, []byte) {
	msg := fmt.Sprintf("Trove spend cap reached: about %s of your %s budget is spent %s (estimated at list prices). "+
		"Trove did not send this request to the provider. To continue, raise the cap with `trove budget set <amount>`, "+
		"or set spend_cap = \"shadow\" under [optimizers] in your Trove config.toml to stop refusing.", spent, capUSD, when)
	var v any
	code := http.StatusPaymentRequired
	if shape == "anthropic" {
		v = map[string]any{"type": "error", "error": map[string]string{"type": "billing_error", "message": msg}}
	} else {
		code = http.StatusTooManyRequests
		v = map[string]any{"error": map[string]any{"message": msg, "type": "insufficient_quota", "param": nil, "code": "trove_spend_cap_reached"}}
	}
	b, _ := json.Marshal(v)
	return code, b
}

// spendFor builds the spend cap for a server, or nil when it cannot do anything: no
// ledger to read, or no cap set in the budget.
func spendFor(opts Options) *spendCap {
	if opts.SpendLedger == nil || opts.Config.Budget.CapUSD <= 0 {
		return nil
	}
	c := newSpendCap(opts.Config.Budget, opts.SpendLedger, opts.Now)
	c.onWarn = opts.OnSpendWarning
	c.active = func() bool { return opts.Optimizer.GatewayState(config.OptimizerSpendCap) == config.StateOn }
	return c
}
