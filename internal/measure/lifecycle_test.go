package measure

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
)

func TestPhaseNeedsBothDaysAndSessions(t *testing.T) {
	m := config.Default().Measure // 7 days and 20 sessions
	tests := []struct {
		name     string
		days     float64
		sessions int
		want     Phase
	}{
		{"just installed", 0, 0, PhaseBaseline},
		{"a week but only 3 sessions", 8, 3, PhaseBaseline},
		{"20 sessions but only one day", 1, 25, PhaseBaseline},
		{"6.9 days, plenty of sessions", 6.9, 50, PhaseBaseline},
		{"exactly 7 days and 20 sessions", 7, 20, PhaseActive},
		{"well past both", 30, 400, PhaseActive},
	}
	for _, tt := range tests {
		now := t0.Add(time.Duration(tt.days * 24 * float64(time.Hour)))
		if got := PhaseFor(m, t0, now, tt.sessions).Phase; got != tt.want {
			t.Errorf("%s: got %s, want %s", tt.name, got, tt.want)
		}
	}
}

func TestGateOnlyEverLowersAndRespectsTheBaseline(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	cfg := config.Default().Measure
	now := t0
	g := NewGate(db, cfg, func() time.Time { return now })
	g.ttl = 0

	// Fresh install: the user asked for "on", but the baseline keeps it in shadow.
	if got := g.State("s1", opt, config.StateOn, now); got != config.StateShadow {
		t.Fatalf("during baseline want shadow, got %s", got)
	}
	// Shadow and off are never raised, whatever the phase.
	for _, c := range []string{config.StateShadow, config.StateOff} {
		if got := g.State("s1", opt, c, now); got != c {
			t.Errorf("configured %s became %s", c, got)
		}
	}
	// 8 days and 20 sessions later the baseline is over.
	for i := 0; i < 20; i++ {
		if _, err := db.ExecContext(ctx, `INSERT INTO sessions(session_key, agent, started_at) VALUES(?, 'a', ?)`, fmt.Sprint("b", i), now.UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	now = t0.AddDate(0, 0, 8)
	if got := g.State("s2", opt, config.StateOn, now); got != config.StateOn {
		t.Errorf("after the baseline want on, got %s", got)
	}
}

func TestInterleavingIsStablePerSessionAndSplitsRoughlyEvenly(t *testing.T) {
	on := 0
	for i := 0; i < 1000; i++ {
		key := fmt.Sprintf("session-%d", i)
		a := Arm(key, opt)
		if a != Arm(key, opt) {
			t.Fatal("a session's arm changed between calls")
		}
		if a == config.StateOn {
			on++
		}
	}
	if on < 440 || on > 560 {
		t.Errorf("%d of 1000 sessions on; want roughly half", on)
	}
}

func TestGateInterleaveSendsOffArmToShadow(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	cfg := config.Default().Measure
	cfg.Interleave, cfg.BaselineDays, cfg.BaselineSessions = true, 0, 0
	g := NewGate(db, cfg, func() time.Time { return t0 })
	g.ttl = 0
	_ = ctx
	var sawOn, sawShadow bool
	for i := 0; i < 50; i++ {
		key := fmt.Sprint("k", i)
		got := g.State(key, opt, config.StateOn, t0)
		if want := map[string]string{config.StateOn: config.StateOn, config.StateOff: config.StateShadow}[Arm(key, opt)]; got != want {
			t.Fatalf("%s: got %s, want %s", key, got, want)
		}
		sawOn, sawShadow = sawOn || got == config.StateOn, sawShadow || got == config.StateShadow
	}
	if !sawOn || !sawShadow {
		t.Error("interleaving should produce both on and shadow sessions")
	}
}

func TestGateFailsTowardShadowWhenTheDatabaseIsUnavailable(t *testing.T) {
	db := openDB(t)
	cfg := config.Default().Measure
	cfg.BaselineDays, cfg.BaselineSessions = 0, 0
	g := NewGate(db, cfg, func() time.Time { return t0 })
	g.ttl = 0
	_ = db.Close()
	if got := g.State("s", opt, config.StateOn, t0); got != config.StateShadow {
		t.Errorf("with the database closed want shadow (original request forwarded), got %s", got)
	}
}
