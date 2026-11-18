package measure

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/store"
)

func TestModelFamily(t *testing.T) {
	for in, want := range map[string]string{
		"claude-sonnet-5-5":        "claude-sonnet",
		"Claude-Opus-5-5-20261101": "claude-opus",
		"gpt-5":                    "gpt-5",
		"gpt-5.1-codex":            "gpt-5.1",
		"claude-haiku-5-5@vertex":  "claude-haiku",
		"":                         "",
	} {
		if got := ModelFamily(in); got != want {
			t.Errorf("ModelFamily(%q) = %q, want %q", in, got, want)
		}
	}
}

// histRow is one stored request; only the columns the learner reads.
type histRow struct {
	model                      string
	input, write, read, output int64
	resultBytes                int64
	sidechain                  bool
	at                         time.Time
}

func insertHistory(t *testing.T, db *store.DB, session string, rows []histRow) {
	t.Helper()
	for i, r := range rows {
		side := 0
		if r.sidechain {
			side = 1
		}
		if _, err := db.ExecContext(context.Background(), `INSERT INTO history_requests
			(request_key, source, session_key, ts, model, input_tokens, cache_write_5m, cache_read, output_tokens, sidechain, tool_result_bytes)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			fmt.Sprintf("%s-%d", session, i), "claude_code", session, r.at.UnixMilli(), r.model,
			r.input, r.write, r.read, r.output, side, r.resultBytes); err != nil {
			t.Fatal(err)
		}
	}
}

// chain builds n+1 requests in which every request after the first adds `tokens` new tokens for the
// previous request's `bytes` of tool results (plus the previous request's 100 output tokens, which join
// the context too), with the cache still warm.
func chain(model string, n int, bytes, tokens int64, start time.Time) []histRow {
	rows := make([]histRow, 0, n+1)
	var ctx int64 = 10_000
	for i := 0; i <= n; i++ {
		r := histRow{model: model, output: 100, resultBytes: bytes, at: start.Add(time.Duration(i) * time.Minute)}
		if i == 0 {
			r.write = ctx
		} else {
			r.read = ctx
			r.write = tokens + 100
			ctx += tokens + 100
		}
		rows = append(rows, r)
	}
	return rows
}

func learn(t *testing.T, db *store.DB) map[string]BytesRatio {
	t.Helper()
	got, err := LearnBytesPerToken(context.Background(), db, t0.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestLearnBytesPerToken(t *testing.T) {
	db := openDB(t)
	insertHistory(t, db, "s1", chain("claude-sonnet-5-5", 25, 8000, 2000, t0))
	got := learn(t, db)
	r, ok := got["claude-sonnet"]
	if !ok {
		t.Fatalf("no ratio learned: %+v", got)
	}
	if math.Abs(r.BytesPerToken-4.0) > 0.01 || r.Samples != 25 {
		t.Errorf("got %+v, want 4.0 bytes per token from 25 samples", r)
	}
}

func TestLearnBytesPerTokenNeedsEnoughSamples(t *testing.T) {
	db := openDB(t)
	insertHistory(t, db, "s1", chain("claude-sonnet-5-5", MinRatioSamples-1, 8000, 2000, t0))
	if got := learn(t, db); len(got) != 0 {
		t.Errorf("a ratio from %d samples was published: %+v", MinRatioSamples-1, got)
	}
}

func TestLearnBytesPerTokenKeepsFamiliesApart(t *testing.T) {
	db := openDB(t)
	insertHistory(t, db, "s1", chain("claude-sonnet-5-5", 25, 8000, 2000, t0))
	insertHistory(t, db, "s2", chain("claude-haiku-5-5", 25, 6000, 2000, t0))
	got := learn(t, db)
	if math.Abs(got["claude-sonnet"].BytesPerToken-4.0) > 0.01 || math.Abs(got["claude-haiku"].BytesPerToken-3.0) > 0.01 {
		t.Errorf("families mixed: %+v", got)
	}
}

func TestLearnBytesPerTokenSkipsPairsThatAreNotClean(t *testing.T) {
	cases := map[string]func([]histRow){
		"cache went cold, so the new tokens include a rebuilt prefix": func(rs []histRow) {
			for i := 1; i < len(rs); i++ {
				rs[i].write += rs[i].read
				rs[i].read = 0
			}
		},
		"tool results too small to measure": func(rs []histRow) {
			for i := range rs {
				rs[i].resultBytes = 400
			}
		},
		"model changed between the two requests": func(rs []histRow) {
			for i := range rs {
				if i%2 == 1 {
					rs[i].model = "claude-opus-5-5"
				}
			}
		},
		"the requests alternate between the main agent and a subagent": func(rs []histRow) {
			for i := range rs {
				rs[i].sidechain = i%2 == 1
			}
		},
		"more output than new tokens": func(rs []histRow) {
			for i := range rs {
				rs[i].output = 50_000
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			db := openDB(t)
			rows := chain("claude-sonnet-5-5", 40, 8000, 2000, t0)
			mutate(rows)
			insertHistory(t, db, "s1", rows)
			if got := learn(t, db); len(got) != 0 {
				t.Errorf("learned from pairs that should be skipped: %+v", got)
			}
		})
	}
}

func TestLearnBytesPerTokenRefusesImplausibleRatios(t *testing.T) {
	for name, tokens := range map[string]int64{"far too many bytes per token": 100, "far too few": 20000} {
		db := openDB(t)
		insertHistory(t, db, "s1", chain("claude-sonnet-5-5", 25, 8000, tokens, t0))
		if got := learn(t, db); len(got) != 0 {
			t.Errorf("%s: published %+v", name, got)
		}
	}
}

func TestLearnBytesPerTokenIgnoresOldHistory(t *testing.T) {
	db := openDB(t)
	insertHistory(t, db, "s1", chain("claude-sonnet-5-5", 25, 8000, 2000, t0.Add(-60*24*time.Hour)))
	if got := learn(t, db); len(got) != 0 {
		t.Errorf("history outside the window was used: %+v", got)
	}
}

func TestLearnBytesPerTokenIsDeterministic(t *testing.T) {
	db := openDB(t)
	insertHistory(t, db, "s1", chain("claude-sonnet-5-5", 25, 8000, 2000, t0))
	a, b := learn(t, db), learn(t, db)
	if a["claude-sonnet"] != b["claude-sonnet"] {
		t.Errorf("two runs differ: %+v vs %+v", a, b)
	}
}
