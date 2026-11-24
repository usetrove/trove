package report

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/pricing"
)

func TestHeadlessFinding(t *testing.T) {
	db := openDB(t)
	installedDaysAgo(t, db, 60)
	at := now.Add(-48 * time.Hour)
	for i, r := range []struct {
		session, ep string
		cost        any
	}{
		{"c-ci", "sdk-cli", 0.30},
		{"c-ci", "sdk-cli", 0.30},
		{"c-cron", "sdk-cli", nil}, // unpriced
		{"c-me", "cli", 1.20},
		{"c-old", "", 0.20}, // no entrypoint
	} {
		exec(t, db, `INSERT INTO history_requests(request_key, source, session_key, ts, model, output_tokens, cost_usd, entrypoint)
			VALUES(?,?,?,?,?,?,?,NULLIF(?, ''))`, "hk"+string(rune('a'+i)), "claude_code", r.session, at.Add(time.Duration(i)*time.Minute).UnixMilli(),
			"claude-sonnet-5-5", 100, r.cost, r.ep)
	}
	// Older than a week: left out.
	exec(t, db, `INSERT INTO history_requests(request_key, source, session_key, ts, model, output_tokens, cost_usd, entrypoint)
		VALUES('hk-old', 'claude_code', 'c-ci', ?, 'claude-sonnet-5-5', 100, 5.0, 'sdk-cli')`, now.Add(-9*24*time.Hour).UnixMilli())
	tbl, err := pricing.Default()
	if err != nil {
		t.Fatal(err)
	}
	r, err := Build(context.Background(), db, Inputs{Config: config.Default(), Now: now, Prices: tbl}, mustPeriod(t, Month))
	if err != nil {
		t.Fatal(err)
	}
	h := r.Headless
	if h == nil || h.Sessions != 2 || h.Requests != 3 || h.USD.Value < 0.599 || h.USD.Value > 0.601 || h.USD.Unpriced != 1 ||
		h.Unknown != 1 || h.EntrypointSource != measure.EntrypointSource || strings.Join(h.Entrypoints, ",") != "sdk-cli" {
		t.Fatalf("finding %+v", h)
	}
	if h.Share == nil || h.Share.Value < 29.9 || h.Share.Value > 30.1 || h.Share.Label != measure.Estimated {
		t.Fatalf("share %+v", h.Share)
	}
	assertLabeled(t, r)
	text := Render(r, Options{Width: 80})
	i := strings.Index(text, "Headless and SDK runs")
	if i < 0 {
		t.Fatalf("section missing:\n%s", text)
	}
	j := i + strings.Index(text[i:], "upper bound.") + len("upper bound.\n")
	golden(t, "headless.txt", text[i:j])
}

func TestNoHeadlessFindingWithoutHeadlessRuns(t *testing.T) {
	db := openDB(t)
	installedDaysAgo(t, db, 60)
	exec(t, db, `INSERT INTO history_requests(request_key, source, session_key, ts, model, output_tokens, cost_usd, entrypoint)
		VALUES('h1', 'claude_code', 'c-me', ?, 'claude-sonnet-5-5', 100, 1.0, 'cli')`, now.Add(-time.Hour).UnixMilli())
	r, err := Build(context.Background(), db, Inputs{Config: config.Default(), Now: now}, mustPeriod(t, Month))
	if err != nil {
		t.Fatal(err)
	}
	if r.Headless != nil {
		t.Fatalf("interactive use only: %+v", r.Headless)
	}
}
