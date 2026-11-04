package external

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/store"
)

func TestChargeCountsRulesTextNotTheManifestClaim(t *testing.T) {
	m := rulesTool("r")
	m.Rules = strings.Repeat("x", 350)
	m.Overhead.InjectedTokens = 1 // a manifest cannot understate what it injects
	c := ChargeFor(m)
	if c.InjectedTokens != 100 || c.PerRequest != 100 || c.Label != "estimated" {
		t.Fatalf("charge = %+v", c)
	}
	s := shellTool("s", "MIT")
	s.Overhead = Overhead{ToolDefTokens: 900}
	if c := ChargeFor(s); c.PerRequest != 900 || c.Source != "declared" {
		t.Fatalf("declared charge = %+v", c)
	}
}

func TestGuardSwitchesOffAToolThatCostsMore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "d"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	claude := t.TempDir()
	path := filepath.Join(claude, "CLAUDE.md")
	orig := "# mine\n"
	_ = os.WriteFile(path, []byte(orig), 0o644)

	env := Env{DataDir: filepath.Join(dir, "d"), ClaudeDir: claude, Runner: &fakeRunner{}, Store: db.ExternalTools(),
		Manifests: []Manifest{rulesTool("rules")}, Now: func() time.Time { return time.Unix(1_800_000_000, 0) }}
	if err := env.Install(ctx, "rules", Approval{Tool: "rules"}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Enable(ctx, "rules"); err != nil {
		t.Fatal(err)
	}
	t0 := time.Unix(1_700_000_000, 0)
	add := func(i int, on bool, cost float64) {
		key := fmt.Sprintf("s%d", i)
		st := "off"
		if on {
			st = "on"
		}
		at := t0.Add(time.Duration(i) * time.Hour).UnixMilli()
		if _, err := db.ExecContext(ctx, `INSERT INTO sessions(session_key, agent, started_at, request_count, total_cost_usd) VALUES(?, 'a', ?, 10, ?)`, key, at, cost); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO session_pins(session_key, states, bytes_per_token, pinned_at) VALUES(?,?,3.5,?)`, key, `{"ext_rules":"`+st+`"}`, at); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 25; i++ {
		add(i, true, 1.5+float64(i%5)*0.01)
	}
	for i := 100; i < 125; i++ {
		add(i, false, 1.0+float64(i%5)*0.01)
	}
	rb, err := env.Guard(ctx, db, config.Default().Measure)
	if err != nil || len(rb) != 1 || rb[0].Err != nil || rb[0].Name != "rules" {
		t.Fatalf("rb=%+v err=%v", rb, err)
	}
	if got, _ := os.ReadFile(path); string(got) != orig {
		t.Fatalf("file not restored:\n%q", got)
	}
	rec, ok, _ := env.Store.Get(ctx, "rules")
	if !ok || rec.State != "off" {
		t.Fatalf("tool should stay installed but off: %+v", rec)
	}
	evs, _ := db.QueryContext(ctx, `SELECT COUNT(*) FROM guardrail_events WHERE optimizer='ext_rules' AND rule='net_cost'`)
	var n int
	if evs.Next() {
		_ = evs.Scan(&n)
	}
	_ = evs.Close()
	if n != 1 {
		t.Fatalf("want one recorded guardrail event, got %d", n)
	}
	if again, _ := env.Guard(ctx, db, config.Default().Measure); len(again) != 0 {
		t.Fatalf("nothing is enabled any more, got %+v", again)
	}
}

func TestActiveStates(t *testing.T) {
	s := &memStore{m: map[string]store.ExternalTool{"a": {Name: "a", State: "on"}, "b": {Name: "b", State: "off"}}}
	got := ActiveStates(context.Background(), s)
	if got["ext_a"] != "on" || got["ext_b"] != "off" || len(got) != 2 {
		t.Fatalf("got %v", got)
	}
}
