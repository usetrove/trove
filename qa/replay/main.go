// replay measures what each request-level optimizer would remove from real agent requests, with zero spend.
//
// It reads Claude Code session logs (JSONL) in memory, rebuilds the Messages API request the agent sent for each
// sampled assistant turn (every message before that turn), and runs the real optimizer engine on it, one optimizer
// at a time. Message text is never written out: the output is counts and byte totals only. Nothing is sent to any
// provider.
//
// Limits, stated in the output too: the logs do not contain the system prompt or tool definitions, so the request
// bodies here are smaller than the real ones; requests are sampled, not all used; and the token figures are the
// engine's own estimates, not provider counts.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
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

const maxBody = 16 << 20 // the gateway's own limit: larger bodies are not optimized

type entry struct {
	Type        string          `json:"type"`
	RequestID   string          `json:"requestId"`
	IsSidechain bool            `json:"isSidechain"`
	Message     json.RawMessage `json:"message"`
}

type message struct {
	Role    string          `json:"role"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"`
}

// turn is one assistant response (possibly streamed over several lines) and the messages before it.
type turn struct {
	before []json.RawMessage // the messages the agent sent in this request
	model  string
}

// readTurns rebuilds the request for every assistant turn in one session log.
func readTurns(path string) ([]turn, error) {
	f, err := os.Open(path) //nolint:gosec // the user's own logs, read in memory
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	r := bufio.NewReaderSize(f, 1<<20)

	var history []json.RawMessage // messages so far, in order
	var turns []turn
	var curID string // the assistant request being accumulated
	var curBefore []json.RawMessage
	var curModel string
	flush := func() {
		if curID != "" {
			turns = append(turns, turn{before: curBefore, model: curModel})
		}
		curID = ""
	}
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var e entry
			if json.Unmarshal(line, &e) == nil && !e.IsSidechain && len(e.Message) > 0 {
				var m message
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
						history = append(history, json.RawMessage(mustJSON(map[string]any{"role": m.Role, "content": json.RawMessage(m.Content)})))
					}
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return turns, err
		}
	}
	flush()
	return turns, nil
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// engineFor returns an engine with exactly one optimizer in the given state. shadow means "would remove".
func engineFor(name, state string) *optimize.Engine {
	cfg := config.Default()
	for _, n := range optimize.PipelineNames {
		cfg.Optimizers[n] = config.StateOff
	}
	cfg.Optimizers[name] = state
	cfg.Optimize.CustomAgents = nil
	cfg.Optimize.BudgetMS = 5000
	return optimize.New(optimize.Options{Config: cfg, Getenv: func(string) string { return "" }})
}

type tally struct {
	requests, changed, failed, skipped, tooBig int
	bytesBefore, bytesRemoved                  int64
	tokensRemoved                              int64
}

func main() {
	dir := flag.String("dir", "", "folder of Claude Code session logs (JSONL)")
	perSession := flag.Int("per", 8, "requests sampled per session, evenly spaced")
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "usage: replay -dir <claude projects folder> [-per N]")
		os.Exit(2)
	}

	var files []string
	_ = filepath.WalkDir(*dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".jsonl") {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)

	// The optimizers that act on a Claude Code request without a custom-agent marker, plus the trimmer in shadow.
	targets := []struct{ name, state string }{
		{"noise_normalizer", config.StateOn},
		{"duplicate_result_collapser", config.StateOn},
		{"output_trimmer", config.StateShadow},
	}
	engines := map[string]*optimize.Engine{}
	totals := map[string]*tally{}
	for _, tg := range targets {
		engines[tg.name] = engineFor(tg.name, tg.state)
		totals[tg.name] = &tally{}
	}

	sessions, turnsSeen, parseErrs := 0, 0, 0
	for _, p := range files {
		turns, err := readTurns(p)
		if err != nil {
			parseErrs++
		}
		if len(turns) == 0 {
			continue
		}
		sessions++
		turnsSeen += len(turns)
		picks := pick(len(turns), *perSession)
		for _, idx := range picks {
			t := turns[idx]
			if len(t.before) == 0 {
				continue
			}
			model := t.model
			if model == "" {
				model = "claude-sonnet-5-5"
			}
			body := mustJSON(map[string]any{"model": model, "max_tokens": 1024, "messages": t.before})
			if len(body) > maxBody {
				for _, tg := range targets {
					totals[tg.name].tooBig++
				}
				continue
			}
			for _, tg := range targets {
				tl := totals[tg.name]
				tl.requests++
				tl.bytesBefore += int64(len(body))
				rq := optimize.RequestIn{
					Provider: "anthropic", Path: "/v1/messages",
					Header: http.Header{"Content-Type": []string{"application/json"}},
					Body:   body,
				}
				res := engines[tg.name].TransformRequest(context.Background(), rq, optimize.Session{Key: fmt.Sprintf("%s#%d", p, idx), Agent: "claude_code"})
				if len(res.Failures) > 0 {
					tl.failed++
				}
				if res.Skipped != "" {
					tl.skipped++
				}
				if res.Changed {
					tl.changed++
				}
				// Shadow and on report the same estimate per applied optimizer (Result.TokensRemoved counts on only).
				for _, a := range res.Applied {
					if a.Name == tg.name {
						tl.tokensRemoved += a.TokensRemoved
						if tg.state == config.StateShadow {
							tl.bytesRemoved += int64(a.BytesRemoved)
						}
					}
				}
				if tg.state == config.StateOn && res.Changed {
					tl.bytesRemoved += int64(len(body) - len(res.Body))
				}
			}
		}
	}

	fmt.Printf("# sessions\t%d\n", sessions)
	fmt.Printf("# assistant turns found\t%d\n", turnsSeen)
	fmt.Printf("# log files that could not be fully read\t%d\n", parseErrs)
	fmt.Printf("# limits\tsystem prompt and tool definitions are not in the logs; requests are sampled; token figures are engine estimates\n")
	fmt.Println("optimizer\tmode\trequests\tchanged\tchanged_pct\tbytes_before\tbytes_removed\tbytes_removed_pct\test_tokens_removed\tfailures\tskipped\ttoo_big")
	for _, tg := range targets {
		tl := totals[tg.name]
		mode := "on"
		if tg.state == config.StateShadow {
			mode = "shadow (estimate only)"
		}
		chPct, brPct := 0.0, 0.0
		if tl.requests > 0 {
			chPct = 100 * float64(tl.changed) / float64(tl.requests)
		}
		if tl.bytesBefore > 0 {
			brPct = 100 * float64(tl.bytesRemoved) / float64(tl.bytesBefore)
		}
		fmt.Printf("%s\t%s\t%d\t%d\t%.1f\t%d\t%d\t%.2f\t%d\t%d\t%d\t%d\n",
			tg.name, mode, tl.requests, tl.changed, chPct, tl.bytesBefore, tl.bytesRemoved, brPct,
			tl.tokensRemoved, tl.failed, tl.skipped, tl.tooBig)
	}
}

// pick returns up to n indices, evenly spaced over [0, total).
func pick(total, n int) []int {
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
