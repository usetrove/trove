package optimize

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
)

// historyOfSize builds a long session whose request body is roughly size bytes:
// noisy build logs, repeated reads, test output and ordinary results, the mix
// every optimizer has work on.
func historyOfSize(size int) []byte {
	system := pad(6000) + "Today's date: 2026-10-29\n"
	var body []byte
	for turns := 10; ; turns += 10 {
		body = req{system: system, tools: []string{"bash", "read", "edit", "grep"}, msgs: benchTurns(turns)}.body()
		if len(body) >= size {
			return body
		}
	}
}

func benchTurns(n int) []msg {
	msgs := []msg{user(text("fix the failing build"))}
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("toolu_%04d", i)
		var out string
		switch i % 6 {
		case 0:
			out = bigOutput("shared")
		case 1:
			out = "\x1b[33mwarn\x1b[0m " + strings.Repeat("compiling dependency graph node\n", 20) + "10%\r60%\rfinished\n"
		case 2:
			out = goTestOutput(60)
		case 3:
			out = bigOutput("shared")
		case 4:
			out = strings.Repeat("func helper"+id+"() int { return 42 }\n", 60)
		default:
			out = fmt.Sprintf("plain result number %d\nnothing to clean here\n", i)
		}
		msgs = append(msgs, assistant(text("step"), toolUse(id, "bash")), user(toolResult(id, out)))
	}
	return msgs
}

func allOnEngine() *Engine {
	cfg := config.Default()
	for _, n := range PipelineNames {
		cfg.Optimizers[n] = config.StateOn
	}
	cfg.Optimizers[config.OptimizerOutputTrimmer] = config.StateShadow // the most it can be until Prompt 11
	cfg.Optimize.CustomAgents = []string{"sdk"}
	cfg.Optimize.BudgetMS = 1000
	return New(Options{Config: cfg, Getenv: func(string) string { return "" }})
}

// BenchmarkEngineAllOn measures the engine with every optimizer on, as the
// gateway calls it, at three history sizes.
func BenchmarkEngineAllOn(b *testing.B) {
	for _, size := range []int{50 << 10, 200 << 10, 2 << 20} {
		body := historyOfSize(size)
		b.Run(fmt.Sprintf("%dKB", len(body)>>10), func(b *testing.B) {
			e := allOnEngine()
			in := input(body)
			if res := e.Run(context.Background(), in); !res.Changed || len(res.Failures) != 0 {
				b.Fatalf("benchmark is not exercising the optimizers: %+v", res.Failures)
			}
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				e.Run(context.Background(), in)
			}
		})
	}
}

// TestEngineStaysWithinTheGatewayBudget checks the acceptance criterion: with
// every optimizer on, p50 overhead stays under the gateway's 5 ms budget for a
// typical request. "Typical" is a ~200 KB history, a long agent session. Larger
// bodies are reported by the benchmark. Skipped under -short because it is a
// timing test.
func TestEngineStaysWithinTheGatewayBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	body := historyOfSize(200 << 10)
	e := allOnEngine()
	in := input(body)
	e.Run(context.Background(), in) // warm up: first request pins the session
	const runs = 101
	d := make([]time.Duration, runs)
	for i := range d {
		start := time.Now()
		e.Run(context.Background(), in)
		d[i] = time.Since(start)
	}
	slices.Sort(d)
	t.Logf("%d KB body, all optimizers on: p50 %v, p90 %v, max %v", len(body)>>10, d[runs/2], d[runs*9/10], d[runs-1])
	if p50 := d[runs/2]; p50 > 5*time.Millisecond {
		t.Errorf("p50 %v exceeds the 5 ms gateway budget", p50)
	}
}

// BenchmarkParseRequest isolates the scan every stage depends on.
func BenchmarkParseRequest(b *testing.B) {
	body := historyOfSize(200 << 10)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := ParseRequest(body); err != nil {
			b.Fatal(err)
		}
	}
}
