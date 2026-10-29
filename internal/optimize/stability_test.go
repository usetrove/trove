package optimize

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/config"
)

// sessionTurns builds the history an agent would resend on turn n (1-based): the
// original, untransformed messages so far. Results mix noise, exact repeats and
// ordinary output so every optimizer has work on every turn.
func sessionTurns(n int) []msg {
	msgs := []msg{user(text("fix the failing build"))}
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("toolu_%03d", i)
		var out string
		switch i % 5 {
		case 0: // exact repeat of an earlier read
			out = bigOutput("shared")
		case 1: // noisy build log
			out = "\x1b[33mwarn\x1b[0m " + strings.Repeat("compiling dependency graph node\n", 12+i) + "10%\r60%\rfinished\n"
		case 2: // progress frames
			out = "Downloading 10% [####====]\nDownloading 60% [########]\nDownloading 100% [########]\nok\n"
		case 3:
			out = bigOutput("shared")
		default:
			out = fmt.Sprintf("plain result number %d\nnothing to clean here\n", i)
		}
		msgs = append(msgs, assistant(text("step"), toolUse(id, "bash")), user(toolResult(id, out)))
	}
	return msgs
}

func stabilityEngine(states map[string]string) *Engine {
	cfg := config.Default()
	for k, v := range states {
		cfg.Optimizers[k] = v
	}
	cfg.Optimize.BudgetMS = 1000
	return New(Options{Config: cfg, Getenv: func(string) string { return "" }})
}

// prefixOf returns the transformed body without the closing "]}" so the next
// turn's body can be checked to extend it.
func prefixOf(t *testing.T, b []byte) []byte {
	t.Helper()
	if !bytes.HasSuffix(b, []byte("]}")) {
		t.Fatalf("unexpected body tail: %q", b[len(b)-10:])
	}
	return b[:len(b)-2]
}

// TestCacheStability50Turns simulates a 50-turn session in which the agent
// resends its original history every turn. The transformed prefix of each
// request must be byte-identical to the previous request, or the provider's
// cache would be invalidated by Trove itself.
func TestCacheStability50Turns(t *testing.T) {
	states := map[string]string{}
	for _, n := range PipelineNames {
		states[n] = config.StateOn
	}
	states[config.OptimizerOutputTrimmer] = config.StateShadow

	run := func(t *testing.T, restartEvery int) {
		e := stabilityEngine(states)
		var prev []byte
		changedTurns := 0
		for turn := 1; turn <= 50; turn++ {
			if restartEvery > 0 && turn%restartEvery == 0 {
				e = stabilityEngine(states) // a gateway restart: no state survives, no memo
			}
			body := req{system: "stable system prompt", tools: []string{"bash"}, msgs: sessionTurns(turn)}.body()
			res := e.Run(context.Background(), input(body))
			if len(res.Failures) != 0 {
				t.Fatalf("turn %d failed: %+v", turn, res.Failures)
			}
			if res.Changed {
				changedTurns++
			}
			if prev != nil && !bytes.HasPrefix(res.Body, prefixOf(t, prev)) {
				i := 0
				for i < len(prev) && i < len(res.Body) && prev[i] == res.Body[i] {
					i++
				}
				t.Fatalf("turn %d: transformed prefix differs from turn %d at byte %d", turn, turn-1, i)
			}
			prev = res.Body
		}
		if changedTurns < 40 {
			t.Errorf("the optimizers only changed %d of 50 turns; the test is not exercising them", changedTurns)
		}
	}
	t.Run("steady", func(t *testing.T) { run(t, 0) })
	t.Run("restart every 7 turns", func(t *testing.T) { run(t, 7) })
}
