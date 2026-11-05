package report

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
)

func seedAdapter(t *testing.T, login string, usageCost bool) Report {
	t.Helper()
	db := openDB(t)
	ts := now.Add(-time.Hour).UnixMilli()
	ins := func(kind, optimizer, state string, bytes, tokens, overhead int, in, out, cr any, cost any) {
		exec(t, db, `INSERT INTO adapter_events(ts, session_key, harness, login, provider, model, kind, optimizer, state, bytes, tokens, overhead_tokens, in_tokens, out_tokens, cache_read_tokens, cost_usd)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, ts, "h-aaaa", "opencode", login, "anthropic", "claude-sonnet-5-5", kind, optimizer, state, bytes, tokens, overhead, in, out, cr, cost)
	}
	ins("rewritten", "noise_normalizer", "on", 4000, 1000, 0, nil, nil, nil, nil)
	ins("rewritten", "noise_normalizer", "on", 2000, 500, 0, nil, nil, nil, nil)
	ins("would_rewrite", "hook_quiet_commands", "shadow", 0, 200, 0, nil, nil, nil, nil)
	var cost any
	if usageCost {
		cost = 0.5
	}
	ins("usage", "", "", 0, 0, 0, 1000, 200, 800, cost)
	ins("usage", "", "", 0, 0, 0, 3000, 100, 0, cost)
	r, err := Build(context.Background(), db, Inputs{Config: config.Default(), Now: now}, mustPeriod(t, Month))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func flat(s string) string { return strings.Join(strings.Fields(s), " ") }

func TestHarnessPluginSectionLabelsEveryNumberAndShowsLogin(t *testing.T) {
	r := seedAdapter(t, "api_key", true)
	if r.Adapters == nil || len(r.Adapters.Decisions) != 2 || len(r.Adapters.Usage) != 1 {
		t.Fatalf("%+v", r.Adapters)
	}
	text := flat(Render(r, Options{Width: 120}))
	for _, want := range []string{
		"Harness plugins",
		"opencode, API key: noise_normalizer (on) made 2 decisions (measured counts), 2 of which changed a call or its output",
		"about 1.5K tokens kept out of the context (estimated from sizes",
		"opencode, API key: hook_quiet_commands (shadow) made 1 decisions (measured counts), none of which changed anything",
		"about 200 tokens it would have kept out of the context (estimated from sizes)",
		"2 model calls in 1 sessions: 4K input, 300 output, 800 cache-read tokens (as the harness reported them)",
		"cost $1.00 (as the harness reported it)", // both usage rows carry $0.50
	} {
		if !strings.Contains(text, want) {
			t.Errorf("report lacks %q:\n%s", want, text)
		}
	}
}

func TestHarnessPluginCostIsNeverShownForALoginThatIsNotPayPerToken(t *testing.T) {
	// A Copilot login reports 0 cost; "free" would be a wrong number.
	text := flat(Render(seedAdapter(t, "copilot", false), Options{Width: 120}))
	if !strings.Contains(text, "GitHub Copilot login") || !strings.Contains(text, "cost is not shown for this login") || strings.Contains(text, "$") {
		t.Errorf("%s", text)
	}
	text = flat(Render(seedAdapter(t, "subscription_or_unknown", false), Options{Width: 120}))
	if !strings.Contains(text, "a login Trove can only watch") {
		t.Errorf("%s", text)
	}
}

func TestHarnessPluginCostTotalNeedsEveryCallToHaveReportedOne(t *testing.T) {
	text := flat(Render(seedAdapter(t, "api_key", false), Options{Width: 120}))
	if !strings.Contains(text, "cost reported for only 0 of 2 calls, so no total is shown") {
		t.Errorf("%s", text)
	}
}

func TestHarnessPluginSectionShowsEvenWithNoGatewayRequests(t *testing.T) {
	r := seedAdapter(t, "api_key", true)
	if r.State != StateEmpty && r.State != StateQuiet {
		t.Skipf("state %s: this fixture has gateway data", r.State)
	}
	if !strings.Contains(Render(r, Options{Width: 100}), "Harness plugins") {
		t.Error("a plugin-only user (no gateway) never sees what the plugin did")
	}
}

func TestReportWithoutAPluginIsUnchanged(t *testing.T) {
	db := openDB(t)
	r, err := Build(context.Background(), db, Inputs{Config: config.Default(), Now: now}, mustPeriod(t, Month))
	if err != nil {
		t.Fatal(err)
	}
	if r.Adapters != nil || strings.Contains(Render(r, Options{Width: 100}), "Harness plugins") {
		t.Error("a section appeared with no plugin data")
	}
}
