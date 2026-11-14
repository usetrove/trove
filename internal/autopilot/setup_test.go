package autopilot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/hookcaps"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/store"
	"github.com/usetrove/trove/internal/technique"
)

// probed writes a hook probe record saying Bash output rewriting was seen to work, as a live probe does.
// Pass entries to write a different record.
func probed(t *testing.T, dir string, entries ...hookcaps.Capability) {
	t.Helper()
	if len(entries) == 0 {
		entries = []hookcaps.Capability{{Agent: "claude", AgentVersion: "2.1.319", Tool: "Bash", InputRewrite: hookcaps.Yes,
			OutputRewrite: hookcaps.Yes, Method: "live", ProbedAt: "2026-11-14T00:00:00Z"}}
	}
	writeProbe(t, dir, hookcaps.Capabilities{V: hookcaps.Version, Entries: entries})
}

func advanceN(t *testing.T, db *store.DB, dir string, from time.Time, n int) config.AutopilotFile {
	t.Helper()
	var f config.AutopilotFile
	for i := 0; i < n; i++ {
		res, err := Advance(context.Background(), db, dir, config.Default(), DefaultPolicy(), from.Add(time.Duration(i)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		f = res.File
	}
	return f
}

// Without a probe the hooks run a rewrite as shadow, so "on" would be a lie. A hooks-only user must be
// held back, told what to do, and promoted only once a probe has seen the rewrite work.
func TestAHooksOnlyUserIsHeldBackUntilAProbeHasSeenTheRewriteWork(t *testing.T) {
	db, dir := open(t)
	setInstall(t, db, 30)
	hookSessions(t, db, "noise_normalizer", 25, 200) // plenty of evidence and a finished baseline
	_, _ = Enable(dir, DefaultPolicy(), hookNow.AddDate(0, 0, -30))

	f := advanceN(t, db, dir, hookNow, 3)
	e := f.Entries["noise_normalizer"]
	if e.State != config.AutopilotShadow || !strings.Contains(e.Reason, "not available in your setup") ||
		!strings.Contains(e.Reason, "Bash output rewriting") || !strings.Contains(e.Reason, "trove hooks verify") {
		t.Fatalf("without a probe: %+v; want shadow, and a reason that says what to run", e)
	}
	if v := planOf(f, "noise_normalizer"); v.Verdict != VerdictUnavailable {
		t.Errorf("planner verdict %q, want unavailable", v.Verdict)
	}

	probed(t, dir)
	f = advanceN(t, db, dir, hookNow.Add(10*time.Hour), 2)
	if got := f.Entries["noise_normalizer"].State; got != config.AutopilotOn {
		t.Errorf("after a probe: %q, want on (candidate, then confirmed)", got)
	}
}

// A probe for the wrong tool or kind is not enough, and neither is a record only the documentation vouches for.
func TestAProbeForTheWrongRewriteDoesNotCount(t *testing.T) {
	db, dir := open(t)
	setInstall(t, db, 30)
	hookSessions(t, db, "noise_normalizer", 25, 200)
	_, _ = Enable(dir, DefaultPolicy(), hookNow.AddDate(0, 0, -30))
	probed(t, dir,
		hookcaps.Capability{Agent: "claude", Tool: "Read", InputRewrite: hookcaps.Yes, OutputRewrite: hookcaps.No, Method: "live"},
		hookcaps.Capability{Agent: "claude", Tool: "Bash", InputRewrite: hookcaps.Yes, OutputRewrite: hookcaps.No, Method: "live"})
	f := advanceN(t, db, dir, hookNow, 3)
	if e := f.Entries["noise_normalizer"]; e.State != config.AutopilotShadow || !strings.Contains(e.Reason, "Bash output rewriting") {
		t.Errorf("a probe that saw Bash output NOT work promoted it: %+v", e)
	}
	probed(t, dir, hookcaps.Capability{Agent: "claude", Tool: "Bash", OutputRewrite: hookcaps.Yes, Method: "docs"})
	if e := advanceN(t, db, dir, hookNow.Add(5*time.Hour), 3).Entries["noise_normalizer"]; e.State != config.AutopilotShadow {
		t.Errorf("a docs-only record promoted it: %+v", e)
	}
}

// The gateway needs no hook probe: a gateway user is not held back for lack of one.
func TestAGatewayUserNeedsNoHookProbe(t *testing.T) {
	db, dir := open(t)
	setInstall(t, db, 30)
	gatewaySessions(t, db, "noise_normalizer", 25, 300)
	_, _ = Enable(dir, DefaultPolicy(), hookNow.AddDate(0, 0, -30))
	f := advanceN(t, db, dir, hookNow, 3)
	if got := f.Entries["noise_normalizer"].State; got != config.AutopilotOn {
		t.Errorf("a gateway user was held back: %q (%q)", got, f.Entries["noise_normalizer"].Reason)
	}
}

// Claude Code is updated, the plugin replaces the probe record with a docs-only one, and the hooks run
// the rewrite as shadow again. Autopilot must say so, and let it earn its way back once re-tested.
func TestAPromotionIsUndoneWhenTheProbeNoLongerCoversIt(t *testing.T) {
	db, dir := open(t)
	setInstall(t, db, 30)
	hookSessions(t, db, "noise_normalizer", 25, 200)
	_, _ = Enable(dir, DefaultPolicy(), hookNow.AddDate(0, 0, -30))
	probed(t, dir)
	if got := advanceN(t, db, dir, hookNow, 2).Entries["noise_normalizer"].State; got != config.AutopilotOn {
		t.Fatalf("setup: %q, want on", got)
	}

	probed(t, dir, hookcaps.Capability{Agent: "claude", AgentVersion: "2.1.400", Tool: "Bash", OutputRewrite: hookcaps.Yes, Method: "docs"})
	res, err := Advance(context.Background(), db, dir, config.Default(), DefaultPolicy(), hookNow.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	e := res.File.Entries["noise_normalizer"]
	if e.State != config.AutopilotShadow || !strings.Contains(e.Reason, "no longer available in your setup") {
		t.Fatalf("after the probe stopped covering it: %+v", e)
	}
	if len(res.Events) != 1 || res.Events[0].To != config.AutopilotShadow {
		t.Errorf("the change must be recorded as an event: %+v", res.Events)
	}
	cfg, _ := config.Load(dir, func(string) string { return "" })
	if cfg.OptimizerState("noise_normalizer") == config.StateOn {
		t.Error("the state file still says on")
	}

	probed(t, dir) // re-tested
	if got := advanceN(t, db, dir, hookNow.Add(48*time.Hour), 2).Entries["noise_normalizer"].State; got != config.AutopilotOn {
		t.Errorf("after re-testing: %q, want on again", got)
	}
}

func TestSetupContextSaysWhatTheInstallCanDo(t *testing.T) {
	s := Setup{
		Traffic:      measure.Traffic{Hooks: true},
		MCP:          true,
		CustomAgents: []string{"sdk"},
		Hooks: hookcaps.Capabilities{V: hookcaps.Version, Entries: []hookcaps.Capability{
			{Agent: "claude", Tool: "Bash", InputRewrite: hookcaps.Yes, OutputRewrite: hookcaps.Yes, Method: "live"},
			{Agent: "claude", Tool: "Read", InputRewrite: hookcaps.Yes, Method: "session"},
		}},
	}
	ctx := s.context()
	if ctx.GatewayOn || ctx.Login != technique.LoginUnknown {
		t.Errorf("hooks only: %+v", ctx)
	}
	joined := strings.Join(ctx.HookCaps, ",")
	for _, want := range []string{"Bash:input", "Bash:output", "Read:input"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing hook capability %s in %v", want, ctx.HookCaps)
		}
	}
	if strings.Contains(joined, "Read:output") {
		t.Errorf("Read output was never probed: %v", ctx.HookCaps)
	}
	if len(ctx.Have) != 3 {
		t.Errorf("Have = %v, want MCP server, recovery store and custom agent", ctx.Have)
	}
	if g := (Setup{Traffic: measure.Traffic{Gateway: true}}).context(); !g.GatewayOn || g.Login != technique.LoginAPIKey {
		t.Errorf("gateway traffic is API-key traffic: %+v", g)
	}
}

func TestPrerequisiteForAsksTheRegistry(t *testing.T) {
	hooksOnly := measure.Traffic{Hooks: true}
	probe := hookcaps.Capabilities{V: hookcaps.Version, Entries: []hookcaps.Capability{
		{Agent: "claude", Tool: "Bash", InputRewrite: hookcaps.Yes, OutputRewrite: hookcaps.Yes, Method: "live"}}}
	cases := []struct {
		name  string
		setup Setup
		want  string // "" means it can run
	}{
		{"hook_repeat_read", Setup{Traffic: hooksOnly}, ""}, // denies a read; needs no probe
		{"hook_read_limit", Setup{Traffic: hooksOnly, Hooks: probe}, "Read input rewriting"},
		{"hook_quiet_commands", Setup{Traffic: hooksOnly, Hooks: probe}, ""},
		{"output_trimmer", Setup{Traffic: hooksOnly, Hooks: probe}, "MCP server"},
		{"output_trimmer", Setup{Traffic: hooksOnly, Hooks: probe, MCP: true}, ""},
		{"hook_large_output", Setup{Traffic: hooksOnly}, "MCP server"},
		{"cache_breakpoints", Setup{Traffic: measure.Traffic{Gateway: true}}, "custom agent"},
		{"cache_breakpoints", Setup{Traffic: measure.Traffic{Gateway: true}, CustomAgents: []string{"sdk"}}, ""},
		{"hook_read_limit", Setup{}, ""}, // nothing seen yet: still collecting
		{"not_a_technique", Setup{Traffic: hooksOnly}, ""},
	}
	for _, c := range cases {
		got := prerequisiteFor(c.name, c.setup)
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%s: %q, want one containing %q", c.name, got, c.want)
		}
	}
}

func TestDetectSetupReadsTheRealFiles(t *testing.T) {
	dir := t.TempDir()
	probed(t, dir)
	s := detectSetup(dir, config.Default(), measure.Traffic{Hooks: true})
	if !s.Hooks.Supports("claude", "Bash", hookcaps.RewriteOutput) {
		t.Error("the probe record on disk was not read")
	}
	if s.MCP || len(s.CustomAgents) != 0 {
		t.Errorf("a fresh install has no MCP server and no custom agents: %+v", s)
	}
	cfg := config.Default()
	cfg.Optimize.CustomAgents = []string{"sdk"}
	if got := detectSetup(t.TempDir(), cfg, measure.Traffic{}); len(got.CustomAgents) != 1 || len(got.Hooks.Entries) != 0 {
		t.Errorf("custom agents come from the configuration, and a missing record is empty: %+v", got)
	}
}
