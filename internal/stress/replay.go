package stress

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/optimize"
)

// Replay reads Claude Code session logs (JSONL) and runs the engine on requests rebuilt from them. Privacy
// rules for this file (CLAUDE.md rules 1, 7 and 12):
//
//   - Files are opened read-only and read as a stream. Nothing is written, not to the scratch folder, not to
//     the report, not to git.
//   - Rebuilt requests exist only in memory, for one engine call each.
//   - The result is aggregates: counts and byte totals per optimizer. It holds no message text, no file path,
//     no session identifier. Session keys are derived from paths in memory and never leave the function.
//   - Errors name no path and no content. A log that cannot be read is counted, not described.
//
// The engine is run in the same states as qa/replay: noise normalization and duplicate collapsing on, the
// trimmer in shadow (it estimates and never changes the request). Every figure is an estimate.

// replayMaxBody is the gateway's own limit: larger requests are not optimized and are counted as too big.
const replayMaxBody = optimize.MaxBody

// ReplaySummary is the aggregate of one replay. Labeled estimated.
type ReplaySummary struct {
	Label           string            `json:"label"` // always "estimated"
	LogFiles        int               `json:"log_files"`
	FilesUnreadable int               `json:"log_files_unreadable"`
	Sessions        int               `json:"sessions"`
	Turns           int               `json:"assistant_turns"`
	PerSession      int               `json:"requests_sampled_per_session"`
	Optimizers      []ReplayOptimizer `json:"optimizers"`
	Limits          []string          `json:"limits"`
}

// ReplayOptimizer is one optimizer's totals over the replayed requests.
type ReplayOptimizer struct {
	Name             string  `json:"name"`
	Mode             string  `json:"mode"` // "on" or "shadow (estimate only)"
	Requests         int     `json:"requests"`
	Changed          int     `json:"changed"`
	Failures         int     `json:"failures"`
	Skipped          int     `json:"skipped"`
	TooBig           int     `json:"too_big"`
	BytesBefore      int64   `json:"bytes_before"`
	BytesRemoved     int64   `json:"bytes_removed"`
	BytesRemovedPct  float64 `json:"bytes_removed_pct"`
	EstTokensRemoved int64   `json:"est_tokens_removed"`
}

// replayTarget is one optimizer state the replay measures.
type replayTarget struct {
	name  string
	state string
}

var replayTargets = []replayTarget{
	{"noise_normalizer", config.StateOn},
	{"duplicate_result_collapser", config.StateOn},
	{"output_trimmer", config.StateShadow},
}

// replayLogs replays every session log under dir. perSession requests are sampled evenly from each session.
func replayLogs(dir string, perSession int) (ReplaySummary, error) {
	st, err := os.Stat(dir)
	if err != nil {
		return ReplaySummary{}, fmt.Errorf("the replay folder could not be read: %v. Check the path you passed to --replay-dir", err)
	}
	if !st.IsDir() {
		return ReplaySummary{}, fmt.Errorf("--replay-dir must be a folder of session logs, not a single file")
	}
	var files []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".jsonl") {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)

	engines := map[string]*optimize.Engine{}
	sums := make([]ReplayOptimizer, len(replayTargets))
	for i, tg := range replayTargets {
		engines[tg.name] = replayEngine(tg.name, tg.state)
		mode := "on"
		if tg.state == config.StateShadow {
			mode = "shadow (estimate only)"
		}
		sums[i] = ReplayOptimizer{Name: tg.name, Mode: mode}
	}

	out := ReplaySummary{Label: "estimated", LogFiles: len(files), PerSession: perSession, Limits: []string{
		"The logs do not contain the system prompt or tool definitions, so requests are smaller than the real ones.",
		"Requests are sampled evenly within each session, not all used.",
		"Token figures are the engine's estimates (3.5 bytes per token), not provider counts.",
	}}
	for _, p := range files {
		turns, err := readReplayTurns(p)
		if err != nil {
			out.FilesUnreadable++
		}
		if len(turns) == 0 {
			continue
		}
		out.Sessions++
		out.Turns += len(turns)
		sessKey := shortHash(p)
		for _, idx := range pickEven(len(turns), perSession) {
			t := turns[idx]
			if len(t.before) == 0 {
				continue
			}
			model := t.model
			if model == "" {
				model = "claude-sonnet-5-5"
			}
			body, err := json.Marshal(map[string]any{"model": model, "max_tokens": 1024, "messages": t.before})
			if err != nil {
				continue // cannot happen for values read from valid JSON; counted nowhere because it is not a request
			}
			for i, tg := range replayTargets {
				tally(&sums[i], engines[tg.name], tg, body, fmt.Sprintf("%s#%d", sessKey, idx))
			}
		}
	}
	for i := range sums {
		if sums[i].BytesBefore > 0 {
			sums[i].BytesRemovedPct = round2(100 * float64(sums[i].BytesRemoved) / float64(sums[i].BytesBefore))
		}
	}
	out.Optimizers = sums
	return out, nil
}

// tally runs one request through one optimizer's engine and adds the result to the totals.
func tally(s *ReplayOptimizer, eng *optimize.Engine, tg replayTarget, body []byte, key string) {
	if len(body) > replayMaxBody {
		s.TooBig++ // the gateway would not optimize a request this large either
		return
	}
	s.Requests++
	s.BytesBefore += int64(len(body))
	res := eng.TransformRequest(context.Background(), optimize.RequestIn{
		Provider: "anthropic", Path: "/v1/messages", Header: http.Header{"Content-Type": {"application/json"}}, Body: body,
	}, optimize.Session{Key: key, Agent: "claude_code"})
	if len(res.Failures) > 0 {
		s.Failures++
	}
	if res.Skipped != "" {
		s.Skipped++
	}
	if res.Changed {
		s.Changed++
	}
	for _, a := range res.Applied {
		if a.Name == tg.name {
			s.EstTokensRemoved += a.TokensRemoved
			if tg.state == config.StateShadow {
				s.BytesRemoved += int64(a.BytesRemoved)
			}
		}
	}
	if tg.state == config.StateOn && res.Changed {
		s.BytesRemoved += int64(len(body) - len(res.Body))
	}
}

// replayEngine returns an engine with exactly one optimizer in the given state.
func replayEngine(name, state string) *optimize.Engine {
	cfg := config.Default()
	cfg.Optimizers = map[string]string{}
	for _, n := range optimize.PipelineNames {
		cfg.Optimizers[n] = config.StateOff
	}
	cfg.Optimizers[name] = state
	cfg.Optimize.BudgetMS = 5000
	return optimize.New(optimize.Options{Config: cfg, Getenv: func(string) string { return "" }})
}

// replayEntry is the subset of a Claude Code log line the replay needs.
type replayEntry struct {
	Type        string          `json:"type"`
	RequestID   string          `json:"requestId"`
	IsSidechain bool            `json:"isSidechain"`
	Message     json.RawMessage `json:"message"`
}

type replayMessage struct {
	Role    string          `json:"role"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"`
}

// replayTurn is one assistant response and the messages the agent had sent before it.
type replayTurn struct {
	before []json.RawMessage
	model  string
}

// readReplayTurns rebuilds the request for every assistant turn in one log, streaming line by line.
func readReplayTurns(path string) ([]replayTurn, error) {
	f, err := os.Open(path) //nolint:gosec // the user's own logs, opened read-only and streamed
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	r := bufio.NewReaderSize(f, 1<<20)

	var history []json.RawMessage
	var turns []replayTurn
	var curID string
	var curBefore []json.RawMessage
	var curModel string
	flush := func() {
		if curID != "" {
			turns = append(turns, replayTurn{before: curBefore, model: curModel})
		}
		curID = ""
	}
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var e replayEntry
			if json.Unmarshal(line, &e) == nil && !e.IsSidechain && len(e.Message) > 0 {
				var m replayMessage
				if json.Unmarshal(e.Message, &m) == nil && (e.Type == "user" || e.Type == "assistant") {
					if e.Type == "assistant" && e.RequestID != "" {
						if curID != e.RequestID {
							flush()
							curID = e.RequestID
							curBefore = append([]json.RawMessage(nil), history...)
							curModel = m.Model
						}
						// Streamed chunks of one response share a request id; they are not new history.
						continue
					}
					flush()
					if m.Role == "" {
						m.Role = e.Type
					}
					if len(m.Content) > 0 && string(m.Content) != "null" {
						b, _ := json.Marshal(map[string]any{"role": m.Role, "content": json.RawMessage(m.Content)})
						history = append(history, b)
					}
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			flush()
			return turns, err
		}
	}
	flush()
	return turns, nil
}

// pickEven returns up to n indices, evenly spaced over [0, total).
func pickEven(total, n int) []int {
	if total <= n {
		out := make([]int, total)
		for i := range out {
			out[i] = i
		}
		return out
	}
	out := make([]int, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, i*total/n)
	}
	return out
}

// shortHash identifies a path for session grouping. It is used in memory only.
func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:6])
}
