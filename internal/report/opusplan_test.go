package report

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/pricing"
)

func TestOpusplanFinding(t *testing.T) {
	db := openDB(t)
	installedDaysAgo(t, db, 60)
	const op, so = "claude-opus-5-5", "claude-sonnet-5-5"
	at := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	m := func(minute int) time.Time { return at.Add(time.Duration(minute) * time.Minute) }
	for _, r := range []lrow{
		{session: "c-p", at: m(0), model: op, write: 60_000, out: 100},
		{session: "c-p", at: m(1), model: op, read: 60_000, write: 1_000, out: 100},
		{session: "c-p", at: m(2), model: so, write: 62_000, out: 100}, // plan mode off: Opus -> Sonnet
		{session: "c-p", at: m(3), model: so, read: 62_000, write: 1_000, out: 100},
		{session: "c-p", at: m(4), model: op, write: 64_000, out: 100}, // back to plan mode
	} {
		addLedger(t, db, r)
	}
	tbl, err := pricing.Default()
	if err != nil {
		t.Fatal(err)
	}
	build := func(setting string) Report {
		t.Helper()
		r, err := Build(context.Background(), db, Inputs{Config: config.Default(), Now: now, Prices: tbl, ClaudeModelSetting: setting}, mustPeriod(t, Month))
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := build("sonnet"); r.Opusplan != nil {
		t.Fatalf("only an opusplan setting turns the finding on: %+v", r.Opusplan)
	}
	r := build("opusplan")
	o := r.Opusplan
	if o == nil || o.Switches.Value != 2 || o.Sessions != 1 || o.Tokens.Value != 126_000 || o.Tokens.Label != "measured" ||
		o.USD.Label != "estimated" || o.USD.Value <= 0 {
		t.Fatalf("finding %+v", o)
	}
	assertLabeled(t, r)
	text := Render(r, Options{Width: 80})
	i := strings.Index(text, "Plan mode with opusplan")
	if i < 0 {
		t.Fatalf("section missing:\n%s", text)
	}
	j := i + strings.Index(text[i:], "upper bound.") + len("upper bound.\n")
	golden(t, "opusplan.txt", text[i:j])
}
