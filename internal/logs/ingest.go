package logs

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/store"
)

// ParserVersions maps a source name to the version of its parser. A file is
// re-read from the start when the version stored for it differs.
type ParserVersions map[string]int

// Annotator adds findings to rows before they are stored. It sees every event
// of one file pass, in order. It must not keep or return text.
type Annotator func(events []Event, rows []Row)

// Row is one history_requests row.
type Row struct {
	Event
	CostUSD       *float64
	PricingStatus string
	Plan          Plan
	Findings      json.RawMessage // nil when none
	ShadowSavings json.RawMessage
}

// Options controls one scan.
type Options struct {
	Since    time.Time // files last written before this are skipped; zero means all
	Roots    []string  // override the default history locations
	Progress func(done, total int)
}

// Summary says what a scan did.
type Summary struct {
	Files         int   `json:"files"`      // history files found in range
	Read          int   `json:"files_read"` // files with new data
	Unchanged     int   `json:"files_unchanged"`
	Events        int64 `json:"requests"`       // requests found in this scan
	SkippedLines  int64 `json:"skipped_lines"`  // lines that could not be read
	UnknownFormat int   `json:"unknown_format"` // files from an agent version the parser was not written for
	Errors        int   `json:"errors"`         // files that could not be opened
	DurationMS    int64 `json:"duration_ms"`
}

// Ingester scans history into the database.
type Ingester struct {
	DB       *store.DB
	Sources  []Source
	Versions ParserVersions
	Pricing  *pricing.Table // nil leaves cost_usd empty
	Plan     func(source string) Plan
	Annotate Annotator
	Workers  int
	Now      func() time.Time
}

type checkpointRow struct {
	size, mtime int64
	cp          Checkpoint
	parser      int
	skipped     int64
}

type result struct {
	file   File
	events []Event
	cp     Checkpoint
	stats  Stats
	err    error
}

// Scan reads new history and stores it. It is safe to run repeatedly: files
// that have not changed are not opened, and rows are keyed by a hash of the
// provider request id, so nothing is counted twice.
func (in *Ingester) Scan(ctx context.Context, opt Options) (Summary, error) {
	start := time.Now()
	var sum Summary

	// The plan can change between scans (config or environment), so rows already
	// stored are restamped with the current answer.
	if in.Plan != nil {
		for _, s := range in.Sources {
			p := string(in.Plan(s.Name()))
			if _, err := in.DB.ExecContext(ctx, `UPDATE history_requests SET plan = ? WHERE source = ? AND plan <> ?`, p, s.Name(), p); err != nil {
				return sum, err
			}
		}
	}

	var files []File
	for _, s := range in.Sources {
		fs, err := s.Discover(ctx, opt.Roots)
		if err != nil {
			return sum, err
		}
		files = append(files, fs...)
	}
	cps, err := in.loadCheckpoints(ctx)
	if err != nil {
		return sum, err
	}

	type job struct {
		src  Source
		file File
		cp   Checkpoint
	}
	var jobs []job
	for _, f := range files {
		if !opt.Since.IsZero() && f.MTime < opt.Since.UnixMilli() {
			continue
		}
		sum.Files++
		src := in.source(f.Source)
		if src == nil {
			continue
		}
		prev, seen := cps[f.Key]
		pv := in.Versions[f.Source]
		if seen && prev.parser == pv && prev.size == f.Size && prev.mtime == f.MTime {
			sum.Unchanged++
			continue
		}
		cp := prev.cp
		if !seen || prev.parser != pv {
			cp = Checkpoint{}
		}
		jobs = append(jobs, job{src, f, cp})
	}

	workers := in.Workers
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	in2 := make(chan job)
	out := make(chan result)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range in2 {
				r := result{file: j.file}
				r.cp, r.stats, r.err = j.src.Parse(ctx, j.file, j.cp, func(e Event) { r.events = append(r.events, e) })
				if r.err != nil {
					r.cp = j.cp
				}
				select {
				case out <- r:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(in2)
		for _, j := range jobs {
			select {
			case in2 <- j:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { wg.Wait(); close(out) }()

	done := 0
	var firstErr error
	for r := range out {
		done++
		if opt.Progress != nil {
			opt.Progress(done, len(jobs))
		}
		if r.err != nil {
			if ctx.Err() == nil {
				sum.Errors++ // unreadable file: counted, not fatal
			}
			continue
		}
		if err := in.store(ctx, r, cps[r.file.Key].skipped); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		sum.Read++
		sum.Events += int64(len(r.events))
		sum.SkippedLines += r.stats.Skipped
		if r.stats.UnknownFormat {
			sum.UnknownFormat++
		}
	}
	sum.DurationMS = time.Since(start).Milliseconds()
	if ctx.Err() != nil {
		return sum, ctx.Err()
	}
	return sum, firstErr
}

func (in *Ingester) source(name string) Source {
	for _, s := range in.Sources {
		if s.Name() == name {
			return s
		}
	}
	return nil
}

func (in *Ingester) loadCheckpoints(ctx context.Context) (map[string]checkpointRow, error) {
	rows, err := in.DB.QueryContext(ctx, `SELECT file_key, size, mtime, offset, lines, turns, parser_version, skipped_lines FROM history_files`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]checkpointRow{}
	for rows.Next() {
		var k string
		var c checkpointRow
		if err := rows.Scan(&k, &c.size, &c.mtime, &c.cp.Offset, &c.cp.Lines, &c.cp.Turns, &c.parser, &c.skipped); err != nil {
			return nil, err
		}
		out[k] = c
	}
	return out, rows.Err()
}

func providerFor(source string) string {
	if source == SourceCodex {
		return pricing.OpenAI
	}
	return pricing.Anthropic
}

func (in *Ingester) row(e Event) Row {
	r := Row{Event: e, Plan: PlanUnknown}
	if in.Plan != nil {
		r.Plan = in.Plan(e.Source)
	}
	if in.Pricing == nil {
		return r
	}
	if e.CacheWriteNA > 0 { // same rule as the gateway: no TTL split, no price
		r.PricingStatus = "unpriced"
		return r
	}
	tier := strings.ToLower(e.ServiceTier)
	if tier == "standard" || tier == "default" || tier == "auto" {
		tier = ""
	}
	c := in.Pricing.Cost(pricing.Request{
		Provider: providerFor(e.Source), Model: e.Model, Time: time.UnixMilli(e.TS), ServiceTier: tier,
		Usage: pricing.Usage{
			UncachedInput: e.Input,
			CacheWrite:    map[string]int64{"5m": e.CacheWrite5m, "1h": e.CacheWrite1h},
			CacheRead:     e.CacheRead, Output: e.Output,
		},
	})
	r.PricingStatus = string(c.Status)
	if c.Status == pricing.Priced {
		v := c.Total.Float64()
		r.CostUSD = &v
	}
	return r
}

func (in *Ingester) store(ctx context.Context, r result, prevSkipped int64) error {
	rows := make([]Row, len(r.events))
	for i, e := range r.events {
		rows[i] = in.row(e)
	}
	if in.Annotate != nil {
		in.Annotate(r.events, rows)
	}
	tx, err := in.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO history_requests
		(request_key, source, session_key, ts, model, input_tokens, cache_write_5m, cache_write_1h, cache_write_other,
		 cache_read, output_tokens, cost_usd, pricing_status, plan, sidechain, tool_names, tool_result_bytes, errors,
		 findings, shadow_savings)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, x := range rows {
		names, bytes, errs := toolSummary(x.Tools)
		if _, err := stmt.ExecContext(ctx, x.RequestKey, x.Source, x.SessionKey, x.TS, x.Model, x.Input,
			x.CacheWrite5m, x.CacheWrite1h, x.CacheWriteNA, x.CacheRead, x.Output, nullFloat(x.CostUSD),
			x.PricingStatus, string(x.Plan), boolInt(x.Sidechain), names, bytes, errs,
			nullJSON(x.Findings), nullJSON(x.ShadowSavings)); err != nil {
			return err
		}
	}
	now := time.Now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO history_files
		(file_key, source, size, mtime, offset, parser_version, format_version, lines, turns, skipped_lines, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(file_key) DO UPDATE SET size=excluded.size, mtime=excluded.mtime, offset=excluded.offset,
		  parser_version=excluded.parser_version, format_version=excluded.format_version, lines=excluded.lines,
		  turns=excluded.turns, skipped_lines=excluded.skipped_lines, updated_at=excluded.updated_at`,
		r.file.Key, r.file.Source, r.file.Size, r.file.MTime, r.cp.Offset, in.Versions[r.file.Source], r.stats.FormatVersion,
		r.cp.Lines, r.cp.Turns, prevSkipped+r.stats.Skipped, now.UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

// toolSummary returns tool names with call counts as JSON, the total result
// size, and the number of failed calls.
func toolSummary(calls []ToolCall) (any, int64, int) {
	if len(calls) == 0 {
		return nil, 0, 0
	}
	counts := map[string]int{}
	var bytes int64
	var errs int
	for _, c := range calls {
		counts[c.Name]++
		bytes += c.ResultBytes
		if c.Error {
			errs++
		}
	}
	b, _ := json.Marshal(counts)
	return string(b), bytes, errs
}

func nullFloat(f *float64) any {
	if f == nil {
		return nil
	}
	return *f
}

func nullJSON(j json.RawMessage) any {
	if len(j) == 0 {
		return nil
	}
	return string(j)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ErrNoHistory is returned by callers that need history when none exists.
var ErrNoHistory = errors.New("no agent history found; run an agent such as Claude Code first, or point trove at its history directory")
