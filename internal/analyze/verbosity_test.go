package analyze

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/store"
)

// turn kinds for the seeded history.
const (
	tPlain     = iota // long reply, then the agent's own tool turn a few seconds later
	tInterrupt        // long streamed reply the client cut off
	tFastUser         // long reply, then a user message within seconds
	tSlowUser         // long reply, then a user message a minute later
	tShortFast        // short reply, then a user message within seconds
	tFastTool         // long reply, then a fast request whose tool results grew
)

// seedVerbosity writes sessions, each a run of turns of the given kinds, and returns the window.
func seedVerbosity(t *testing.T, sessions int, kinds []int) (*store.DB, time.Time, time.Time) {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	w := store.NewWriter(db, store.WriterOptions{})
	base := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	cats := func(hist, tools int64) []byte {
		return []byte(fmt.Sprintf(`{"label":"estimated","categories":{"history":%d,"tool_results:Bash":%d}}`, hist, tools))
	}
	for s := 0; s < sessions; s++ {
		key := fmt.Sprintf("s%02d", s)
		at := base.Add(time.Duration(s) * time.Hour)
		hist, tools := int64(1000), int64(0)
		for _, k := range kinds {
			out, lat := int64(800), int64(10_000)
			row := store.Request{TS: at, SessionKey: key, Provider: "anthropic", Model: "claude-opus-5-5", Endpoint: "/v1/messages",
				Streamed: true, StatusCode: 200, LatencyMS: lat, OutputTokens: out, SegmentTokenEstimate: cats(hist, tools)}
			gap := 3 * time.Second
			switch k {
			case tInterrupt:
				row.ErrorClass, row.OutputTokens = "client_disconnected", 300
			case tShortFast:
				row.OutputTokens = 40
			case tSlowUser:
				gap = time.Minute
			}
			w.Enqueue(row)
			at = at.Add(time.Duration(lat)*time.Millisecond + gap)
			hist += 500 // every next request carries the reply and a new message
			if k == tPlain || k == tFastTool {
				tools += 400 // the next request carries a tool result: the agent's own turn
			}
		}
		// A closing request so the last turn has a successor.
		w.Enqueue(store.Request{TS: at, SessionKey: key, Provider: "anthropic", Model: "claude-opus-5-5", Endpoint: "/v1/messages",
			Streamed: true, StatusCode: 200, LatencyMS: 1000, OutputTokens: 10, SegmentTokenEstimate: cats(hist, tools)})
	}
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	return db, base.Add(-time.Hour), base.Add(time.Duration(sessions+1) * time.Hour)
}

func repeat(k, n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = k
	}
	return out
}

func TestLearnVerbosity(t *testing.T) {
	mix := func(parts ...[]int) []int {
		var out []int
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	cases := []struct {
		name       string
		sessions   int
		kinds      []int
		level      string
		signals    int // per session
		interrupts int // per session
		fast       int // per session
	}{
		{"too few sessions", 3, repeat(tInterrupt, 10), "", 10, 10, 0},
		{"too few replies", 12, repeat(tInterrupt, 2), "", 2, 2, 0},
		{"quiet history stays concise", 12, mix(repeat(tPlain, 8), repeat(tSlowUser, 2)), VerbosityLevelConcise, 0, 0, 0},
		{"interrupted replies pick terse", 12, mix(repeat(tPlain, 6), repeat(tInterrupt, 2)), VerbosityLevelTerse, 2, 2, 0},
		{"fast user follow-ups pick terse", 12, mix(repeat(tPlain, 6), repeat(tFastUser, 2)), VerbosityLevelTerse, 2, 0, 2},
		{"fast tool turns are not follow-ups", 12, repeat(tFastTool, 8), VerbosityLevelConcise, 0, 0, 0},
		{"short replies are not counted", 12, mix(repeat(tPlain, 8), repeat(tShortFast, 4)), VerbosityLevelConcise, 0, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db, from, to := seedVerbosity(t, c.sessions, c.kinds)
			got, err := LearnVerbosity(context.Background(), db, from, to, DefaultVerbosityThresholds())
			if err != nil {
				t.Fatal(err)
			}
			if got.Level != c.level {
				t.Errorf("level %q, want %q (%+v)", got.Level, c.level, got)
			}
			e := got.Evidence
			if e.Label != "estimated" || e.Sessions != c.sessions {
				t.Errorf("evidence %+v; want label estimated and %d sessions", e, c.sessions)
			}
			if e.Interrupted != c.interrupts*c.sessions || e.FastFollowUps != c.fast*c.sessions || e.Signals != c.signals*c.sessions {
				t.Errorf("counts %+v; want %d interrupted, %d fast follow-ups, %d signals", e, c.interrupts*c.sessions, c.fast*c.sessions, c.signals*c.sessions)
			}
			if got.Reason == "" {
				t.Error("every result must say why")
			}
			if c.level == "" && !strings.Contains(got.Reason, "not enough") {
				t.Errorf("reason %q should say there is not enough evidence", got.Reason)
			}
		})
	}
}

func TestLearnVerbosityIgnoresOtherModelsBetweenTurns(t *testing.T) {
	// A side request on another model (e.g. a title or subagent call) between a reply and
	// the next main turn must not hide or fake a follow-up.
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	w := store.NewWriter(db, store.WriterOptions{})
	at := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	main := func(ts time.Time, hist, tools int64) store.Request {
		return store.Request{TS: ts, SessionKey: "x", Provider: "anthropic", Model: "claude-opus-5-5", StatusCode: 200, Streamed: true,
			LatencyMS: 10_000, OutputTokens: 900, SegmentTokenEstimate: []byte(fmt.Sprintf(`{"categories":{"history":%d,"tool_results":%d}}`, hist, tools))}
	}
	w.Enqueue(main(at, 1000, 0))
	w.Enqueue(store.Request{TS: at.Add(10500 * time.Millisecond), SessionKey: "x", Provider: "anthropic", Model: "claude-haiku-5-5", StatusCode: 200, LatencyMS: 300, OutputTokens: 900})
	w.Enqueue(main(at.Add(12*time.Second), 1600, 0))
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := LearnVerbosity(context.Background(), db, at.Add(-time.Hour), at.Add(time.Hour), DefaultVerbosityThresholds())
	if err != nil {
		t.Fatal(err)
	}
	if got.Evidence.FastFollowUps != 1 {
		t.Errorf("fast follow-ups %d, want 1 (paired within the same model): %+v", got.Evidence.FastFollowUps, got.Evidence)
	}
}
