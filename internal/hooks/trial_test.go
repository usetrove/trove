package hooks

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/gateway/session"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/store"
)

const trialHook = config.OptimizerHookRepeatRead

// startAs runs SessionStart for a session with the given id and returns its key.
func (r *rig) startAs(id string) string {
	r.t.Helper()
	if out, code := r.run(EventReset, r.input("claude_session_start.json", "{{SESSION}}", id)); out != "" || code != 0 {
		r.t.Fatalf("session start: out=%q code=%d", out, code)
	}
	return session.ClaudeCodeKey(id)
}

func (r *rig) armEvents() map[string][]string {
	r.t.Helper()
	out := map[string][]string{}
	for _, e := range r.events() {
		if e.Kind == KindArmOn || e.Kind == KindArmOff {
			out[e.Key] = append(out[e.Key], e.Kind)
		}
	}
	return out
}

func trialSnapshot(rolledBack ...string) Snapshot {
	return Snapshot{
		States:     map[string]string{trialHook: stateShadow, config.OptimizerNoiseNormalizer: stateOn},
		Trial:      []string{trialHook},
		RolledBack: rolledBack,
	}
}

// An optimizer on trial is on for about half of sessions, chosen by a hash of the session key, and each
// session's side is recorded when it starts.
func TestSessionsOnTrialAreSplitAndRecorded(t *testing.T) {
	r := newRig(t, nil)
	r.snapshot(trialSnapshot())
	on := 0
	const n = 60
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("trial-session-%d", i)
		key := r.startAs(id)
		want := measure.Arm(key, trialHook)
		got := loadSession(r.dir, key).Pins[trialHook]
		if (want == config.StateOn) != (got == stateOn) {
			t.Fatalf("session %d: arm %q but pinned %q", i, want, got)
		}
		if got == stateOn {
			on++
		}
		if n := loadSession(r.dir, key).Pins[config.OptimizerNoiseNormalizer]; n != stateOn {
			t.Fatalf("an optimizer not on trial must keep its state, got %q", n)
		}
	}
	if on < 15 || on > 45 {
		t.Errorf("%d of %d sessions on; a random half was intended", on, n)
	}
	arms := r.armEvents()
	if len(arms) != n {
		t.Fatalf("%d sessions recorded an arm, want %d", len(arms), n)
	}
	for key, kinds := range arms {
		want := KindArmOff
		if measure.Arm(key, trialHook) == config.StateOn {
			want = KindArmOn
		}
		if len(kinds) != 1 || kinds[0] != want {
			t.Errorf("session recorded %v, want one %s", kinds, want)
		}
	}
}

// A session already pinned (a resume or a second SessionStart) is never assigned again: an arm cannot change.
func TestASessionIsAssignedOnlyOnce(t *testing.T) {
	r := newRig(t, nil)
	r.snapshot(trialSnapshot())
	key := r.startAs("once")
	first := loadSession(r.dir, key).Pins[trialHook]
	r.snapshot(Snapshot{States: map[string]string{trialHook: stateShadow}, RolledBack: []string{}}) // trial over
	r.startAs("once")
	if got := loadSession(r.dir, key).Pins[trialHook]; got != first {
		t.Errorf("pin changed from %q to %q mid-session", first, got)
	}
	if n := len(r.armEvents()[key]); n != 1 {
		t.Errorf("%d arm events for one session", n)
	}
}

// With no trial there is nothing to assign or record, and a rollback still lowers an "on" arm to shadow.
func TestNoTrialMeansNoAssignmentAndARollbackStillLowersIt(t *testing.T) {
	r := newRig(t, map[string]string{trialHook: stateShadow})
	r.startAs("plain")
	if len(r.armEvents()) != 0 {
		t.Error("no optimizer is on trial, yet an arm was recorded")
	}
	r2 := newRig(t, nil)
	r2.snapshot(trialSnapshot(trialHook))
	for i := 0; i < 20; i++ {
		key := r2.startAs(fmt.Sprintf("rb-%d", i))
		if got := loadSession(r2.dir, key).Pins[trialHook]; got == stateOn {
			t.Fatalf("a rolled-back optimizer was pinned on for session %d", i)
		}
	}
}

// The arms the hooks record reach experiment_assignments, where the comparison reads them, and not hook_events.
func TestArmRecordsAreIngestedAsAssignments(t *testing.T) {
	r := newRig(t, nil)
	r.snapshot(trialSnapshot())
	for i := 0; i < 12; i++ {
		r.startAs(fmt.Sprintf("ing-%d", i))
	}
	db, err := store.Open(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := IngestSpool(context.Background(), db, r.dir); err != nil {
		t.Fatal(err)
	}
	var n, onN int
	_ = db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(arm = 'on'), 0) FROM experiment_assignments WHERE optimizer = ?`, trialHook).Scan(&n, &onN)
	if n != 12 {
		t.Fatalf("%d assignments ingested, want 12", n)
	}
	var ev int
	_ = db.QueryRow(`SELECT COUNT(*) FROM hook_events`).Scan(&ev)
	if ev != 0 {
		t.Errorf("arm records leaked into hook_events (%d rows); they would count as decisions", ev)
	}
	// A second record for the same session and optimizer, with the other arm, must not overwrite the first.
	var key, arm string
	if err := db.QueryRow(`SELECT session_key, arm FROM experiment_assignments WHERE optimizer = ? LIMIT 1`, trialHook).Scan(&key, &arm); err != nil {
		t.Fatal(err)
	}
	other := KindArmOn
	if arm == "on" {
		other = KindArmOff
	}
	if err := record(r.dir, event{T: r.now.UnixMilli() + 1000, Key: key, Hook: trialHook, Kind: other}); err != nil {
		t.Fatal(err)
	}
	if _, err := IngestSpool(context.Background(), db, r.dir); err != nil {
		t.Fatal(err)
	}
	var after string
	_ = db.QueryRow(`SELECT arm FROM experiment_assignments WHERE session_key = ? AND optimizer = ?`, key, trialHook).Scan(&after)
	if after != arm {
		t.Errorf("a repeated record changed the arm from %q to %q", arm, after)
	}
}

func TestTheSnapshotListsOnlyTrialsTheHooksMayRun(t *testing.T) {
	f := config.AutopilotFile{Enabled: true, Policy: config.AutopilotPolicyV2, Entries: map[string]config.AutopilotEntry{
		trialHook:                         {State: config.AutopilotProbation},
		config.OptimizerHookQuietCommands: {State: config.AutopilotProbation},
		"volatile_prefix_mover":           {State: config.AutopilotProbation}, // a gateway optimizer, not the hooks'
	}}
	cfg := config.Default().WithAutopilot(f)
	snap := BuildSnapshot(cfg, nil, false, time.Now())
	if got := strings.Join(snap.Trial, ","); got != config.OptimizerHookQuietCommands+","+trialHook && got != trialHook+","+config.OptimizerHookQuietCommands {
		t.Errorf("Trial = %q", got)
	}
	if snap.States[trialHook] != stateShadow {
		t.Errorf("an optimizer on trial is shadow in the snapshot's states, got %q", snap.States[trialHook])
	}
	off := config.Default()
	off.Experiment.Enabled = false
	if got := BuildSnapshot(off.WithAutopilot(f), nil, false, time.Now()).Trial; len(got) != 0 {
		t.Errorf("A/B comparison is off, yet %v are on trial", got)
	}
	mine := config.Default()
	mine.Optimizers = map[string]string{trialHook: config.StateOff}
	if got := BuildSnapshot(mine.WithAutopilot(f), nil, false, time.Now()).Trial; strings.Contains(strings.Join(got, ","), trialHook) {
		t.Errorf("the user's own setting must win, yet %v", got)
	}
}
