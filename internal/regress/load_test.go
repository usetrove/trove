package regress

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/analyze"
	"github.com/usetrove/trove/internal/measure/limitfit"
	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/store"
)

func ledgerReq(ts int64, cw int64, thinking int64) analyze.LedgerRequest {
	th := thinking
	return analyze.LedgerRequest{SessionKey: "s1", TS: ts, Model: "claude-opus-5-5", Plan: "api_key", AgentVersion: "2.1.89",
		Input: 10, CacheWrite5m: cw, CacheRead: 30_000, Output: 500, Thinking: &th}
}

func TestFromLedgerMarksUnexplainedRebuildsAndThinking(t *testing.T) {
	second := ledgerReq(61_000, 60_000, 300)
	second.CacheRead = 0 // the whole context written again, a minute later, same model and version
	reqs := []analyze.LedgerRequest{ledgerReq(1_000, 30_000, 0), second}
	got := FromLedger(reqs)
	if len(got) != 2 {
		t.Fatalf("got %d requests", len(got))
	}
	if got[0].Unexplained || !got[1].Unexplained {
		t.Errorf("rebuilds: %+v", got)
	}
	if got[1].Thinking != 300 || !got[1].ThinkingMeasured || !got[1].Split || got[1].Version != "2.1.89" || got[1].Session != "s1" {
		t.Errorf("request: %+v", got[1])
	}
}

func TestOutputEquivalentAndIntervals(t *testing.T) {
	tbl, err := pricing.Default()
	if err != nil {
		t.Fatal(err)
	}
	r := analyze.LedgerRequest{TS: now.UnixMilli(), Model: "claude-opus-5-5", Output: 1_000, SessionKey: "s1", AgentVersion: "2.1.89"}
	oe, ok := OutputEquivalent(tbl, &r)
	if !ok || math.Abs(oe-1_000) > 1e-6 {
		t.Fatalf("output alone: %v %v", oe, ok)
	}
	r.Input = 1_000 // input is cheaper than output, so it adds less than 1,000
	if oe2, _ := OutputEquivalent(tbl, &r); oe2 <= oe || oe2 >= 2_000 {
		t.Errorf("with input: %v", oe2)
	}
	if _, ok := OutputEquivalent(tbl, &analyze.LedgerRequest{TS: r.TS, Model: "not-a-model", Output: 5}); ok {
		t.Error("an unknown model was priced")
	}
	ivs := Intervals([]limitfit.Interval{{Start: r.TS - 1, End: r.TS, Delta: 3, Clean: true}, {Start: r.TS - 1, End: r.TS, Delta: 3}},
		[]analyze.LedgerRequest{r}, tbl)
	if len(ivs) != 1 || ivs[0].Version != "2.1.89" || ivs[0].OETokens <= 0 || len(ivs[0].Sessions) != 1 {
		t.Errorf("intervals: %+v", ivs)
	}
}

func TestRefreshOnARealDatabase(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	r := Refresh(context.Background(), db, dir, nil, now, false)
	if r == nil || len(r.Alerts) != 0 || r.ComputedAt != now.UnixMilli() {
		t.Fatalf("result = %+v", r)
	}
	if c := ReadCache(dir); c == nil || c.ComputedAt != now.UnixMilli() {
		t.Fatalf("cache = %+v", c)
	}
	// Fresh: not recomputed (a later time inside RefreshEvery returns the saved one).
	if r2 := Refresh(context.Background(), db, dir, nil, now.Add(time.Minute), false); r2.ComputedAt != now.UnixMilli() {
		t.Errorf("recomputed a fresh result")
	}
	if StatusSentence(r, now) != "" {
		t.Error("a sentence without an alert")
	}
}
