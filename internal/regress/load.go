package regress

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/usetrove/trove/internal/analyze"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/measure/limitfit"
	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/store"
)

// FromLedger turns history requests into the watch's requests: the unexplained rebuilds from the
// rebuild ledger and the thinking split from the output ledger.
func FromLedger(reqs []analyze.LedgerRequest) []Request {
	type at struct {
		session string
		ts      int64
	}
	unexplained := map[at]int{}
	for _, e := range analyze.RebuildLedger(reqs) {
		if e.Cause == analyze.CauseUnexplained {
			unexplained[at{e.Session, e.TS}]++
		}
	}
	entries, _ := analyze.OutputLedger(reqs)
	out := make([]Request, 0, len(entries))
	for _, e := range entries {
		r := Request{TS: e.TS, Session: e.Session, Version: e.Version, Model: e.Model, Effort: e.Effort, Split: !e.Unsplit,
			Thinking: e.Thinking, ThinkingMeasured: e.ThinkingMeasured}
		if k := (at{e.Session, e.TS}); unexplained[k] > 0 {
			unexplained[k]--
			r.Unexplained = true
		}
		out = append(out, r)
	}
	return out
}

// OutputEquivalent is a request's tokens at API list prices, in output tokens of the same model: its
// API-equivalent cost over the cost of one output token. ok is false when the model has no price.
func OutputEquivalent(tbl *pricing.Table, r *analyze.LedgerRequest) (float64, bool) {
	if tbl == nil {
		return 0, false
	}
	at := time.UnixMilli(r.TS)
	full := tbl.Cost(pricing.Request{Provider: pricing.Anthropic, Model: r.Model, Time: at, Usage: pricing.Usage{
		UncachedInput: r.Input, CacheRead: r.CacheRead, Output: r.Output,
		CacheWrite: map[string]int64{"5m": r.CacheWrite5m + r.CacheWriteNA, "1h": r.CacheWrite1h},
	}})
	unit := tbl.Cost(pricing.Request{Provider: pricing.Anthropic, Model: r.Model, Time: at, Usage: pricing.Usage{Output: 1_000_000}})
	if full.Status != pricing.Priced || unit.Status != pricing.Priced || unit.Total.Float64() <= 0 {
		return 0, false
	}
	return full.Total.Float64() / unit.Total.Float64() * 1_000_000, true
}

// Intervals pairs the 5-hour meter's clean intervals with the output-equivalent tokens and versions of
// the requests inside them. An interval with an unpriced request is left out.
func Intervals(ivs []limitfit.Interval, reqs []analyze.LedgerRequest, tbl *pricing.Table) []Interval {
	sorted := append([]analyze.LedgerRequest(nil), reqs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].TS < sorted[j].TS })
	var out []Interval
	for _, iv := range ivs {
		if !iv.Clean || iv.Delta <= 0 {
			continue
		}
		lo := sort.Search(len(sorted), func(i int) bool { return sorted[i].TS > iv.Start })
		x := Interval{Start: iv.Start, End: iv.End, Delta: iv.Delta}
		versions, sessions := map[string]bool{}, map[string]bool{}
		priced := true
		for i := lo; i < len(sorted) && sorted[i].TS <= iv.End; i++ {
			oe, ok := OutputEquivalent(tbl, &sorted[i])
			if !ok {
				priced = false
				break
			}
			x.OETokens += oe
			versions[sorted[i].AgentVersion] = true
			sessions[sorted[i].SessionKey] = true
		}
		if !priced || x.OETokens <= 0 {
			continue
		}
		if len(versions) == 1 {
			for v := range versions {
				x.Version = v
			}
		}
		for s := range sessions {
			x.Sessions = append(x.Sessions, s)
		}
		sort.Strings(x.Sessions)
		out = append(out, x)
	}
	return out
}

// Load reads what the watch needs: the last BaselineDays+RecentDays of Claude Code history and the 5-hour
// meter's intervals. Numbers, hashed keys, model names and version strings only.
func Load(ctx context.Context, db *store.DB, tbl *pricing.Table, now time.Time) (Data, error) {
	d := Data{Now: now, Loc: time.Local}
	w := measure.Window{From: now.AddDate(0, 0, -(BaselineDays + RecentDays + 1)), To: now.Add(time.Minute)}
	reqs, err := measure.HistoryLedger(ctx, db, w)
	if err != nil {
		return d, err
	}
	d.Requests = FromLedger(reqs)
	lf, err := limitfit.Load(ctx, db, now)
	if err != nil {
		return d, err
	}
	d.Intervals = Intervals(limitfit.BuildIntervals(lf.Samples, lf.Requests, limitfit.FiveHour, lf.HistoryThrough), reqs, tbl)
	return d, nil
}

// CacheFile holds the last result, for the status line (one small file read).
const CacheFile = "regress.json"

// RefreshEvery is how old the saved result may get before Refresh recomputes it.
const RefreshEvery = 30 * time.Minute

// WriteCache saves r atomically with permissions 0600.
func WriteCache(dir string, r Result) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".regress-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(0o600); err != nil && !errors.Is(err, os.ErrInvalid) {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(dir, CacheFile))
}

// ReadCache returns the saved result, or nil.
func ReadCache(dir string) *Result {
	b, err := os.ReadFile(filepath.Join(dir, CacheFile)) //nolint:gosec // Trove's own data directory
	if err != nil {
		return nil
	}
	var r Result
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	return &r
}

// Refresh recomputes the result when the saved one is older than RefreshEvery (or force is set) and
// saves it. Best effort: on any error the saved result stays and is returned.
func Refresh(ctx context.Context, db *store.DB, dir string, tbl *pricing.Table, now time.Time, force bool) *Result {
	cur := ReadCache(dir)
	if !force && cur != nil && now.UnixMilli()-cur.ComputedAt < RefreshEvery.Milliseconds() && cur.ComputedAt <= now.UnixMilli() {
		return cur
	}
	d, err := Load(ctx, db, tbl, now)
	if err != nil {
		return cur
	}
	r := Compute(d)
	_ = WriteCache(dir, r)
	return &r
}

// MaxStatusAge: the status line shows an alert from a saved result no older than this.
const MaxStatusAge = 36 * time.Hour

// StatusSentence is the strongest alert's sentence for the status line, or "".
func StatusSentence(r *Result, now time.Time) string {
	if r == nil || len(r.Alerts) == 0 || now.UnixMilli()-r.ComputedAt > MaxStatusAge.Milliseconds() {
		return ""
	}
	return r.Alerts[0].Sentence
}
