package hooks

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/hookcaps"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/store"
)

// Autopilot reads the hooks' shadow evidence through measure.HookShadowEstimates. That function trusts
// the name and kind the hooks write. This test runs the real hook on a noisy command output in shadow
// mode, takes the event through the real spool into a real database, and reads it back with the adapter,
// so a rename on either side fails here and not silently in a user's Autopilot.
func TestShadowEventsAreWhatAutopilotsAdapterReads(t *testing.T) {
	noClock(t)
	r := newRig(t, nil)
	r.snapshot(Snapshot{States: map[string]string{rwNoise: "shadow"}})
	r.allow(Capability{Tool: "Bash", OutputRewrite: CapYes})
	r.start()
	noisy := "\x1b[31mERROR\x1b[0m connection refused\n" + strings.Repeat("retrying the connection\n", 60) + "done\n"
	if out, _ := r.post("claude_post_bash.json", noisy, false); out != "" {
		t.Fatalf("shadow mode must not change the output, got %q", out)
	}
	if got := r.kinds(); len(got) == 0 || got[len(got)-1] != rwNoise+":would_rewrite" {
		t.Fatalf("the hook wrote %v; the adapter reads would_rewrite events named %s", got, rwNoise)
	}

	db, err := store.Open(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	if _, err := IngestSpool(ctx, db, r.dir); err != nil {
		t.Fatal(err)
	}
	w := measure.Window{From: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)}
	got, err := measure.HookShadowEstimates(ctx, db, w)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := got[rwNoise]
	if !ok || e.Sessions != 1 || e.Events < 1 || e.Tokens <= 0 {
		t.Fatalf("the adapter found %+v for %s in %v; want one session with a positive estimate", e, rwNoise, got)
	}
	if n, err := measure.HookSessions(ctx, db); err != nil || n != 1 {
		t.Errorf("HookSessions = %d, %v; want 1", n, err)
	}
	if tr, err := measure.TrafficSeen(ctx, db, w); err != nil || !tr.Hooks || tr.Gateway {
		t.Errorf("TrafficSeen = %+v, %v; want hooks only", tr, err)
	}
}

// Autopilot reads the probe record through internal/hookcaps, and the hooks write it through
// SaveCapabilities. This keeps the two in step: what is saved is exactly what is read back, and a
// record a probe made for Bash output is seen as supported by the query Autopilot asks.
func TestTheProbeRecordTheHooksWriteIsWhatAutopilotReads(t *testing.T) {
	dir := t.TempDir()
	want := Capabilities{Entries: []Capability{
		{Agent: "claude", AgentVersion: "2.1.319", Tool: "Bash", InputRewrite: CapYes, OutputRewrite: CapYes, Method: "live"},
		{Agent: "claude", AgentVersion: "2.1.319", Tool: "Grep", OutputRewrite: CapNo, Method: "live"},
	}}
	if err := SaveCapabilities(dir, want); err != nil {
		t.Fatal(err)
	}
	got := hookcaps.Load(dir)
	if len(got.Entries) != 2 || got.V != hookcaps.Version {
		t.Fatalf("what the hooks saved was not read back: %+v", got)
	}
	if !got.SupportsAny("Bash", hookcaps.RewriteOutput) || got.SupportsAny("Grep", hookcaps.RewriteOutput) {
		t.Errorf("Autopilot's query disagrees with the record: %+v", got)
	}
	// And the hooks' own view of the same file agrees with Autopilot's.
	own := LoadCapabilities(dir)
	for _, tool := range []string{"Bash", "Grep"} {
		for _, kind := range []string{RewriteInput, RewriteOutput} {
			if own.Supports("claude", tool, kind) != got.Supports("claude", tool, kind) {
				t.Errorf("%s %s: the hooks and Autopilot read the record differently", tool, kind)
			}
		}
	}
}
