package measure

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
)

func TestRollupCarriesTheTestPassSignal(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	for _, k := range []string{"F", "P", "N"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO sessions(session_key, agent, started_at, ended_at, request_count) VALUES(?,'x',?,?,3)`,
			k, t0.UnixMilli(), t0.Add(10*time.Minute).UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	// F passed, then failed last. P failed, then passed last. N never ran an allowlisted test.
	runs := []struct {
		key  string
		at   time.Duration
		exit int
	}{{"F", time.Minute, 0}, {"F", 2 * time.Minute, 1}, {"P", time.Minute, 2}, {"P", 3 * time.Minute, 0}, {"P", 2 * time.Minute, 1}}
	for _, r := range runs {
		if _, err := db.ExecContext(ctx, `INSERT INTO test_runs(ts, session_key, cmd_hash, exit_code) VALUES(?,?,?,?)`,
			t0.Add(r.at).UnixMilli(), r.key, "0123456789abcdef", r.exit); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := RollupSessions(ctx, db, config.Default().Measure, t0.Add(time.Hour), 30*time.Minute); err != nil {
		t.Fatal(err)
	}
	get := func(k string) SessionSignals {
		var raw string
		if err := db.QueryRowContext(ctx, `SELECT outcome_signals FROM sessions WHERE session_key = ?`, k).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var s SessionSignals
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	want := map[string]TestSignal{
		"F": {Last: TestFail, Runs: 2, Failed: 1},
		"P": {Last: TestPass, Runs: 3, Failed: 2},
		"N": {Last: TestNone},
	}
	for k, w := range want {
		if got := get(k).Tests; got != w {
			t.Errorf("session %s tests = %+v; want %+v", k, got, w)
		}
	}
}
