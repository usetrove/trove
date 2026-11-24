package measure

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestCodeTrialByArmReadsFinishedSessions(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	since := t0.UnixMilli()
	const opt = "ext_ponytail"
	at := func(d time.Duration) int64 { return t0.Add(d).UnixMilli() }
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range []struct{ key, arm string }{{"k-on", "on"}, {"k-off", "off"}, {"k-run", "on"}, {"k-none", "off"}, {"k-talk", "off"}} {
		exec(`INSERT INTO experiment_assignments(session_key, optimizer, arm, stratum, seq, assigned_at) VALUES(?,?,?,'',0,?)`, a.key, opt, a.arm, since+1)
	}
	req := func(rk, key string, d time.Duration, model string) {
		exec(`INSERT INTO history_requests(request_key, source, session_key, ts, model) VALUES(?,?,?,?,?)`, rk, "claude_code", key, at(d), model)
	}
	tool := func(rk string, idx int, name string, bytes int64) {
		exec(`INSERT INTO history_tool_calls(request_key, idx, ts, tool_name, input_bytes) VALUES(?,?,?,?,?)`, rk, idx, since, name, bytes)
	}
	req("r1", "k-on", time.Minute, "claude-opus-5-5")
	tool("r1", 0, "Write", 700)
	tool("r1", 1, "Read", 99999)                          // not code
	req("r2", "k-on~1", 2*time.Minute, "claude-opus-5-5") // a gateway split of the same session
	tool("r2", 0, "Edit", 350)
	req("r3", "k-off", time.Minute, "claude-opus-5-5")
	tool("r3", 0, "MultiEdit", 1400)
	req("r4", "k-run", 10*time.Hour, "claude-sonnet-5-5") // still running at the cutoff
	tool("r4", 0, "Write", 5000)
	req("r5", "k-talk", time.Minute, "claude-opus-5-5") // wrote no code: counted as zero
	on, off, model, err := CodeTrialByArm(ctx, db, opt, since, at(5*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if on.Sessions != 1 || on.Coding != 1 || on.PerSession[0] != 1050 {
		t.Errorf("on arm: %+v", on)
	}
	if off.Sessions != 2 || off.Coding != 1 || off.PerSession[0] != 1400 || off.PerSession[1] != 0 {
		t.Errorf("off arm: %+v (k-none has no history and is left out; k-talk counts as zero)", off)
	}
	if model != "claude-opus-5-5" {
		t.Errorf("model = %q", model)
	}
}

func TestJudgeCodeTrial(t *testing.T) {
	arm := func(base float64, n int) CodeArm {
		a := CodeArm{Sessions: n, Coding: n}
		for i := 0; i < n; i++ {
			a.PerSession = append(a.PerSession, base*(1+0.05*float64(i%5)))
		}
		return a
	}
	tests := TestFailArm{Sessions: 40, Failed: 4}
	cases := []struct {
		name         string
		on, off      CodeArm
		enough, save bool
		worse        bool
	}{
		{"too few", arm(1000, 5), arm(2000, 80), false, false, false},
		{"half the code", arm(1000, 80), arm(2000, 80), true, true, false},
		{"more code", arm(3000, 80), arm(2000, 80), true, false, true},
		{"same", arm(2000, 80), arm(2000, 80), true, false, false},
	}
	for _, c := range cases {
		v := JudgeCodeTrial(c.on, c.off, tests, tests, 20)
		if v.Enough != c.enough || v.Saves != c.save || v.Worse != c.worse {
			t.Errorf("%s: %+v", c.name, v)
		}
	}
	v := JudgeCodeTrial(arm(1050, 80), arm(2100, 80), tests, TestFailArm{}, 20)
	txt := v.Text()
	for _, want := range []string{"(estimated from tool-input sizes", "90% of sessions that ran them with the rules and n/a without (measured"} {
		if !strings.Contains(txt, want) {
			t.Errorf("text lacks %q: %s", want, txt)
		}
	}
	if v.SavedPerSession <= 0 {
		t.Errorf("saved per session = %v", v.SavedPerSession)
	}
}
