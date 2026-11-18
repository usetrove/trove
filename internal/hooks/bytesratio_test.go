package hooks

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/store"
)

// shadowRepeat reads a 7,000-byte file twice in shadow mode and returns the would_block event.
func shadowRepeat(t *testing.T, r *rig, startInput string) event {
	t.Helper()
	p := r.file("a.go", strings.Repeat("x", 7000))
	if out, code := r.run(EventReset, startInput); out != "" || code != 0 {
		t.Fatalf("session start: out=%q code=%d", out, code)
	}
	r.read(p, "p1")
	r.read(p, "p2")
	es := r.events()
	if len(es) != 1 || es[0].Kind != KindWouldBlock || es[0].Bytes != 7000 {
		t.Fatalf("events = %+v", es)
	}
	return es[0]
}

func ratioRig(t *testing.T, ratios map[string]measure.BytesRatio) *rig {
	r := newRig(t, nil)
	r.snapshot(Snapshot{States: map[string]string{config.OptimizerHookRepeatRead: "shadow"}, BytesPerToken: ratios})
	return r
}

func TestHookEstimateUsesTheLearnedRatioOfTheSessionsModelFamily(t *testing.T) {
	r := ratioRig(t, map[string]measure.BytesRatio{
		"claude-sonnet": {BytesPerToken: 7, Samples: 50},
		"claude-opus":   {BytesPerToken: 2, Samples: 90},
	})
	e := shadowRepeat(t, r, r.input("claude_session_start.json")) // the fixture's model is claude-sonnet-5-5
	if e.Tokens != 1000 || e.Ratio != 7 || e.Samples != 50 {
		t.Errorf("event = %+v, want 1000 tokens at 7 bytes per token from 50 samples", e)
	}
}

func TestHookEstimateKeepsTheDefaultWhenNothingWasLearned(t *testing.T) {
	for name, tc := range map[string]struct {
		ratios map[string]measure.BytesRatio
		start  func(*rig) string
	}{
		"no snapshot ratios": {nil, func(r *rig) string { return r.input("claude_session_start.json") }},
		"another family only": {map[string]measure.BytesRatio{"claude-opus": {BytesPerToken: 2, Samples: 90}},
			func(r *rig) string { return r.input("claude_session_start.json") }},
		"the session start named no model": {map[string]measure.BytesRatio{"claude-sonnet": {BytesPerToken: 7, Samples: 50}},
			func(r *rig) string {
				return strings.Replace(r.input("claude_session_start.json"), `,"model":"claude-sonnet-5-5"`, "", 1)
			}},
		"a nonsense ratio": {map[string]measure.BytesRatio{"claude-sonnet": {BytesPerToken: 0, Samples: 50}},
			func(r *rig) string { return r.input("claude_session_start.json") }},
	} {
		t.Run(name, func(t *testing.T) {
			r := ratioRig(t, tc.ratios)
			e := shadowRepeat(t, r, tc.start(r))
			if e.Tokens != 2000 || e.Ratio != 0 || e.Samples != 0 {
				t.Errorf("event = %+v, want 2000 tokens at the default 3.5 with no ratio recorded", e)
			}
		})
	}
}

// A session keeps the ratio of the family it started with, even if the snapshot is rewritten (rule 5).
func TestHookRatioIsPinnedWithTheSession(t *testing.T) {
	r := ratioRig(t, map[string]measure.BytesRatio{"claude-sonnet": {BytesPerToken: 7, Samples: 50}})
	p := r.file("a.go", strings.Repeat("x", 7000))
	r.start()
	r.read(p, "p1")
	r.snapshot(Snapshot{States: map[string]string{config.OptimizerHookRepeatRead: "shadow"},
		BytesPerToken: map[string]measure.BytesRatio{"claude-opus": {BytesPerToken: 2, Samples: 90}}})
	r.read(p, "p2")
	es := r.events()
	if len(es) != 1 || es[0].Tokens != 1000 || es[0].Ratio != 7 {
		t.Fatalf("the session switched ratio mid-session: %+v", es)
	}
}

func TestSnapshotCarriesLearnedRatios(t *testing.T) {
	r := newRig(t, nil)
	want := map[string]measure.BytesRatio{"claude-sonnet": {BytesPerToken: 4.25, Samples: 31}}
	r.snapshot(Snapshot{BytesPerToken: want})
	got, ok := LoadSnapshot(r.dir)
	if !ok || got.BytesPerToken["claude-sonnet"] != want["claude-sonnet"] {
		t.Errorf("round trip lost the ratios: %+v", got)
	}
}

func TestMaintainPublishesLearnedRatios(t *testing.T) {
	r := newRig(t, nil)
	db, err := store.Open(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	now := time.Now()
	// 25 pairs, each 8,000 bytes of tool results answered by 2,000 new tokens: 4 bytes per token.
	var prompt int64 = 10_000
	for i := 0; i <= 25; i++ {
		read, write := prompt, int64(2100)
		if i == 0 {
			read, write = 0, prompt
		} else {
			prompt += write
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO history_requests
			(request_key, source, session_key, ts, model, input_tokens, cache_write_5m, cache_read, output_tokens, sidechain, tool_result_bytes)
			VALUES (?,?,?,?,?,0,?,?,100,0,8000)`,
			fmt.Sprintf("k%d", i), "claude_code", "s", now.Add(-time.Hour+time.Duration(i)*time.Second).UnixMilli(), "claude-sonnet-5-5", write, read); err != nil {
			t.Fatal(err)
		}
	}
	if err := Maintain(ctx, db, r.dir, config.Default(), r.env, now); err != nil {
		t.Fatal(err)
	}
	snap, _ := LoadSnapshot(r.dir)
	if got := snap.BytesPerToken["claude-sonnet"]; got.Samples != 25 || got.BytesPerToken < 3.99 || got.BytesPerToken > 4.01 {
		t.Errorf("snapshot ratios = %+v", snap.BytesPerToken)
	}
}
