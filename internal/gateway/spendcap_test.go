package gateway

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
)

// fakeLedger answers from a function of the period and counts how often it is read.
type fakeLedger struct {
	calls atomic.Int64
	mu    sync.Mutex
	fn    func(from, to time.Time) (float64, error)
	block chan struct{} // when set, a read waits for it to close
}

func (l *fakeLedger) read(ctx context.Context, from, to time.Time) (float64, error) {
	l.calls.Add(1)
	l.mu.Lock()
	fn, block := l.fn, l.block
	l.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	return fn(from, to)
}

func (l *fakeLedger) set(fn func(from, to time.Time) (float64, error)) {
	l.mu.Lock()
	l.fn = fn
	l.mu.Unlock()
}

func constLedger(usd float64) *fakeLedger {
	l := &fakeLedger{}
	l.fn = func(_, _ time.Time) (float64, error) { return usd, nil }
	return l
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

var capDay = time.Date(2026, 11, 13, 14, 0, 0, 0, time.UTC)

func newCap(cap float64, period string, l *fakeLedger, c *clock) *spendCap {
	return newSpendCap(config.Budget{Period: period, CapUSD: cap, WarnAtPercent: 80}, l.read, c.now)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestSpendCapIsOffWithoutACapOrALedger(t *testing.T) {
	c := &clock{t: capDay}
	l := constLedger(999)
	if newCap(0, "daily", l, c).over() {
		t.Error("no cap set, yet it reported over")
	}
	if l.calls.Load() != 0 {
		t.Error("with no cap it read the ledger")
	}
	var none *spendCap
	if none.over() {
		t.Error("a nil cap reported over")
	}
	none.note(5) // must not panic
	if spendFor(Options{Config: config.Default()}) != nil {
		t.Error("a server with no ledger got a spend cap")
	}
	cfg := config.Default()
	cfg.Budget.CapUSD = 5
	if spendFor(Options{Config: cfg}) != nil {
		t.Error("a server with a cap but no ledger got a spend cap")
	}
}

// Until the ledger has been read nothing is marked, and the read never holds up the request.
func TestSpendCapNeverWaitsForTheLedger(t *testing.T) {
	c := &clock{t: capDay}
	l := constLedger(100)
	l.block = make(chan struct{})
	cp := newCap(10, "daily", l, c)
	start := time.Now()
	if cp.over() {
		t.Fatal("reported over before the ledger was read")
	}
	if time.Since(start) > 200*time.Millisecond {
		t.Fatal("over() waited for the ledger")
	}
	close(l.block)
	waitFor(t, "the ledger to be read", func() bool { return cp.over() })
}

func TestSpendCapComparesTheLedgerWithTheCapAndCountsNewSpending(t *testing.T) {
	c := &clock{t: capDay}
	l := constLedger(7.5)
	cp := newCap(10, "daily", l, c)
	cp.over()
	waitFor(t, "seeding", func() bool { cp.mu.Lock(); defer cp.mu.Unlock(); return cp.seeded })
	if cp.over() {
		t.Fatal("7.50 of 10.00 reported over")
	}
	cp.note(2.0)
	if cp.over() {
		t.Fatal("9.50 of 10.00 reported over")
	}
	cp.note(0.5)
	if !cp.over() {
		t.Fatal("exactly the cap must count as reached, as the report's CapReached does")
	}
	cp.note(-3) // a refund or a bad value never lowers the total
	cp.note(0)
	if !cp.over() {
		t.Error("a non-positive cost changed the total")
	}
}

func TestSpendCapAsksTheLedgerForTheBudgetPeriod(t *testing.T) {
	for period, want := range map[string][2]time.Time{
		"daily":   {time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)},
		"weekly":  {time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)}, // Monday
		"monthly": {time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)},
	} {
		c := &clock{t: capDay}
		var got [2]time.Time
		var mu sync.Mutex
		l := &fakeLedger{fn: func(from, to time.Time) (float64, error) {
			mu.Lock()
			got = [2]time.Time{from, to}
			mu.Unlock()
			return 0, nil
		}}
		cp := newCap(10, period, l, c)
		cp.over()
		waitFor(t, "a read", func() bool { return l.calls.Load() == 1 })
		waitFor(t, "the answer", func() bool { cp.mu.Lock(); defer cp.mu.Unlock(); return cp.seeded })
		mu.Lock()
		if !got[0].Equal(want[0]) || !got[1].Equal(want[1]) {
			t.Errorf("%s: read %v to %v, want %v to %v", period, got[0], got[1], want[0], want[1])
		}
		mu.Unlock()
	}
}

// A new period starts at zero at once, whatever the ledger said before.
func TestSpendCapStartsAgainWhenThePeriodRollsOver(t *testing.T) {
	c := &clock{t: time.Date(2026, 11, 13, 23, 50, 0, 0, time.UTC)}
	l := &fakeLedger{fn: func(from, _ time.Time) (float64, error) {
		if from.Day() == 13 {
			return 50, nil
		}
		return 0.25, nil
	}}
	cp := newCap(10, "daily", l, c)
	cp.over()
	waitFor(t, "seeding", func() bool { cp.mu.Lock(); defer cp.mu.Unlock(); return cp.seeded })
	if !cp.over() {
		t.Fatal("50 of 10 on the 13th must be over")
	}
	c.advance(20 * time.Minute) // past midnight
	if cp.over() {
		t.Fatal("the new day began over the cap")
	}
	waitFor(t, "the new day's ledger read", func() bool { cp.mu.Lock(); defer cp.mu.Unlock(); return cp.spent == 0.25 })
	if cp.over() {
		t.Error("0.25 of 10 on the new day reported over")
	}
}

// The running total is corrected from the ledger now and then, so it cannot drift.
func TestSpendCapHealsDriftFromTheLedger(t *testing.T) {
	c := &clock{t: capDay}
	l := constLedger(1)
	cp := newCap(10, "daily", l, c)
	cp.over()
	waitFor(t, "seeding", func() bool { cp.mu.Lock(); defer cp.mu.Unlock(); return cp.seeded })
	cp.note(20) // counted in memory, but the ledger never saw it (a dropped row)
	if !cp.over() {
		t.Fatal("setup: 21 of 10 should be over")
	}
	c.advance(spendReseedEvery + time.Second)
	cp.over() // starts a reread
	waitFor(t, "the reread", func() bool { return l.calls.Load() == 2 })
	waitFor(t, "the total to follow the ledger", func() bool { return !cp.over() })
}

// A broken database must not be hammered on every request.
func TestSpendCapDoesNotRetryABrokenLedgerOnEveryRequest(t *testing.T) {
	c := &clock{t: capDay}
	l := &fakeLedger{fn: func(_, _ time.Time) (float64, error) { return 0, errors.New("database is locked") }}
	cp := newCap(10, "daily", l, c)
	for i := 0; i < 50; i++ {
		if cp.over() {
			t.Fatal("reported over with no ledger figure")
		}
		time.Sleep(time.Millisecond)
	}
	waitFor(t, "the first read", func() bool { return l.calls.Load() >= 1 })
	if n := l.calls.Load(); n != 1 {
		t.Errorf("read the broken ledger %d times in 50 requests", n)
	}
	c.advance(spendRetryEvery + time.Second)
	cp.over()
	waitFor(t, "the retry", func() bool { return l.calls.Load() == 2 })
}

// Spending recorded while the ledger is being read is not lost.
func TestSpendCapKeepsSpendingRecordedDuringAnInFlightRead(t *testing.T) {
	c := &clock{t: capDay}
	l := constLedger(8)
	l.block = make(chan struct{})
	cp := newCap(10, "daily", l, c)
	cp.over() // starts the read, which waits
	cp.note(3)
	close(l.block)
	waitFor(t, "seeding", func() bool { cp.mu.Lock(); defer cp.mu.Unlock(); return cp.seeded })
	if !cp.over() {
		t.Error("8 from the ledger plus 3 recorded during the read is 11 of 10, which is over")
	}
}

func TestSpendCapUnderConcurrency(t *testing.T) {
	c := &clock{t: capDay}
	cp := newCap(1000, "daily", constLedger(0), c)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cp.over()
			cp.note(0.01)
		}()
	}
	wg.Wait()
}
