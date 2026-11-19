package measure

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestTestFailByArmCountsTheLastRunOfFinishedSessions(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	since := t0.UnixMilli()
	asg := []struct {
		key, arm string
		at       int64
	}{
		{"on1", "on", since + 1}, {"on2", "on", since + 1}, {"on3", "on", since + 1}, {"on4", "on", since + 1},
		{"off1", "off", since + 1}, {"off2", "off", since + 1}, {"off3", "off", since + 1},
		{"old", "on", since - 1}, // assigned before the trial: left out
	}
	for _, a := range asg {
		if _, err := db.ExecContext(ctx, `INSERT INTO experiment_assignments(session_key, optimizer, arm, stratum, seq, assigned_at) VALUES(?,?,?,'',0,?)`,
			a.key, "hook_x", a.arm, a.at); err != nil {
			t.Fatal(err)
		}
	}
	runs := []struct {
		key  string
		at   time.Duration
		exit int
	}{
		{"on1", time.Minute, 0}, {"on1", 2 * time.Minute, 1}, // ends failing
		{"on2~1", time.Minute, 1},                            // a split gateway key still counts; ends failing
		{"on3", time.Minute, 1}, {"on3", 2 * time.Minute, 0}, // ends passing
		{"on4", 50 * time.Minute, 1}, // still running at the cutoff: left out
		{"off1", time.Minute, 0},
		{"off2", time.Minute, 1}, {"off2", 2 * time.Minute, 0},
		// off3 ran no test: not counted
		{"old", time.Minute, 1},
	}
	for _, r := range runs {
		if _, err := db.ExecContext(ctx, `INSERT INTO test_runs(ts, session_key, cmd_hash, exit_code) VALUES(?,?,?,?)`,
			t0.Add(r.at).UnixMilli(), r.key, "0123456789abcdef", r.exit); err != nil {
			t.Fatal(err)
		}
	}
	on, off, err := TestFailByArm(ctx, db, "hook_x", since, t0.Add(30*time.Minute).UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if want := (TestFailArm{Sessions: 3, Failed: 2}); on != want {
		t.Errorf("on = %+v; want %+v", on, want)
	}
	if want := (TestFailArm{Sessions: 2, Failed: 0}); off != want {
		t.Errorf("off = %+v; want %+v", off, want)
	}
}

func TestTestFailTrips(t *testing.T) {
	arm := func(n, f int) TestFailArm { return TestFailArm{Sessions: n, Failed: f} }
	cases := []struct {
		name    string
		on, off TestFailArm
		margin  float64
		trip    bool
	}{
		{"too few sessions on one side", arm(9, 9), arm(10, 0), 10, false},
		{"worse by more than the margin", arm(10, 4), arm(10, 2), 10, true},
		{"worse by exactly the margin", arm(10, 3), arm(10, 2), 10, false},
		{"stricter margin trips on a smaller gap", arm(20, 4), arm(20, 2), 5, true},
		{"the same gap passes a looser margin", arm(20, 4), arm(20, 2), 10, false},
		{"better with it on", arm(10, 0), arm(10, 5), 10, false},
	}
	for _, c := range cases {
		trip, why := TestFailTrips(c.on, c.off, 10, c.margin)
		if trip != c.trip {
			t.Errorf("%s: trip = %v (%q); want %v", c.name, trip, why, c.trip)
		}
		if trip && !strings.Contains(why, "measured") {
			t.Errorf("%s: reason %q is not labeled measured", c.name, why)
		}
	}
}
