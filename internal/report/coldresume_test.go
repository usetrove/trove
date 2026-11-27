package report

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
)

func TestColdResumeSection(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	seedHistory(t, db, "subscription") // a report with no traffic at all stops before its sections
	if r, err := Build(ctx, db, Inputs{Config: config.Default(), Now: now}, mustPeriod(t, Month)); err != nil || r.ColdResume != nil {
		t.Fatalf("no guard events, no section: %+v %v", r.ColdResume, err)
	}
	at := now.Add(-48 * time.Hour)
	for _, e := range []struct {
		kind   string
		tokens int64
	}{
		{"blocked", 210000}, {"overridden", 210000},
		{"blocked", 320000}, {"cleared", 320000},
		{"blocked", 180000}, {"cleared", 180000},
		{"advised", 160000}, {"would_block", 230000}, {"would_advise", 170000},
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO hook_events(ts, session_key, hook, kind, tokens) VALUES(?,?,?,?,?)`,
			at.UnixMilli(), "c-s", config.OptimizerHookColdResume, e.kind, e.tokens); err != nil {
			t.Fatal(err)
		}
	}
	r, err := Build(ctx, db, Inputs{Config: config.Default(), Now: now}, mustPeriod(t, Month))
	if err != nil || r.ColdResume == nil {
		t.Fatalf("%+v %v", r.ColdResume, err)
	}
	assertLabeled(t, r)
	text := Render(r, Options{Width: 80})
	i := strings.Index(text, "Cold-resume guard")
	j := strings.Index(text, "Biggest leaks")
	if i < 0 || j < i {
		t.Fatalf("section missing:\n%s", text)
	}
	golden(t, "cold_resume.txt", text[i:j])
	golden(t, "cold_resume.json", toJSON(t, r.ColdResume))
}

func TestRebuildGuardSections(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	seedHistory(t, db, "subscription")
	at := now.Add(-48 * time.Hour)
	for _, e := range []struct {
		hook, kind string
		tokens     int64
	}{
		{config.OptimizerHookModelSwitch, "advised", 182000}, {config.OptimizerHookModelSwitch, "overridden", 182000},
		{config.OptimizerHookModelSwitch, "advised", 240000},
		{config.OptimizerHookModelSwitch, "would_advise", 200000},
		{config.OptimizerHookColdCompact, "blocked", 163000}, {config.OptimizerHookColdCompact, "overridden", 163000},
		{config.OptimizerHookColdCompact, "blocked", 400000}, {config.OptimizerHookColdCompact, "cleared", 400000},
		{config.OptimizerHookColdCompact, "would_block", 210000},
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO hook_events(ts, session_key, hook, kind, tokens) VALUES(?,?,?,?,?)`,
			at.UnixMilli(), "c-s", e.hook, e.kind, e.tokens); err != nil {
			t.Fatal(err)
		}
	}
	r, err := Build(ctx, db, Inputs{Config: config.Default(), Now: now}, mustPeriod(t, Month))
	if err != nil || r.ModelSwitch == nil || r.ColdCompact == nil || r.ColdResume != nil {
		t.Fatalf("%+v %+v %+v %v", r.ModelSwitch, r.ColdCompact, r.ColdResume, err)
	}
	assertLabeled(t, r)
	text := Render(r, Options{Width: 80})
	i := strings.Index(text, "Model-switch guard")
	j := strings.Index(text, "Biggest leaks")
	if i < 0 || j < i || !strings.Contains(text[i:j], "Cold-compact guard") {
		t.Fatalf("sections missing:\n%s", text)
	}
	golden(t, "rebuild_guards.txt", text[i:j])
	golden(t, "rebuild_guards.json", toJSON(t, map[string]any{"model_switch": r.ModelSwitch, "cold_compact": r.ColdCompact}))
}
