package suggest

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/store"
)

var (
	ttlBase = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	ttlFrom = ttlBase.AddDate(0, 0, -10)
	ttlTo   = ttlBase.AddDate(0, 0, 10)
)

type ttlFx struct {
	t   *testing.T
	db  *store.DB
	tbl *pricing.Table
	n   int
}

func newTTLFx(t *testing.T) *ttlFx {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	tbl, err := pricing.Default()
	if err != nil {
		t.Fatal(err)
	}
	return &ttlFx{t: t, db: db, tbl: tbl}
}

type hreq struct {
	session, source, model, plan string
	side                         int
	at                           time.Time
	w5, w1, wo, rd               int64
}

func (f *ttlFx) add(r hreq) {
	f.t.Helper()
	f.n++
	if r.source == "" {
		r.source = "claude_code"
	}
	if r.model == "" {
		r.model = "claude-sonnet-5-5"
	}
	if r.plan == "" {
		r.plan = "api_key"
	}
	if _, err := f.db.Exec(`INSERT INTO history_requests (request_key, source, session_key, ts, model, cache_write_5m, cache_write_1h, cache_write_other, cache_read, plan, sidechain)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`, fmt.Sprintf("k%d", f.n), r.source, r.session, r.at.UnixMilli(), r.model, r.w5, r.w1, r.wo, r.rd, r.plan, r.side); err != nil {
		f.t.Fatal(err)
	}
}

// sessions adds n sessions. Each writes `prefix` tokens first, then makes `quick`
// requests two minutes apart, then `pauses` requests that each follow a pause of
// `pause`. When oneHour is true the writes are at the one-hour rate, else at five
// minutes. A request after a pause that a five-minute entry would not survive
// rewrites the prefix when !oneHour; with oneHour it reads it.
func (f *ttlFx) sessions(n int, o struct {
	oneHour        bool
	prefix         int64
	quick, pauses  int
	pause          time.Duration
	side           int
	plan, source   string
	unsplit        bool
	model          string
	rewriteOnPause bool
}) {
	f.t.Helper()
	for i := 0; i < n; i++ {
		s := fmt.Sprintf("s%d-%d", f.n, i)
		at := ttlBase.Add(time.Duration(i) * time.Minute)
		write := func(tokens int64, read int64, r hreq) hreq {
			switch {
			case o.unsplit:
				r.wo = tokens
			case o.oneHour:
				r.w1 = tokens
			default:
				r.w5 = tokens
			}
			r.rd = read
			return r
		}
		base := hreq{session: s, side: o.side, plan: o.plan, source: o.source, model: o.model}
		first := base
		first.at = at
		f.add(write(o.prefix, 0, first))
		for q := 0; q < o.quick; q++ {
			at = at.Add(2 * time.Minute)
			r := base
			r.at = at
			f.add(write(500, o.prefix, r))
		}
		for p := 0; p < o.pauses; p++ {
			at = at.Add(o.pause)
			r := base
			r.at = at
			if o.rewriteOnPause {
				f.add(write(o.prefix, 0, r)) // the entry expired: the whole prefix is written again
			} else {
				f.add(write(500, o.prefix, r)) // the entry survived: it is read
			}
		}
	}
}

func (f *ttlFx) run() []Suggestion {
	f.t.Helper()
	got, err := cacheTTL(context.Background(), f.db, f.tbl, ttlFrom, ttlTo)
	if err != nil {
		f.t.Fatal(err)
	}
	return got
}

type opts = struct {
	oneHour        bool
	prefix         int64
	quick, pauses  int
	pause          time.Duration
	side           int
	plan, source   string
	unsplit        bool
	model          string
	rewriteOnPause bool
}

// The one-hour entries earned their premium: the pauses of 5 to 60 minutes read
// the prompt back. Nothing to say.
func TestCacheTTLStaysQuietWhenOneHourIsPaying(t *testing.T) {
	f := newTTLFx(t)
	f.sessions(30, opts{oneHour: true, prefix: 60000, quick: 10, pauses: 4, pause: 20 * time.Minute})
	if got := f.run(); len(got) != 0 {
		t.Fatalf("suggested a change although the one-hour lifetime paid for itself: %+v", got)
	}
}

// One hour was paid for and never needed: every request followed within five minutes.
func TestCacheTTLSuggestsFiveMinutesWhenNoPauseNeededOneHour(t *testing.T) {
	f := newTTLFx(t)
	f.sessions(30, opts{oneHour: true, prefix: 60000, quick: 10})
	got := f.run()
	if len(got) != 1 {
		t.Fatalf("want 1 suggestion, got %+v", got)
	}
	s := got[0]
	if s.Kind != KindCacheTTL || !strings.Contains(s.Title, "5-minute") || !strings.Contains(s.Title, "main conversation") {
		t.Errorf("title %q", s.Title)
	}
	if !strings.Contains(s.Detail, `"promptCacheTtl"`) || !strings.Contains(s.Detail, `"5m"`) || !strings.Contains(s.Detail, "2.1.242") {
		t.Errorf("detail lacks the setting, value or version: %q", s.Detail)
	}
	if s.Label != "estimated" || s.EstSavingsUSD == nil {
		t.Fatalf("label %q, savings %v", s.Label, s.EstSavingsUSD)
	}
	// 30 sessions x 60,000 + 300 x 500 one-hour tokens, premium 4-2.5 = $1.50 per million.
	want := float64(30*60000+30*10*500) * 1.5 / 1e6
	if d := *s.EstSavingsUSD - want; d < -0.01 || d > 0.01 {
		t.Errorf("saving %.4f, want about %.4f", *s.EstSavingsUSD, want)
	}
	if s.Confidence != "low" {
		t.Errorf("330 requests is below the %d needed for medium confidence; got %q", confidentRequests, s.Confidence)
	}
}

func TestCacheTTLConfidenceRisesWithMoreHistory(t *testing.T) {
	f := newTTLFx(t)
	f.sessions(40, opts{oneHour: true, prefix: 60000, quick: 10}) // 440 requests, 40 sessions
	got := f.run()
	if len(got) != 1 || got[0].Confidence != "medium" {
		t.Fatalf("got %+v", got)
	}
}

func TestCacheTTLSuggestsOneHourWhenFiveMinuteEntriesKeepExpiring(t *testing.T) {
	f := newTTLFx(t)
	f.sessions(30, opts{prefix: 60000, quick: 20, pauses: 10, pause: 20 * time.Minute, rewriteOnPause: true})
	got := f.run()
	if len(got) != 1 || !strings.Contains(got[0].Title, "1-hour") || !strings.Contains(got[0].Detail, `"1h"`) {
		t.Fatalf("got %+v", got)
	}
	if got[0].EstSavingsUSD == nil || *got[0].EstSavingsUSD <= 0 {
		t.Errorf("saving %v", got[0].EstSavingsUSD)
	}
}

func TestCacheTTLNeedsEnoughRequests(t *testing.T) {
	f := newTTLFx(t)
	f.sessions(5, opts{oneHour: true, prefix: 60000, quick: 10}) // 55 requests
	if got := f.run(); len(got) != 0 {
		t.Fatalf("suggested from %d requests: %+v", 55, got)
	}
}

// A subscription may not bill per token, so no dollar saving is stated.
func TestCacheTTLStatesNoMoneyFigureWithoutApiKeyEvidence(t *testing.T) {
	for _, plan := range []string{"subscription", "unknown"} {
		f := newTTLFx(t)
		f.sessions(30, opts{oneHour: true, prefix: 60000, quick: 10, plan: plan})
		got := f.run()
		if len(got) != 1 {
			t.Fatalf("%s: got %+v", plan, got)
		}
		if got[0].EstSavingsUSD != nil {
			t.Errorf("%s: stated a dollar saving of %.2f for a plan that may not bill per token", plan, *got[0].EstSavingsUSD)
		}
		if !strings.Contains(got[0].Scenario, "not money you would have saved") || !strings.Contains(got[0].Scenario, "$") {
			t.Errorf("%s: scenario %q", plan, got[0].Scenario)
		}
	}
}

// When the history does not say which lifetime wrote the cache, there is no finding.
func TestCacheTTLSkipsHistoryWithoutALifetimeSplit(t *testing.T) {
	f := newTTLFx(t)
	f.sessions(30, opts{prefix: 60000, quick: 10, unsplit: true})
	if got := f.run(); len(got) != 0 {
		t.Fatalf("guessed a lifetime from unsplit writes: %+v", got)
	}
}

func TestCacheTTLSubagentsAreJudgedSeparately(t *testing.T) {
	f := newTTLFx(t)
	// Subagents: five-minute writes with many expiring pauses. The main conversation: healthy one hour.
	f.sessions(30, opts{prefix: 60000, quick: 20, pauses: 10, pause: 20 * time.Minute, rewriteOnPause: true, side: 1})
	f.sessions(30, opts{oneHour: true, prefix: 60000, quick: 10, pauses: 4, pause: 20 * time.Minute})
	got := f.run()
	if len(got) != 1 {
		t.Fatalf("want only the subagent finding, got %+v", got)
	}
	if !strings.Contains(got[0].Title, "subagents") || !strings.Contains(got[0].Detail, `"subagentPromptCacheTtl"`) || strings.Contains(got[0].Detail, `"promptCacheTtl"`) {
		t.Errorf("subagent finding names the wrong setting: %+v", got[0])
	}
}

func TestCacheTTLIgnoresOtherAgentsUnpricedModelsAndOldRows(t *testing.T) {
	f := newTTLFx(t)
	f.sessions(30, opts{oneHour: true, prefix: 60000, quick: 10, source: "codex"})
	if got := f.run(); len(got) != 0 {
		t.Errorf("used another agent's history: %+v", got)
	}
	f = newTTLFx(t)
	f.sessions(30, opts{oneHour: true, prefix: 60000, quick: 10, model: "model-with-no-price"})
	if got := f.run(); len(got) != 0 {
		t.Errorf("made a figure for an unpriced model: %+v", got)
	}
	f = newTTLFx(t)
	f.sessions(30, opts{oneHour: true, prefix: 60000, quick: 10})
	got, err := cacheTTL(context.Background(), f.db, f.tbl, ttlBase.AddDate(1, 0, 0), ttlBase.AddDate(1, 0, 1))
	if err != nil || len(got) != 0 {
		t.Errorf("rows outside the window counted: %v %+v", err, got)
	}
}

func TestCacheTTLNoHistoryTableMeansNoFinding(t *testing.T) {
	f := newTTLFx(t)
	if got := f.run(); len(got) != 0 {
		t.Fatalf("empty history gave %+v", got)
	}
}

func TestCacheTTLIsDeterministicAndPartOfGenerate(t *testing.T) {
	f := newTTLFx(t)
	f.sessions(30, opts{oneHour: true, prefix: 60000, quick: 10})
	a, b := f.run(), f.run()
	if len(a) != 1 || len(b) != 1 || a[0].Detail != b[0].Detail || *a[0].EstSavingsUSD != *b[0].EstSavingsUSD {
		t.Fatalf("not deterministic: %+v vs %+v", a, b)
	}
	all, err := Generate(context.Background(), f.db, f.tbl, ttlFrom, ttlTo)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range all {
		found = found || s.Kind == KindCacheTTL
	}
	if !found {
		t.Error("Generate does not include the cache lifetime finding")
	}
	none, err := Generate(context.Background(), f.db, nil, ttlFrom, ttlTo)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range none {
		if s.Kind == KindCacheTTL {
			t.Error("a finding that needs prices was produced without a price list")
		}
	}
}
