package store

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Request is one row of request metadata. It deliberately has no field for
// prompt text, code, file paths or tool output.
type Request struct {
	TS                   time.Time
	SessionKey           string // hash, never a raw identifier
	Agent                string
	AgentVersion         string
	Provider             string
	Model                string
	Endpoint             string // route only, no query string
	Streamed             bool
	StatusCode           int
	LatencyMS            int64
	TTFTMS               *int64 // nil when not streamed
	InputUncachedTokens  int64
	CacheWriteTokens     int64
	CacheReadTokens      int64
	OutputTokens         int64
	ReasoningTokens      int64
	CostUSD              *float64 // nil when pricing is unknown
	PricingVersion       string
	SegmentTokenEstimate []byte // JSON
	Findings             []byte // JSON
	OptimizationsApplied []byte // JSON
	ShadowSavings        []byte // JSON
	ErrorClass           string
	UsageRaw             []byte // JSON: the provider's usage object, as reported
	ServiceTier          string // as reported by the provider; empty if none
}

// WriterOptions tunes the Writer. Zero values pick sensible defaults.
type WriterOptions struct {
	BufferSize    int           // queue capacity (default 1024)
	BatchSize     int           // max rows per transaction (default 100)
	FlushInterval time.Duration // max wait before flushing a partial batch (default 250ms)
	Logger        *slog.Logger  // must already redact; nil discards
}

// WriterStats are cumulative counters.
type WriterStats struct {
	Written uint64
	Dropped uint64 // rows discarded because the buffer was full or the writer closed
	Failed  uint64 // rows lost to database errors
}

// Writer persists Requests off the hot path. Enqueue never blocks: if the
// buffer is full the row is dropped and counted.
type Writer struct {
	db   *DB
	opts WriterOptions
	ch   chan Request
	done chan struct{}

	mu     sync.RWMutex
	closed bool

	written, dropped, failed atomic.Uint64

	hold chan struct{} // tests only: the loop waits for this to close before draining
}

// NewWriter starts the background flush loop.
func NewWriter(db *DB, opts WriterOptions) *Writer { return newWriter(db, opts, nil) }

// newWriter is NewWriter with an optional hold channel that keeps the loop
// idle until closed, so tests can fill the buffer deterministically.
func newWriter(db *DB, opts WriterOptions, hold chan struct{}) *Writer {
	if opts.BufferSize <= 0 {
		opts.BufferSize = 1024
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 100
	}
	if opts.FlushInterval <= 0 {
		opts.FlushInterval = 250 * time.Millisecond
	}
	w := &Writer{db: db, opts: opts, ch: make(chan Request, opts.BufferSize), done: make(chan struct{}), hold: hold}
	go w.loop()
	return w
}

// Enqueue queues r without ever blocking. It reports whether the row was accepted.
func (w *Writer) Enqueue(r Request) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.closed {
		w.dropped.Add(1)
		return false
	}
	select {
	case w.ch <- r:
		return true
	default:
		w.dropped.Add(1)
		return false
	}
}

// Stats returns the current counters.
func (w *Writer) Stats() WriterStats {
	return WriterStats{Written: w.written.Load(), Dropped: w.dropped.Load(), Failed: w.failed.Load()}
}

// Close stops accepting rows, flushes what is queued and waits for the loop,
// or until ctx ends.
func (w *Writer) Close(ctx context.Context) error {
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		close(w.ch)
	}
	w.mu.Unlock()
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *Writer) loop() {
	defer close(w.done)
	if w.hold != nil {
		<-w.hold
	}
	batch := make([]Request, 0, w.opts.BatchSize)
	tick := time.NewTicker(w.opts.FlushInterval)
	defer tick.Stop()
	flush := func() {
		if len(batch) > 0 {
			w.flush(batch)
			batch = batch[:0]
		}
	}
	for {
		select {
		case r, ok := <-w.ch:
			if !ok {
				flush()
				return
			}
			batch = append(batch, r)
			if len(batch) >= w.opts.BatchSize {
				flush()
			}
		case <-tick.C:
			flush()
		}
	}
}

func (w *Writer) flush(batch []Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := w.db.insertRequests(ctx, batch); err != nil {
		w.failed.Add(uint64(len(batch)))
		if w.opts.Logger != nil {
			w.opts.Logger.Warn("could not save usage rows; they were skipped and requests are unaffected", "rows", len(batch), "err", err)
		}
		return
	}
	w.written.Add(uint64(len(batch)))
}

const insertRequestSQL = `INSERT INTO requests (
	ts, session_key, agent, agent_version, provider, model, endpoint, streamed, status_code,
	latency_ms, ttft_ms, input_uncached_tokens, cache_write_tokens, cache_read_tokens,
	output_tokens, reasoning_tokens, cost_usd, pricing_version, segment_token_estimates,
	findings, optimizations_applied, shadow_savings, error_class, usage_raw, service_tier
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`

const upsertSessionSQL = `INSERT INTO sessions (
	session_key, agent, started_at, ended_at, request_count,
	total_input_uncached_tokens, total_cache_write_tokens, total_cache_read_tokens,
	total_output_tokens, total_cost_usd
) VALUES (?,?,?,?,1,?,?,?,?,?)
ON CONFLICT(session_key) DO UPDATE SET
	started_at = MIN(started_at, excluded.started_at),
	ended_at = MAX(COALESCE(ended_at, 0), excluded.ended_at),
	request_count = request_count + 1,
	total_input_uncached_tokens = total_input_uncached_tokens + excluded.total_input_uncached_tokens,
	total_cache_write_tokens = total_cache_write_tokens + excluded.total_cache_write_tokens,
	total_cache_read_tokens = total_cache_read_tokens + excluded.total_cache_read_tokens,
	total_output_tokens = total_output_tokens + excluded.total_output_tokens,
	total_cost_usd = total_cost_usd + excluded.total_cost_usd`

func jsonOrNil(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}

// insertRequests stores rows and updates session totals in one transaction.
func (d *DB) insertRequests(ctx context.Context, rows []Request) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	ins, err := tx.PrepareContext(ctx, insertRequestSQL)
	if err != nil {
		return err
	}
	defer func() { _ = ins.Close() }()
	ses, err := tx.PrepareContext(ctx, upsertSessionSQL)
	if err != nil {
		return err
	}
	defer func() { _ = ses.Close() }()

	for _, r := range rows {
		ts := r.TS.UnixMilli()
		var cost any
		var costSum float64
		if r.CostUSD != nil {
			cost, costSum = *r.CostUSD, *r.CostUSD
		}
		var ttft any
		if r.TTFTMS != nil {
			ttft = *r.TTFTMS
		}
		if _, err := ins.ExecContext(ctx, ts, r.SessionKey, r.Agent, r.AgentVersion, r.Provider, r.Model, r.Endpoint,
			r.Streamed, r.StatusCode, r.LatencyMS, ttft, r.InputUncachedTokens, r.CacheWriteTokens, r.CacheReadTokens,
			r.OutputTokens, r.ReasoningTokens, cost, r.PricingVersion, jsonOrNil(r.SegmentTokenEstimate),
			jsonOrNil(r.Findings), jsonOrNil(r.OptimizationsApplied), jsonOrNil(r.ShadowSavings), r.ErrorClass, jsonOrNil(r.UsageRaw), r.ServiceTier); err != nil {
			return err
		}
		if r.SessionKey != "" {
			if _, err := ses.ExecContext(ctx, r.SessionKey, r.Agent, ts, ts, r.InputUncachedTokens,
				r.CacheWriteTokens, r.CacheReadTokens, r.OutputTokens, costSum); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
