package hooks

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/features"
	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/store"
)

const (
	tuneCwd  = "/home/u/proj" // the cwd in the testdata fixtures
	tuneSalt = "test-salt"
)

func tunedRig(t *testing.T, tune map[string]optimize.Tuning) *rig {
	t.Helper()
	noClock(t)
	r := newRig(t, nil)
	r.snapshot(Snapshot{States: map[string]string{rwTrim: "on", rwNoise: "on"}, TroveRun: true, RepoSalt: tuneSalt, Tuning: tune})
	r.allow(Capability{Tool: "Bash", OutputRewrite: CapYes}, Capability{Tool: "Grep", OutputRewrite: CapYes})
	return r
}

func bigTunedKey(v int) optimize.Tuning {
	return optimize.Tuning{optimize.TuningKey(config.OptimizerOutputTrimmer, optimize.ParamMinBytes): v}
}

func TestSessionPinsTheReposTuningAndKeepsItForTheSession(t *testing.T) {
	full, _ := goTestOutput(400)
	repo := features.RepoKey(tuneCwd, tuneSalt)

	// This repo has min_bytes tuned above the output: nothing is trimmed.
	r := tunedRig(t, map[string]optimize.Tuning{repo: bigTunedKey(50000)})
	r.start()
	if out, code := r.post("claude_post_bash.json", full, false); code != 0 || out != "" {
		t.Fatalf("with min_bytes tuned above the output nothing should be trimmed: code=%d out=%q", code, out)
	}

	// The snapshot changes mid-session (tuning removed): the running session keeps its pin.
	r.snapshot(Snapshot{States: map[string]string{rwTrim: "on", rwNoise: "on"}, TroveRun: true, RepoSalt: tuneSalt})
	if out, _ := r.post("claude_post_bash.json", full, false); out != "" {
		t.Error("a session must keep the tuning it started with (rule 5)")
	}

	// A new session starts after the change and gets the defaults.
	if err := os.Remove(sessionPath(r.dir, r.key())); err != nil {
		t.Fatal(err)
	}
	r.start()
	if out, _ := r.post("claude_post_bash.json", full, false); out == "" {
		t.Error("a new session without tuning should trim with the defaults")
	}
}

func TestTuningOfAnotherRepoDoesNotApply(t *testing.T) {
	full, _ := goTestOutput(400)
	r := tunedRig(t, map[string]optimize.Tuning{features.RepoKey("/somewhere/else", tuneSalt): bigTunedKey(50000)})
	r.start()
	if out, _ := r.post("claude_post_bash.json", full, false); out == "" {
		t.Error("tuning for another repo must not apply here")
	}
}

func TestPinnedTuningIgnoresGarbageAndStates(t *testing.T) {
	got := pinnedTuning(map[string]string{
		"hook_repeat_read":           "on",
		"t:output_trimmer.keep_tail": "15",
		"t:output_trimmer.keep_head": "lots",
		"output_trimmer":             "shadow",
	})
	if len(got) != 1 || got["output_trimmer.keep_tail"] != 15 {
		t.Errorf("got %v", got)
	}
	if pinnedTuning(nil) != nil || pinnedTuning(map[string]string{"output_trimmer": "on"}) != nil {
		t.Error("no tuned pins should give nil, so defaults apply")
	}
}

func TestMaintainPublishesAppliedTuningToTheSnapshotAndReviewsIt(t *testing.T) {
	r := newRig(t, nil)
	db, err := store.Open(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	now := time.Now()
	salt, err := db.Features().Salt(ctx)
	if err != nil {
		t.Fatal(err)
	}
	repo := features.RepoKey(tuneCwd, salt)
	row := store.TuningRow{RepoKey: repo, Optimizer: config.OptimizerOutputTrimmer, Param: optimize.ParamKeepTail, Value: 15,
		TunedAt: now, Source: "local", BeforeRate: 0.4, NBefore: 40}
	if err := db.Tuning().Suggest(ctx, []store.TuningRow{row}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	if err := Maintain(ctx, db, r.dir, cfg, r.env, now); err != nil {
		t.Fatal(err)
	}
	if snap, _ := LoadSnapshot(r.dir); len(snap.Tuning) != 0 {
		t.Errorf("a suggestion must not reach the snapshot: %+v", snap.Tuning)
	}
	if _, err := db.Tuning().Apply(ctx, repo, now); err != nil {
		t.Fatal(err)
	}
	if err := Maintain(ctx, db, r.dir, cfg, r.env, now); err != nil {
		t.Fatal(err)
	}
	snap, _ := LoadSnapshot(r.dir)
	if snap.RepoSalt != salt || snap.Tuning[repo][optimize.TuningKey(config.OptimizerOutputTrimmer, optimize.ParamKeepTail)] != 15 {
		t.Errorf("snapshot = %+v", snap)
	}

	// Trimmed outputs since applying, with no fall in the fetch-back share: Maintain reverts it.
	if err := db.Features().PutSession(ctx, store.SessionFeaturesRow{SessionKey: "s", RepoKey: repo, RecordedAt: now}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ { // 20 trimmed outputs (the review's minimum), 20 fetched back
		kind := "rewritten"
		if i < 20 {
			kind = "fetched_back"
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO hook_events (ts, session_key, hook, kind, bytes, tokens) VALUES (?,?,?,?,0,0)`,
			now.Add(time.Second).UnixMilli(), "s", config.OptimizerOutputTrimmer, kind); err != nil {
			t.Fatal(err)
		}
	}
	if err := Maintain(ctx, db, r.dir, cfg, r.env, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if snap, _ := LoadSnapshot(r.dir); len(snap.Tuning) != 0 {
		t.Errorf("tuning that did not help must be gone from the snapshot: %+v", snap.Tuning)
	}
}
