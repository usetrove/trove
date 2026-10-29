package analyze

import (
	"bytes"
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/store"
)

// Distinctive strings planted in every kind of content the analyzer reads.
// None of them may appear in anything stored.
var sentinels = []string{
	"SENTINEL_SYSTEM_PROMPT", "SENTINEL_USER_REQUEST", "SENTINEL_ASSISTANT_REPLY", "SENTINEL_TOOL_OUTPUT",
	"SENTINEL_SECRET_PATH", "SENTINEL_CONTEXT_FILE", "SENTINEL_CODE_SNIPPET", "SENTINEL_TOOL_ARGUMENT",
	"2026-10-28", "2026-10-29",
}

func sentinelSession(t *testing.T) []turn {
	t.Helper()
	sysFor := func(date string) []string {
		return []string{
			"SENTINEL_SYSTEM_PROMPT " + pad("system", 6000),
			"Today's date is " + date + ".\nWorking directory: /home/SENTINEL_SECRET_PATH/proj\n" + pad("tail", 400),
			"Contents of /home/SENTINEL_SECRET_PATH/CLAUDE.md:\nSENTINEL_CONTEXT_FILE\n" + pad("rules", 3000),
		}
	}
	out := strings.Repeat("\x1b[31mSENTINEL_TOOL_OUTPUT\x1b[0m\n", 30)
	msgs := []message{
		user(text("SENTINEL_USER_REQUEST please edit main.go")),
		assistant(text("SENTINEL_ASSISTANT_REPLY"), toolUse("t1", "Read")),
		user(toolResult("t1", out+"func SENTINEL_CODE_SNIPPET() {}", false)),
		assistant(toolUse("t2", "Read")),
		user(toolResult("t2", out+"func SENTINEL_CODE_SNIPPET() {}", false)),
	}
	b1 := req{system: sysFor("2026-10-28"), tools: []string{"Read", "Unused"}, msgs: msgs, caching: true}.body()
	b2 := req{system: sysFor("2026-10-29"), tools: []string{"Read", "Unused"}, msgs: append(msgs, assistant(text("SENTINEL_TOOL_ARGUMENT")), user(text("more"))), caching: true}.body()
	return []turn{
		{body: b1, write: bodyTokens(t, b1), output: 900},
		{at: time.Minute, body: b2, write: bodyTokens(t, b2), output: 900},
	}
}

func TestNoSegmentTextReachesTheDatabase(t *testing.T) {
	a := newTestAnalyzer(t, nil)
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	w := store.NewWriter(db, store.WriterOptions{})

	var sawMiss, sawShadow bool
	for _, tn := range sentinelSession(t) {
		in := tn.input(t, "sess-1")
		for _, s := range sentinels[:7] { // the planted text really is in the request being analyzed
			if !bytes.Contains(in.Body, []byte(s)) {
				t.Fatalf("fixture lost %s", s)
			}
		}
		out := a.Analyze(in)
		if out == nil {
			t.Fatal("not analyzed")
		}
		for name, b := range map[string][]byte{"estimates": out.Estimates, "findings": out.Findings, "shadow": out.Shadow} {
			for _, s := range sentinels {
				if bytes.Contains(b, []byte(s)) {
					t.Fatalf("%s output contains %q: %s", name, s, b)
				}
			}
		}
		sawMiss = sawMiss || bytes.Contains(out.Findings, []byte(ReasonVolatile))
		sawShadow = sawShadow || len(out.Shadow) > 0
		w.Enqueue(store.Request{
			TS: in.Start, SessionKey: "sess-1", Provider: "anthropic", Model: testModel, Endpoint: "/v1/messages",
			SegmentTokenEstimate: out.Estimates, Findings: out.Findings, ShadowSavings: out.Shadow,
		})
	}
	if !sawMiss || !sawShadow {
		t.Fatalf("fixture exercised miss=%v shadow=%v; it must exercise both", sawMiss, sawShadow)
	}
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	files, _ := filepath.Glob(filepath.Join(dir, "*"))
	if len(files) == 0 {
		t.Fatal("no database files found")
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range sentinels {
			if bytes.Contains(b, []byte(s)) {
				t.Errorf("%s contains %q", filepath.Base(f), s)
			}
		}
	}
}

func TestRollupsPerSessionAndDay(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	w := store.NewWriter(db, store.WriterOptions{})

	day1 := time.Date(2026, 10, 28, 23, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 10, 29, 1, 0, 0, 0, time.UTC)
	rows := []struct {
		ts   time.Time
		sess string
		json string
	}{
		{day1, "A", `{"label":"estimated","optimizers":{"dedupe_tool_results":{"tokens":1000,"usd":0.005},"strip_noise":{"tokens":200,"usd":0.001}}}`},
		{day2, "A", `{"label":"estimated","optimizers":{"dedupe_tool_results":{"tokens":500,"usd":0.0025}}}`},
		{day2, "B", `{"label":"estimated","optimizers":{"dedupe_tool_results":{"tokens":300,"usd":null}}}`},
	}
	for _, r := range rows {
		w.Enqueue(store.Request{TS: r.ts, SessionKey: r.sess, Provider: "anthropic", ShadowSavings: []byte(r.json)})
	}
	w.Enqueue(store.Request{TS: day2, SessionKey: "A", Provider: "anthropic"}) // no shadow data: ignored
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	sa, err := SessionRollup(ctx, db, "A")
	if err != nil {
		t.Fatal(err)
	}
	d := sa.Optimizers[OptDedupe]
	if sa.Requests != 2 || d.Tokens != 1500 || math.Abs(d.USD-0.0075) > 1e-12 || d.Requests != 2 || sa.Optimizers[OptNoise].Tokens != 200 {
		t.Fatalf("session A = %+v", sa)
	}
	day, err := DayRollup(ctx, db, day2)
	if err != nil {
		t.Fatal(err)
	}
	d = day.Optimizers[OptDedupe]
	if day.Requests != 2 || d.Tokens != 800 || math.Abs(d.USD-0.0025) > 1e-12 || d.UnpricedRequests != 1 || day.Label != "estimated" {
		t.Fatalf("day = %+v", day)
	}
}
