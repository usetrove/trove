package report

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

// The status line works in the local zone, as the command does.
var nowLocal = time.Date(2026, 10, 29, 12, 0, 0, 0, time.Local)

// writeRequests stores requests through the real writer, so usage_daily is
// filled exactly as the gateway fills it.
func writeRequests(t *testing.T, db *store.DB, rs []store.Request) {
	t.Helper()
	w := store.NewWriter(db, store.WriterOptions{FlushInterval: time.Millisecond})
	for _, r := range rs {
		if !w.Enqueue(r) {
			t.Fatal("writer dropped a request")
		}
	}
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func money(v float64) *float64 { return &v }

func seedStatus(t *testing.T, db *store.DB) {
	t.Helper()
	var rs []store.Request
	for i := 0; i < 20; i++ {
		r := store.Request{TS: time.Date(2026, 10, 1+i, 9, 0, 0, 0, time.Local), SessionKey: fmt.Sprintf("s%d", i), Agent: "claude_code",
			Model: "m", StatusCode: 200, CostUSD: money(30)}
		if i >= 15 {
			r.TokensRemoved, r.CostDiffUSD, r.OptimizationsApplied = 1000, money(2), []byte(`{"optimizers":[{"name":"x","state":"on","tokens_removed":1000}]}`)
		}
		rs = append(rs, r)
	}
	// The status line's own session: key derived from the Claude Code session id.
	key := session.ClaudeCodeKey("sess-123") + "~1"
	rs = append(rs,
		store.Request{TS: nowLocal.Add(-time.Hour), SessionKey: key, Agent: "claude_code", Model: "m", StatusCode: 200, CostUSD: money(1.5)},
		store.Request{TS: nowLocal.Add(-30 * time.Minute), SessionKey: key, Agent: "claude_code", Model: "m", StatusCode: 200, CostUSD: money(1.7)})
	writeRequests(t, db, rs)
}

func TestStatusMatchesMeasurementLayer(t *testing.T) {
	db := openDB(t)
	seedStatus(t, db)
	cfg := config.Default()
	cfg.Budget.CapUSD = 1500

	st, err := BuildStatus(context.Background(), db, cfg, nowLocal, "sess-123")
	if err != nil {
		t.Fatal(err)
	}
	// The summary the status line reads must equal what the measurement layer
	// computes from the requests themselves.
	p := Period{Name: Month, From: StartOf(Month, nowLocal), To: nowLocal.Add(time.Millisecond)}
	rows, _ := measure.TotalsBy(context.Background(), db, p.Window(), measure.ByAgent)
	spend, _, n := sumTotals(rows)
	if st.Spend.Value != spend.Value || st.Requests != n || n != 22 {
		t.Errorf("status spend %v over %d requests; measure says %v over %d", st.Spend.Value, st.Requests, spend.Value, n)
	}
	pace, _ := BuildPacing(context.Background(), db, cfg, nowLocal)
	if st.Budget.Status != pace.Status || st.Budget.PercentUsed != pace.PercentUsed {
		t.Errorf("status pace %+v, report pace %+v", st.Budget, pace)
	}
	sv, _ := measure.SavingsByOptimizer(context.Background(), db, p.Window(), cfg.Measure)
	var saved float64
	for _, s := range sv {
		saved += s.USD.Value
	}
	if st.Saved.Value != saved || saved != 10 {
		t.Errorf("status saved %v; measure says %v", st.Saved.Value, saved)
	}
	if st.Session == nil || st.Session.Calls != 2 || st.Session.Cost.Value != 3.2 || st.Session.Cost.Label != measure.Estimated {
		t.Errorf("session = %+v", st.Session)
	}
	assertLabeled(t, st)

	other, _ := BuildStatus(context.Background(), db, cfg, nowLocal, "unknown-session")
	if other.Session != nil {
		t.Errorf("an unknown session id must show no session cost: %+v", other.Session)
	}
}

// midMonth is early enough that the fixture spend is ahead of a $700 budget.
var midMonth = time.Date(2026, 10, 12, 12, 0, 0, 0, time.Local)

func TestRenderStatusGoldens(t *testing.T) {
	db := openDB(t)
	seedStatus(t, db)
	ctx := context.Background()
	build := func(cap float64, id string, at time.Time) Status {
		cfg := config.Default()
		cfg.Budget.CapUSD = cap
		st, err := BuildStatus(ctx, db, cfg, at, id)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	var out strings.Builder
	line := func(label string, st Status, format string, color bool) {
		out.WriteString(fmt.Sprintf("%-26s %s\n", label, strings.ReplaceAll(RenderStatus(st, format, color), "\x1b", "<ESC>")))
	}
	line("no budget", build(0, "", nowLocal), FormatShell, false)
	line("no budget, session", build(0, "sess-123", nowLocal), FormatClaude, false)
	line("under cap", build(2000, "sess-123", nowLocal), FormatClaude, true)
	line("ahead (claude, color)", build(700, "sess-123", midMonth), FormatClaude, true)
	line("ahead (tmux, color)", build(700, "sess-123", midMonth), FormatTmux, true)
	line("ahead (shell)", build(700, "sess-123", midMonth), FormatShell, true)
	line("ahead (no color)", build(700, "sess-123", midMonth), FormatClaude, false)
	line("near cap", build(700, "", nowLocal), FormatClaude, true)
	line("over cap", build(500, "", nowLocal), FormatClaude, true)
	line("empty period", build(0, "", nowLocal.AddDate(0, 3, 0)), FormatShell, false)
	line("no data at all", Status{}, FormatShell, false)

	unpriced := build(0, "", nowLocal)
	unpriced.Spend.Unpriced = 2
	line("unpriced calls", unpriced, FormatShell, false)
	golden(t, "statusline.txt", out.String())
}
