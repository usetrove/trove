package gateway

import (
	"context"
	"sync"
	"time"

	"github.com/usetrove/trove/internal/config"
)

// Spend cap, shadow mode.
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

// SpendLedger returns what the ledger says was spent in [from, to), in dollars.
type SpendLedger func(ctx context.Context, from, to time.Time) (float64, error)

const (
	spendReseedEvery  = 5 * time.Minute
	spendRetryEvery   = 30 * time.Second // after a failed first read
	spendSeedDeadline = 2 * time.Second
)

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
	defer c.mu.Unlock()
	c.seeding = false
	c.seedAt = c.now() // on failure too, so a broken database is not retried on every request
	if err != nil {
		return
	}
	c.from, c.to = from, to
	c.spent, c.seeded = usd+c.pending, true
	c.pending = 0
}

// note adds the cost of a request that was just recorded.
func (c *spendCap) note(usd float64) {
	if c == nil || usd <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seeding {
		c.pending += usd
	}
	if c.seeded {
		c.spent += usd
	}
}

// spendFor builds the spend cap for a server, or nil when it cannot do anything: no
// ledger to read, or no cap set in the budget.
func spendFor(opts Options) *spendCap {
	if opts.SpendLedger == nil || opts.Config.Budget.CapUSD <= 0 {
		return nil
	}
	return newSpendCap(opts.Config.Budget, opts.SpendLedger, opts.Now)
}
