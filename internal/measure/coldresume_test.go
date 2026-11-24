package measure

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/store"
	"github.com/usetrove/trove/internal/technique"
)

func TestColdNoteTokensMatchTheRegistry(t *testing.T) {
	o, ok := technique.OverheadOf(config.OptimizerHookColdResume)
	if !ok || o.PerUse != ColdNoteTokens {
		t.Errorf("registry declares %+v (ok %v); measure charges %d per cleared session", o, ok, ColdNoteTokens)
	}
}

func coldEvent(t *testing.T, db *store.DB, ts time.Time, kind string, tokens int64) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `INSERT INTO hook_events(ts, session_key, hook, kind, tokens) VALUES(?,?,?,?,?)`,
		ts.UnixMilli(), "c-s", config.OptimizerHookColdResume, kind, tokens); err != nil {
		t.Fatal(err)
	}
}

func meterReading(t *testing.T, db *store.DB, ts time.Time, pct float64, resets time.Time) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `INSERT INTO limit_samples(ts, session_key, five_hour_pct, five_hour_resets_at) VALUES(?,?,?,?)`,
		ts.UnixMilli(), "c-m", pct, resets.UnixMilli()); err != nil {
		t.Fatal(err)
	}
}

func TestColdResumeSummaryCounts(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	w := Window{From: t0, To: t0.Add(30 * 24 * time.Hour)}
	if c, err := ColdResumeSummary(ctx, db, w); err != nil || c != nil {
		t.Fatalf("no events: %+v %v", c, err)
	}
	at := t0.Add(time.Hour)
	coldEvent(t, db, at, "blocked", 200000)
	coldEvent(t, db, at, "overridden", 200000)
	coldEvent(t, db, at, "blocked", 300000)
	coldEvent(t, db, at, "cleared", 300000)
	coldEvent(t, db, at, "blocked", 180000)
	coldEvent(t, db, at, "advised", 160000)
	coldEvent(t, db, at, "would_block", 210000)
	coldEvent(t, db, at, "would_advise", 170000)
	coldEvent(t, db, t0.Add(-time.Hour), "blocked", 999999) // outside the window
	c, err := ColdResumeSummary(ctx, db, w)
	if err != nil || c == nil {
		t.Fatalf("%+v %v", c, err)
	}
	if c.Blocked != 3 || c.Overridden != 1 || c.Cleared != 1 || c.Advised != 1 || c.WouldBlock != 1 || c.WouldAdvise != 1 {
		t.Errorf("counts %+v", c)
	}
	if c.TokensAvoided.Value != 300000-ColdNoteTokens || c.TokensAvoided.Label != Estimated || c.TokensAvoided.N != 1 {
		t.Errorf("tokens avoided %+v", c.TokensAvoided)
	}
	if c.ClearedShare == nil || math.Abs(c.ClearedShare.Value-100.0/3) > 1e-9 || c.ClearedShare.Label != Measured ||
		c.OverriddenShare == nil || math.Abs(c.OverriddenShare.Value-100.0/3) > 1e-9 {
		t.Errorf("shares %+v %+v", c.ClearedShare, c.OverriddenShare)
	}
	if c.HourAfter != nil || !strings.Contains(c.HourAfterNote, "Not enough sessions yet") {
		t.Errorf("hour after should wait for more sessions: %+v %q", c.HourAfter, c.HourAfterNote)
	}
}

// The hour after: the meter's movement after each block is compared with the movement after shadow events of a
// similar rebuild size. Events whose window reset in the hour are left out.
func TestColdResumeHourAfter(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	w := Window{From: t0, To: t0.Add(60 * 24 * time.Hour)}
	day := 24 * time.Hour
	// Each event gets its own day, its own 5-hour window and a reading just before it.
	for i := 0; i < 12; i++ {
		at := t0.Add(time.Duration(i)*day + time.Hour)
		resets := at.Add(3 * time.Hour)
		meterReading(t, db, at.Add(-10*time.Minute), 20, resets)
		meterReading(t, db, at.Add(40*time.Minute), 21+float64(i%2), resets) // 1 or 2 points: mean 1.5
		coldEvent(t, db, at, "blocked", 200000)
	}
	for i := 0; i < 12; i++ {
		at := t0.Add(time.Duration(20+i)*day + time.Hour)
		resets := at.Add(3 * time.Hour)
		meterReading(t, db, at.Add(-10*time.Minute), 20, resets)
		meterReading(t, db, at.Add(50*time.Minute), 24+float64(i%2), resets) // 4 or 5 points: mean 4.5
		coldEvent(t, db, at, "would_block", 220000)
	}
	// A shadow event in another size bucket, with nothing to match it: counted in the side's mean, not the difference.
	at := t0.Add(40*day + time.Hour)
	meterReading(t, db, at.Add(-time.Minute), 10, at.Add(2*time.Hour))
	meterReading(t, db, at.Add(time.Minute), 40, at.Add(2*time.Hour))
	coldEvent(t, db, at, "would_block", 900000)
	// A block whose window reset within the hour: left out.
	at = t0.Add(45*day + time.Hour)
	meterReading(t, db, at.Add(-time.Minute), 80, at.Add(10*time.Minute))
	meterReading(t, db, at.Add(30*time.Minute), 2, at.Add(5*time.Hour))
	coldEvent(t, db, at, "blocked", 200000)

	c, err := ColdResumeSummary(ctx, db, w)
	if err != nil || c == nil || c.HourAfter == nil {
		t.Fatalf("%+v %v", c, err)
	}
	h := c.HourAfter
	if h.Guard.N != 12 || h.Guard.Value != 1.5 || h.Guard.Label != Measured {
		t.Errorf("guard side %+v", h.Guard)
	}
	if h.Shadow.N != 13 {
		t.Errorf("shadow side %+v", h.Shadow)
	}
	if math.Abs(h.Diff.Value-(-3)) > 1e-9 || h.DiffLow >= -3 || h.DiffHi <= -3 || h.DiffHi >= 0 || h.Diff.Label != Measured {
		t.Errorf("difference %+v [%v, %v]", h.Diff, h.DiffLow, h.DiffHi)
	}
	if !strings.Contains(c.HourAfterNote, "not a randomized comparison") {
		t.Errorf("note %q", c.HourAfterNote)
	}
}

func TestHourMovement(t *testing.T) {
	r := func(ts, resets int64, pct float64) reading { return reading{ts: ts, resets: resets, pct: pct} }
	rs := []reading{r(100, 10_000_000, 10), r(200, 10_000_000, 12), r(3_600_150, 10_030_000, 15), r(3_700_000, 20_000_000, 1)}
	cases := []struct {
		ts   int64
		want float64
		ok   bool
	}{
		{150, 5, true},        // 10 before, 15 within the hour (same window: reset times 30 s apart)
		{50, 0, false},        // no reading before
		{250, 3, true},        // 12 before, 15 within the hour
		{3_600_100, 0, false}, // the hour reaches a reading in the next window
	}
	for _, c := range cases {
		got, ok := hourMovement(rs, c.ts)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("ts %d: %v %v, want %v %v", c.ts, got, ok, c.want, c.ok)
		}
	}
}
