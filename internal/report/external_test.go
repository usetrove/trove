package report

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/external"
	"github.com/usetrove/trove/internal/store"
)

// seedExternal records a normal month and marks tool as installed in state.
func seedExternal(t *testing.T, db *store.DB, tool toolRef, state string, _, _ float64, _, _ int64) {
	t.Helper()
	seedNormal(t, db)
	exec(t, db, `INSERT INTO external_tools(name, version, license, state, installed_at, license_ack_at, changes) VALUES(?,?,?,?,?,?,'[]')`,
		tool.name, tool.version, tool.license, state, now.AddDate(0, 0, -20).UnixMilli(), now.AddDate(0, 0, -20).UnixMilli())
}

type toolRef struct{ name, version, license string }

func addExternalSessions(t *testing.T, db *store.DB, optimizer string, onCost, offCost float64, onPrompt, offPrompt int64) {
	t.Helper()
	add := func(i int, on bool, cost float64, prompt int64) {
		key := fmt.Sprintf("x%03d", i)
		at := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * 6 * time.Hour).UnixMilli()
		st := "off"
		if on {
			st = "on"
		}
		exec(t, db, `INSERT INTO sessions(session_key, agent, started_at, ended_at, request_count, total_input_uncached_tokens, total_cost_usd) VALUES(?, 'claude_code', ?, ?, 10, ?, ?)`,
			key, at, at, prompt*10, cost)
		exec(t, db, `INSERT INTO session_pins(session_key, states, bytes_per_token, pinned_at) VALUES(?,?,3.5,?)`,
			key, fmt.Sprintf(`{%q:%q}`, optimizer, st), at)
	}
	for i := 0; i < 25; i++ {
		add(i, true, onCost+float64(i%5)*0.01, onPrompt+int64(i%5)*100)
	}
	for i := 100; i < 125; i++ {
		add(i, false, offCost+float64(i%5)*0.01, offPrompt+int64(i%5)*100)
	}
}

func TestReportExternalToolsGoldens(t *testing.T) {
	cases := []struct {
		name string
		seed func(*testing.T, *store.DB)
	}{
		{"external_cheaper", func(t *testing.T, db *store.DB) {
			seedExternal(t, db, toolRef{"rtk", "1.1.0", "Apache-2.0"}, "on", 0, 0, 0, 0)
			addExternalSessions(t, db, "ext_rtk", 0.80, 1.00, 40_000, 50_000)
		}},
		{"external_rolled_back", func(t *testing.T, db *store.DB) {
			seedExternal(t, db, toolRef{"terse-rules", "1.0.0", "Trove-project"}, "off", 0, 0, 0, 0)
			addExternalSessions(t, db, "ext_terse-rules", 1.50, 1.00, 60_000, 50_000)
			exec(t, db, `INSERT INTO guardrail_events(ts, optimizer, rule, observed, threshold, sample_on, sample_ref, reference, reason)
				VALUES(?, 'ext_terse-rules', 'net_cost', 0.5, 0, 25, 25, 'off_sessions', ?)`,
				now.UnixMilli(), "Trove switched terse-rules off because sessions with it on cost $1.5200 each on average against $1.0200 without it (25 on, 25 off sessions), so it was costing more than it saved.")
		}},
		{"external_restricted_license", func(t *testing.T, db *store.DB) {
			seedExternal(t, db, toolRef{"squeeze", "2.0.0", "Elastic-2.0"}, "on", 0, 0, 0, 0)
		}},
	}
	// squeeze is a fixture manifest, not shipped in the binary.
	fixture := external.Manifest{Name: "squeeze", Version: "2.0.0", License: "Elastic-2.0", SourceURL: "https://example.com/squeeze",
		ActsAs: external.ActsShellWrapper, Category: external.CategoryShellCompressor,
		Overhead: external.Overhead{ToolDefTokens: 300},
		Claimed:  external.Claim{Text: "cuts shell output by 70%", SourceURL: "https://example.com/squeeze"}}
	builtin, err := external.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	manifests := append(append([]external.Manifest{}, builtin...), fixture)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openDB(t)
			tc.seed(t, db)
			r, err := Build(context.Background(), db, Inputs{Config: config.Default(), Now: now, ExternalManifests: manifests}, mustPeriod(t, Month))
			if err != nil {
				t.Fatal(err)
			}
			if len(r.ExternalTools) != 1 {
				t.Fatalf("want one external tool, got %+v", r.ExternalTools)
			}
			golden(t, tc.name+".txt", Render(r, Options{Width: 80}))
			golden(t, tc.name+".json", toJSON(t, r.ExternalTools))
			assertLabeled(t, r)
		})
	}
}

// The tool's own claim and Trove's measurement must stay separate and labeled.
func TestReportExternalClaimIsNeverMeasured(t *testing.T) {
	db := openDB(t)
	seedExternal(t, db, toolRef{"rtk", "1.1.0", "Apache-2.0"}, "on", 0, 0, 0, 0)
	r, err := Build(context.Background(), db, Inputs{Config: config.Default(), Now: now}, mustPeriod(t, Month))
	if err != nil {
		t.Fatal(err)
	}
	out := Render(r, Options{Width: 80})
	if !strings.Contains(out, "Claimed by the tool:") || !strings.Contains(out, "not checked by Trove") {
		t.Errorf("claim not labeled:\n%s", out)
	}
	tool := r.ExternalTools[0]
	if tool.Claimed.Label != "claimed" || tool.PromptTokens != nil || tool.Cost != nil {
		t.Errorf("with no off sessions nothing may be measured: %+v", tool)
	}
	if !strings.Contains(out, "not available yet") {
		t.Errorf("missing-data message absent:\n%s", out)
	}
}
