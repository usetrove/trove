package measure

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/store"
)

const opt = "duplicate_result_collapser"

// fixture describes a simulated history: baseline sessions before the optimizer
// was on, then sessions with it on, optionally with off sessions interleaved.
type fixture struct {
	baselineRepeats []int
	onRepeats       []int
	offRepeats      []int
	baselineErrs    []int
	onErrs          []int
	onCollapsed     []int // per on session
	onRefetches     []int
	noTools         bool
}

func (f fixture) load(t *testing.T) (*store.DB, time.Time) {
	t.Helper()
	db := openDB(t)
	at := t0
	next := func() time.Time { at = at.Add(time.Hour); return at }
	n := 0
	add := func(s seed) {
		n++
		s.key = fmt.Sprintf("s%03d", n)
		s.pinnedAt = next()
		s.noTools = f.noTools
		insertFinished(t, db, s)
	}
	pick := func(v []int, i int) int {
		if len(v) == 0 {
			return 0
		}
		return v[i%len(v)]
	}
	for i := range f.baselineRepeats {
		add(seed{repeats: f.baselineRepeats[i], toolErrs: pick(f.baselineErrs, i)})
	}
	for i := range f.onRepeats {
		add(seed{on: []string{opt}, repeats: f.onRepeats[i], toolErrs: pick(f.onErrs, i), collapsed: pick(f.onCollapsed, i), refetches: pick(f.onRefetches, i)})
	}
	for i := range f.offRepeats {
		add(seed{repeats: f.offRepeats[i]})
	}
	return db, at.Add(time.Hour)
}

func TestGuardrailRollbackOnRegressionsNeverOnCleanHistories(t *testing.T) {
	m := config.Default().Measure // 20 sessions minimum, +30% retries, 25% re-fetch
	base := cycle([]int{1, 2, 1, 2, 1, 2}, 30)
	tests := []struct {
		name     string
		f        fixture
		wantRule string // "" = must not roll back
		wantRef  string
	}{
		{"regression: retries up 67% against baseline", fixture{baselineRepeats: base, onRepeats: cycle([]int{2, 3, 2, 3}, 25)}, RuleRetries, RefBaseline},
		{"regression: retries up against off sessions", fixture{baselineRepeats: base, onRepeats: cycle([]int{2, 3, 2, 3}, 25), offRepeats: cycle([]int{1, 2, 1, 2}, 25)}, RuleRetries, RefOffSessions},
		{"regression: tool errors up", fixture{baselineRepeats: base, onRepeats: base[:25], baselineErrs: []int{0, 1}, onErrs: []int{1, 2}}, RuleToolErrors, RefBaseline},
		{"regression: results re-fetched in most sessions", fixture{baselineRepeats: base, onRepeats: base[:25], onCollapsed: []int{3}, onRefetches: []int{1, 1, 0}}, RuleRefetch, RefBaseline},
		{"regression: retries appear where there were none", fixture{baselineRepeats: cycle([]int{0}, 30), onRepeats: cycle([]int{1, 2}, 25)}, RuleRetries, RefBaseline},

		{"clean: identical behaviour", fixture{baselineRepeats: base, onRepeats: cycle([]int{2, 1, 2, 1}, 30)}, "", ""},
		{"clean: retries slightly down", fixture{baselineRepeats: base, onRepeats: cycle([]int{1, 1, 2, 1}, 30)}, "", ""},
		{"clean: +7% retries is under the 30% limit", fixture{baselineRepeats: cycle([]int{1, 2, 1, 2, 1, 2, 1, 2, 1, 2, 1, 2, 1, 2}, 30), onRepeats: cycle([]int{1, 2, 1, 2, 1, 2, 1, 2, 1, 2, 1, 2, 1, 2, 2}, 30)}, "", ""},
		{"clean: +50% but pure chance (spiky, high variance)", fixture{baselineRepeats: cycle([]int{0, 0, 0, 0, 6}, 20), onRepeats: cycle([]int{0, 0, 0, 0, 9}, 20)}, "", ""},
		{"clean: occasional re-fetch below the limit", fixture{baselineRepeats: base, onRepeats: base[:25], onCollapsed: []int{3}, onRefetches: []int{0, 0, 0, 1, 0, 0, 0, 0}}, "", ""},
		{"clean: re-fetch 8 of 25 sessions is not yet beyond chance", fixture{baselineRepeats: base, onRepeats: base[:25], onCollapsed: []int{2}, onRefetches: cycle([]int{1, 0, 0}, 25)[:25]}, "", ""},
		{"clean: no tool signals at all", fixture{baselineRepeats: base, onRepeats: cycle([]int{9}, 30), noTools: true}, "", ""},

		{"small sample: doubled retries in 8 sessions is not a conclusion", fixture{baselineRepeats: base, onRepeats: cycle([]int{4}, 8)}, "", ""},
		{"small sample: no baseline yet", fixture{baselineRepeats: base[:6], onRepeats: cycle([]int{4}, 30)}, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, now := tt.f.load(t)
			ctx := context.Background()
			checks, events, err := Evaluate(ctx, db, m, now)
			if err != nil {
				t.Fatal(err)
			}
			rolled, _ := Overrides(ctx, db)
			if tt.wantRule == "" {
				if len(events) != 0 || len(rolled) != 0 {
					t.Fatalf("clean history rolled back: events=%+v checks=%+v", events, checks)
				}
				if strings.Contains(strings.ToLower(checks[0].Summary), "unchanged") {
					t.Errorf("summary claims quality is unchanged: %q", checks[0].Summary)
				}
				return
			}
			if len(events) != 1 || events[0].Rule != tt.wantRule || events[0].Reference != tt.wantRef {
				t.Fatalf("events = %+v, want one %s event against %s", events, tt.wantRule, tt.wantRef)
			}
			reason, ok := rolled[opt]
			if !ok || reason != events[0].Reason || !strings.Contains(reason, "shadow") || !strings.Contains(reason, opt) {
				t.Errorf("override not recorded with a reason: %q", reason)
			}
			stored, _ := GuardrailEvents(ctx, db, 10)
			if len(stored) != 1 || stored[0].SampleOn == 0 || (tt.wantRule != RuleRefetch && stored[0].SampleRef == 0) {
				t.Errorf("stored events = %+v", stored)
			}
			// Idempotent: a second run does not roll back again.
			if _, again, err := Evaluate(ctx, db, m, now.Add(time.Minute)); err != nil || len(again) != 0 {
				t.Errorf("second Evaluate: events=%v err=%v", again, err)
			}
		})
	}
}

func TestSmallSamplesReportNotEnoughData(t *testing.T) {
	f := fixture{baselineRepeats: cycle([]int{1, 2}, 30), onRepeats: cycle([]int{5}, 8)}
	db, _ := f.load(t)
	checks, err := Check(context.Background(), db, config.Default().Measure)
	if err != nil || len(checks) != 1 {
		t.Fatalf("checks=%+v err=%v", checks, err)
	}
	c := checks[0]
	if c.Verdict != NotEnoughData || !strings.Contains(c.Summary, TextNotEnough) {
		t.Errorf("verdict=%s summary=%q", c.Verdict, c.Summary)
	}
	for _, r := range c.Rules {
		if r.Comparison.Verdict != NotEnoughData || !strings.Contains(r.Text, TextNotEnough) {
			t.Errorf("rule %s: %+v", r.Rule, r)
		}
	}
}

func TestNoRegressionNeverClaimsQualityUnchanged(t *testing.T) {
	f := fixture{baselineRepeats: cycle([]int{1, 2}, 30), onRepeats: cycle([]int{1, 2}, 30)}
	db, _ := f.load(t)
	checks, _ := Check(context.Background(), db, config.Default().Measure)
	s := checks[0].Summary
	if checks[0].Verdict != NoRegression || !strings.Contains(s, "no regression detected locally") || !strings.Contains(s, "benchmarks") {
		t.Errorf("summary = %q", s)
	}
}

func TestClearedRollbackIsNotImmediatelyRetriggeredByOldSessions(t *testing.T) {
	m := config.Default().Measure
	f := fixture{baselineRepeats: cycle([]int{1, 2}, 30), onRepeats: cycle([]int{3, 4}, 25)}
	db, now := f.load(t)
	ctx := context.Background()
	if _, ev, err := Evaluate(ctx, db, m, now); err != nil || len(ev) != 1 {
		t.Fatalf("first Evaluate: %v %v", ev, err)
	}
	if err := ClearOverride(ctx, db, opt, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if o, _ := Overrides(ctx, db); len(o) != 0 {
		t.Fatalf("override still active: %v", o)
	}
	if _, ev, err := Evaluate(ctx, db, m, now.Add(2*time.Hour)); err != nil || len(ev) != 0 {
		t.Errorf("old sessions re-triggered the rollback: %v %v", ev, err)
	}
}

func TestRolledBackOptimizerGateIsShadowForNewSessions(t *testing.T) {
	m := config.Default()
	f := fixture{baselineRepeats: cycle([]int{1, 2}, 30), onRepeats: cycle([]int{3, 4}, 25)}
	db, now := f.load(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES(?, ?)`, keyInstallAt, fmt.Sprint(t0.AddDate(0, 0, -30).UnixMilli())); err != nil {
		t.Fatal(err)
	}
	g := NewGate(db, m.Measure, func() time.Time { return now })
	g.ttl = 0
	if got := g.State("new", opt, config.StateOn, now); got != config.StateOn {
		t.Fatalf("before rollback want on, got %s", got)
	}
	if _, _, err := Evaluate(ctx, db, m.Measure, now); err != nil {
		t.Fatal(err)
	}
	if got := g.State("new", opt, config.StateOn, now); got != config.StateShadow {
		t.Errorf("after rollback want shadow, got %s", got)
	}
}
