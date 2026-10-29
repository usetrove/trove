package logs

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/usetrove/trove/internal/analyze"
	"github.com/usetrove/trove/internal/pricing"
)

// Detectors the history audit can and cannot run. History has token counts,
// tool names and result sizes, but not request bodies, so detectors that read
// text or compare prompts are skipped, not approximated.
var (
	// AuditDetectors run on history.
	AuditDetectors = []string{analyze.DetOversized, analyze.DetDuplicate, analyze.DetExpensive}
	// SkippedDetectors need data that history does not keep.
	SkippedDetectors = map[string]string{
		analyze.DetNoisy:        "needs tool output text, which is not stored",
		analyze.DetContextFiles: "needs the prompt text",
		analyze.DetUnusedTools:  "needs the tool definitions sent with each request",
		analyze.DetCacheMiss:    "needs consecutive request bodies to compare prefixes",
		analyze.DetDrift:        "not yet implemented for history",
	}
)

// Auditor turns events into findings. Every number it produces is an estimate
// and a lower bound: it counts the first request that carried a wasteful tool
// result, not the later requests that carried it again.
type Auditor struct {
	an   *analyze.Analyzer
	opts analyze.Options
}

// NewAuditor returns an auditor that prices findings with tbl (nil: tokens only).
func NewAuditor(tbl *pricing.Table) *Auditor {
	return &Auditor{an: analyze.New(analyze.Options{Pricing: tbl}), opts: analyze.DefaultOptions(tbl)}
}

// Annotate fills Findings and ShadowSavings on rows. events and rows are
// aligned and in file order. It matches Ingester.Annotate.
//
// A tool result enters the context of the next request in the same stream
// (session and sidechain), so a finding about a result is attached to that
// request, where its tokens were billed.
func (a *Auditor) Annotate(events []Event, rows []Row) {
	type stream struct {
		session string
		side    bool
	}
	seen := map[stream]map[string]bool{} // result hashes already in this stream
	next := map[int]int{}                // event index -> index of the next event in the same stream
	last := map[stream]int{}
	for i := len(events) - 1; i >= 0; i-- {
		k := stream{events[i].SessionKey, events[i].Sidechain}
		if j, ok := last[k]; ok {
			next[i] = j
		}
		last[k] = i
	}

	type acc struct {
		overTokens, dupTokens int64
		overN, dupN           int
		tools                 map[string]int
	}
	byRow := map[int]*acc{}
	get := func(j int) *acc {
		if byRow[j] == nil {
			byRow[j] = &acc{tools: map[string]int{}}
		}
		return byRow[j]
	}

	for i, e := range events {
		k := stream{e.SessionKey, e.Sidechain}
		if seen[k] == nil {
			seen[k] = map[string]bool{}
		}
		j, carried := next[i]
		for _, t := range e.Tools {
			if t.ResultBytes == 0 {
				continue
			}
			tokens := int64(float64(t.ResultBytes) / analyze.DefaultBytesPerToken)
			dup := t.ResultBytes >= analyze.MinDuplicateBytes && t.ResultHash != "" && seen[k][t.ResultHash]
			if t.ResultHash != "" {
				seen[k][t.ResultHash] = true
			}
			if !carried {
				continue // never sent to the model, so nothing was billed
			}
			x := get(j)
			if dup {
				x.dupTokens += tokens
				x.dupN++
				x.tools[t.Name]++
			} else if over := tokens - a.opts.OversizedToolTokens; over > 0 {
				x.overTokens += over
				x.overN++
				x.tools[t.Name]++
			}
		}
	}

	for i := range rows {
		e := events[i]
		var fs []analyze.Finding
		if x := byRow[i]; x != nil {
			if x.overN > 0 {
				fs = append(fs, analyze.NewFinding(analyze.DetOversized, "low", x.overTokens, a.price(e, x.overTokens), map[string]any{
					"results": x.overN, "threshold_tokens": a.opts.OversizedToolTokens, "tools": x.tools, "basis": basis,
					"note": "tokens counted are only those above the threshold",
				}))
			}
			if x.dupN > 0 {
				fs = append(fs, analyze.NewFinding(analyze.DetDuplicate, "low", x.dupTokens, a.price(e, x.dupTokens), map[string]any{
					"duplicates": x.dupN, "tools": x.tools, "basis": basis,
				}))
			}
		}
		if req, ok := a.request(e); ok {
			totalIn := e.Input + e.CacheRead + e.CacheWrite5m + e.CacheWrite1h
			if f, ok := a.an.ExpensiveModel(providerFor(e.Source), req, time.UnixMilli(e.TS), totalIn, e.Output); ok {
				fs = append(fs, f)
			}
		}
		if len(fs) == 0 {
			continue
		}
		sort.Slice(fs, func(x, y int) bool { return fs[x].Detector < fs[y].Detector })
		rows[i].Findings, _ = json.Marshal(fs)
		rows[i].ShadowSavings, _ = json.Marshal(analyze.ShadowFor(fs))
	}
}

const basis = "history: result size converted to tokens at a prior bytes-per-token ratio; counts the first request that carried the result only (a lower bound)"

func (a *Auditor) request(e Event) (pricing.Request, bool) {
	if e.CacheWriteNA > 0 {
		return pricing.Request{}, false
	}
	return pricing.Request{
		Provider: providerFor(e.Source), Model: e.Model, Time: time.UnixMilli(e.TS),
		Usage: pricing.Usage{
			UncachedInput: e.Input, CacheRead: e.CacheRead, Output: e.Output,
			CacheWrite: map[string]int64{"5m": e.CacheWrite5m, "1h": e.CacheWrite1h},
		},
	}, true
}

// price values removing tokens from request e: new content in a prompt was
// billed as a cache write when the request wrote cache, otherwise as input.
func (a *Auditor) price(e Event, tokens int64) *float64 {
	tbl := a.an.Pricing()
	req, ok := a.request(e)
	if tbl == nil || !ok || tokens <= 0 {
		return nil
	}
	rem := map[string]int64{}
	left := tokens
	for _, ttl := range []string{"5m", "1h"} {
		take := min(left, req.Usage.CacheWrite[ttl])
		if take > 0 {
			rem[pricing.CatCacheWrite(ttl)] = take
			left -= take
		}
	}
	if left > 0 {
		rem[pricing.CatInput] = left
	}
	cf, err := tbl.CostWithoutAll(req, rem)
	if err != nil || !cf.Valid {
		return nil
	}
	v := cf.Savings.Float64()
	return &v
}
