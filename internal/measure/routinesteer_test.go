package measure

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"
)

func th(n int64) sql.NullInt64 { return sql.NullInt64{Int64: n, Valid: true} }

func TestSteerTurnsSplitsByEvents(t *testing.T) {
	evs := []steerEvent{
		{100, "steered"}, {200, "turn_end"}, // turn 1: requests at 110 and 150
		{300, "would_steer"}, {400, "turn_end"}, {400, "reprompted"}, // turn 2: request at 310 has no thinking figure
		{500, "steered"}, // turn 3: open until the end of the session
	}
	reqs := []thinkReq{
		{50, th(9999)}, // before any routine turn
		{110, th(40)}, {150, th(60)},
		{250, th(7777)}, // after turn 1 closed: a non-routine turn
		{310, sql.NullInt64{}},
		{450, th(5555)}, // the prompt after turn 2's own turn
		{510, th(10)}, {900, th(20)},
	}
	s := steerTurns(evs, reqs)
	// Turn 1: 100; turn 2: no figure, left out; turn 3: 30.
	if s.turns != 2 || s.thinking != 130 || s.closed != 2 || s.reprompted != 1 {
		t.Errorf("got %+v", s)
	}
}

// A turn that ends where the next routine turn starts (no turn_end in between, which the hook never writes but a
// damaged spool could) is still bounded.
func TestSteerTurnsBoundedByTheNextRoutineTurn(t *testing.T) {
	s := steerTurns([]steerEvent{{100, "steered"}, {200, "steered"}}, []thinkReq{{150, th(5)}, {250, th(7)}})
	if s.turns != 2 || s.thinking != 12 {
		t.Errorf("got %+v", s)
	}
}

func TestSteerTrialByArmReadsFinishedSessions(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	since := t0.UnixMilli()
	const opt = "hook_routine_steer"
	at := func(d time.Duration) int64 { return t0.Add(d).UnixMilli() }
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range []struct{ key, arm string }{{"c-on1", "on"}, {"c-on2", "on"}, {"c-off1", "off"}, {"c-run", "on"}, {"c-none", "off"}} {
		exec(`INSERT INTO experiment_assignments(session_key, optimizer, arm, stratum, seq, assigned_at) VALUES(?,?,?,'',0,?)`, a.key, opt, a.arm, since+1)
	}
	ev := func(key string, d time.Duration, kind string) {
		exec(`INSERT INTO hook_events(ts, session_key, hook, kind) VALUES(?,?,?,?)`, at(d), key, opt, kind)
	}
	n := 0
	req := func(key string, d time.Duration, thinking any, sidechain int, agent any) {
		n++
		exec(`INSERT INTO history_requests(request_key, source, session_key, ts, thinking_tokens, sidechain, agent_key) VALUES(?,?,?,?,?,?,?)`,
			fmt.Sprintf("r%d", n), "claude_code", key, at(d), thinking, sidechain, agent)
	}
	// c-on1: two routine turns, 100 and 50 thinking; a subagent's request inside the window is not counted.
	ev("c-on1", time.Minute, "steered")
	req("c-on1", time.Minute+time.Second, 100, 0, nil)
	req("c-on1", time.Minute+2*time.Second, 5000, 1, "agent-x")
	ev("c-on1", 2*time.Minute, "turn_end")
	ev("c-on1", 3*time.Minute, "steered")
	req("c-on1", 3*time.Minute+time.Second, 50, 0, nil)
	ev("c-on1", 4*time.Minute, "turn_end")
	ev("c-on1", 4*time.Minute, "reprompted")
	req("c-on1", 4*time.Minute+time.Second, 900, 0, nil)
	// c-on2 (a split gateway key): one turn of 30.
	ev("c-on2~1", time.Minute, "steered")
	req("c-on2~1", time.Minute+time.Second, 30, 0, nil)
	// c-off1: one turn of 400, closed without a correction.
	ev("c-off1", time.Minute, "would_steer")
	req("c-off1", time.Minute+time.Second, 400, 0, nil)
	ev("c-off1", 2*time.Minute, "turn_end")
	// c-run: still running at the cutoff.
	ev("c-run", time.Minute, "steered")
	req("c-run", 50*time.Minute, 1, 0, nil)
	// c-none has no routine turn at all.

	on, off, err := SteerTrialByArm(ctx, db, opt, since, at(30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if on.Sessions != 2 || on.Turns != 3 || on.Closed != 2 || on.Reprompted != 1 || on.Mean() != (75+30)/2.0 {
		t.Errorf("on = %+v, mean %g", on, on.Mean())
	}
	if off.Sessions != 1 || off.Turns != 1 || off.Closed != 1 || off.Reprompted != 0 || off.Mean() != 400 {
		t.Errorf("off = %+v, mean %g", off, off.Mean())
	}
}

func arm(means []float64, closed, reprompted int) SteerArm {
	return SteerArm{Sessions: len(means), Turns: len(means) * 3, PerSession: means, Closed: closed, Reprompted: reprompted}
}

func spread(center float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = center + float64(i%5-2)*center*0.1
	}
	return out
}

func TestJudgeSteerTrial(t *testing.T) {
	cases := []struct {
		name          string
		on, off       SteerArm
		enough, saves bool
		worse, trips  bool
	}{
		{"too few sessions", arm(spread(100, 5), 0, 0), arm(spread(400, 5), 0, 0), false, false, false, false},
		{"a clear saving", arm(spread(100, 30), 60, 3), arm(spread(400, 30), 60, 3), true, true, false, false},
		{"no difference", arm(spread(400, 30), 60, 3), arm(spread(400, 30), 60, 3), true, false, false, false},
		{"more thinking with it", arm(spread(700, 30), 60, 3), arm(spread(400, 30), 60, 3), true, false, true, false},
		{"many more re-prompts", arm(spread(100, 30), 200, 120), arm(spread(400, 30), 200, 10), true, true, false, true},
		{"a few more re-prompts, within the margin", arm(spread(100, 30), 200, 20), arm(spread(400, 30), 200, 10), true, true, false, false},
	}
	for _, c := range cases {
		v := JudgeSteerTrial(c.on, c.off, 20, 10)
		if v.Enough != c.enough || v.Saves != c.saves || v.Worse != c.worse || v.RepromptTrips != c.trips {
			t.Errorf("%s: %+v", c.name, v)
		}
		if v.RepromptTrips && !strings.Contains(v.RepromptWhy, "(measured)") {
			t.Errorf("%s: reason not labeled: %s", c.name, v.RepromptWhy)
		}
		if txt := v.Text(); txt == "" || (v.Enough && !strings.Contains(txt, "(measured")) {
			t.Errorf("%s: text %q", c.name, txt)
		}
	}
}

func TestSteerLimitsNeedsAFit(t *testing.T) {
	if e := SteerLimits(t.TempDir(), "claude-opus-5-5", 300, t0); e != nil {
		t.Errorf("no fit, but got %+v", e)
	}
	if e := SteerLimits(t.TempDir(), "claude-opus-5-5", -5, t0); e != nil {
		t.Errorf("a negative saving is not a share of the limits: %+v", e)
	}
}
