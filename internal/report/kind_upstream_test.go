package report

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
)

func TestReportSplitsAutomatedWorkAndShowsServerCapacity(t *testing.T) {
	db := openDB(t)
	ts := now.Add(-24 * time.Hour)
	ins := func(kind, upstream string, cost any, latency, ttft any, out int) {
		exec(t, db, `INSERT INTO requests(ts, session_key, agent, provider, model, status_code, input_uncached_tokens,
			output_tokens, cost_usd, kind, upstream, latency_ms, ttft_ms) VALUES(?,?,?,?,?,200,1000,?,?,?,?,?,?)`,
			ts.UnixMilli(), "s-"+kind, "claude_code", "anthropic", "m", out, cost, kind, upstream, latency, ttft)
	}
	ins("interactive", "", 0.50, 1000, nil, 100)
	ins("automated", "", 0.25, 2000, nil, 100)
	ins("unknown", "", 0.10, 3000, nil, 100)
	ins("unknown", "vllm", 0.01, 4000, 250, 400)

	cfg := config.Default()
	cfg.Upstreams.Compat = []config.CompatUpstream{{Name: "vllm", BaseURL: "http://10.0.0.5:8000",
		InputPerMTok: "1", OutputPerMTok: "2", GPUHourlyUSD: "2.00"}}
	r, err := Build(context.Background(), db, Inputs{Config: cfg, Now: now}, mustPeriod(t, Month))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Spend.ByKind) != 3 {
		t.Fatalf("by kind: %+v", r.Spend.ByKind)
	}
	if len(r.Upstreams) != 1 || r.Upstreams[0].Name != "vllm" || r.Upstreams[0].GPU == nil {
		t.Fatalf("upstreams: %+v", r.Upstreams)
	}
	assertLabeled(t, r)
	out := Render(r, Options{})
	for _, want := range []string{"Spend by session kind", "automated", "never guesses", "Self-hosted and open-weight servers",
		"time to first byte", "GPU-hour estimate", "(estimated", "Time to first byte is when"} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
}

func TestReportWithoutDeclaredKindsHasNoKindSection(t *testing.T) {
	db := openDB(t)
	seedNormal(t, db)
	r, err := Build(context.Background(), db, Inputs{Config: testConfig(), Now: now}, mustPeriod(t, Month))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Spend.ByKind) != 0 || len(r.Upstreams) != 0 {
		t.Errorf("sections appeared with nothing declared: %+v %+v", r.Spend.ByKind, r.Upstreams)
	}
	if strings.Contains(Render(r, Options{}), "session kind") {
		t.Error("the kind section was rendered")
	}
}
