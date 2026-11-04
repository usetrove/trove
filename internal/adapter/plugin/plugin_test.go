package plugin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/optimize"
)

func newServer() *Server {
	cfg := config.Default()
	cfg.Optimize.BudgetMS = 1000 // tests must not depend on machine load; the budget itself is tested in package optimize
	return New(optimize.New(optimize.Options{Config: cfg, Getenv: func(string) string { return "" }}))
}

func call(t testing.TB, s *Server, m any) Reply {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return s.Handle(context.Background(), b)
}

const noisy = "\x1b[32mok\x1b[0m line\nprogress 10%\rprogress 50%\rprogress 100%\n"

func TestToolOutputRoundTrip(t *testing.T) {
	s := newServer()
	on := map[string]string{"noise_normalizer": "on"}
	rep := call(t, s, Message{ID: json.RawMessage(`7`), Method: "tool_output", Session: Session{Key: "k", States: on},
		Call: &optimize.ToolCall{Name: "Bash"}, Output: &optimize.ToolOutput{Parts: []optimize.Part{{Text: noisy}}}})
	if !rep.OK || string(rep.ID) != "7" || rep.Output == nil || len(rep.Reports) != 1 || !rep.Reports[0].Changed {
		t.Fatalf("%+v", rep)
	}
	if strings.Contains(rep.Output.Parts[0].Text, "\x1b[") {
		t.Fatalf("ANSI not removed: %q", rep.Output.Parts[0].Text)
	}
	// An optimizer the plugin did not switch on does nothing.
	rep = call(t, s, Message{Method: "tool_output", Session: Session{Key: "k"}, Call: &optimize.ToolCall{Name: "Bash"},
		Output: &optimize.ToolOutput{Parts: []optimize.Part{{Text: noisy}}}})
	if !rep.OK || rep.Output.Parts[0].Text != noisy || len(rep.Reports) != 0 {
		t.Fatalf("off must change nothing: %+v", rep)
	}
}

func TestToolInputLargeOutputAdvice(t *testing.T) {
	s := newServer()
	rep := call(t, s, Message{Method: "tool_input", Session: Session{Key: "k", TroveRun: true, States: map[string]string{"hook_large_output": "on"}},
		Call: &optimize.ToolCall{Name: "Bash", Input: json.RawMessage(`{"command":"go test ./..."}`)}})
	if !rep.OK || len(rep.Reports) != 1 || rep.Reports[0].Decision == nil || rep.Reports[0].Decision.Kind != optimize.KindAdvised || rep.Reports[0].Decision.Message == "" {
		t.Fatalf("%+v", rep)
	}
}

func TestRepeatReadKeepsLedgerAcrossCalls(t *testing.T) {
	path := t.TempDir() + string(os.PathSeparator) + "a.txt"
	if err := os.WriteFile(path, []byte("package a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newServer()
	st := Session{Key: "k", Prompts: 1, States: map[string]string{"hook_repeat_read": "on"}}
	in, _ := json.Marshal(map[string]any{"file_path": path})
	c := &optimize.ToolCall{Name: "Read", Input: in}
	if r := call(t, s, Message{Method: "tool_output", Session: st, Call: c, Output: &optimize.ToolOutput{Parts: []optimize.Part{{Text: "package a"}}}}); !r.OK || len(r.Reports) != 1 || r.Reports[0].Err != "" {
		t.Fatalf("the read was not recorded: %+v", r)
	}
	r := call(t, s, Message{Method: "tool_input", Session: st, Call: c})
	if !r.OK || len(r.Reports) != 1 || r.Reports[0].Decision == nil || r.Reports[0].Decision.Kind != optimize.KindBlocked {
		t.Fatalf("a repeat read should be blocked once: %+v", r)
	}
	r = call(t, s, Message{Method: "tool_input", Session: st, Call: c})
	if len(r.Reports) != 1 || r.Reports[0].Decision == nil || r.Reports[0].Decision.Kind != optimize.KindRereadAnyway {
		t.Fatalf("the retry must go through: %+v", r)
	}
}

func TestRequestMethodAndCapabilities(t *testing.T) {
	s := newServer()
	body := []byte(`{"model":"claude-sonnet-4-5","messages":[]}`)
	r := call(t, s, Message{Method: "request", Session: Session{Key: "k"}, Request: &RequestIn{Provider: "anthropic", Path: "/v1/messages", Body: body}})
	if !r.OK || r.Request == nil || !bytes.Equal(r.Request.Body, body) || r.Request.Changed {
		t.Fatalf("%+v", r)
	}
	if r := call(t, s, Message{Method: "capabilities"}); !r.OK || len(r.Matrix) != len(optimize.Matrix()) {
		t.Fatalf("%+v", r)
	}
}

func TestFailsOpenOnBadInput(t *testing.T) {
	s := newServer()
	for name, line := range map[string]string{
		"garbage":        `not json`,
		"unknown method": `{"id":1,"method":"nope"}`,
		"missing call":   `{"id":2,"method":"tool_input"}`,
		"missing output": `{"id":3,"method":"tool_output","call":{"name":"Bash"}}`,
		"missing req":    `{"id":4,"method":"request"}`,
	} {
		if r := s.Handle(context.Background(), []byte(line)); r.OK || r.Error == "" || r.Call != nil || r.Output != nil || r.Request != nil {
			t.Errorf("%s: %+v", name, r)
		}
	}
}

func TestServeLoop(t *testing.T) {
	in := strings.NewReader(`{"id":1,"method":"capabilities"}` + "\n\n" + `garbage` + "\n" + `{"id":2,"method":"nope"}` + "\n")
	var out bytes.Buffer
	if err := newServer().Serve(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], `"ok":true`) || strings.Contains(lines[1], `"ok":true`) {
		t.Fatalf("replies:\n%s", out.String())
	}
}

// The protocol must have no field a credential could travel in (CLAUDE.md rule 2).
func TestProtocolHasNoCredentialFields(t *testing.T) {
	banned := map[string]bool{"headers": true, "header": true, "authorization": true, "api_key": true, "x-api-key": true,
		"token": true, "secret": true, "cookie": true, "cookies": true, "credentials": true, "password": true}
	for _, typ := range []reflect.Type{reflect.TypeOf(Message{}), reflect.TypeOf(RequestIn{}), reflect.TypeOf(Session{})} {
		for i := 0; i < typ.NumField(); i++ {
			name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
			if banned[strings.ToLower(name)] {
				t.Errorf("%s.%s is a credential-shaped field", typ.Name(), typ.Field(i).Name)
			}
		}
	}
}

// --- benchmarks: per-call overhead of the plugin path ---

type loop struct {
	w  io.Writer
	rd *bufio.Reader
}

func (l *loop) roundTrip(b testing.TB, msg []byte) {
	if _, err := l.w.Write(append(append([]byte(nil), msg...), '\n')); err != nil {
		b.Fatal(err)
	}
	if _, err := l.rd.ReadBytes('\n'); err != nil {
		b.Fatal(err)
	}
}

// inProcess runs Serve over pipes: protocol plus engine, no process boundary.
func inProcess(b testing.TB) *loop {
	cr, cw := io.Pipe() // client -> server
	sr, sw := io.Pipe() // server -> client
	go func() { _ = newServer().Serve(context.Background(), cr, sw); _ = sw.Close() }()
	b.Cleanup(func() { _ = cw.Close() })
	return &loop{w: cw, rd: bufio.NewReaderSize(sr, 1<<20)}
}

// subprocess runs the real binary named by TROVE_BENCH_BIN over OS pipes and
// returns the cold start (spawn to first reply) alongside.
func subprocess(b testing.TB) (*loop, time.Duration) {
	bin := os.Getenv("TROVE_BENCH_BIN")
	if bin == "" {
		b.Skip("set TROVE_BENCH_BIN to a built trove binary to measure the real subprocess")
	}
	cmd := exec.Command(bin, "engine", "serve", "--data-dir", b.TempDir())
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	start := time.Now()
	if err := cmd.Start(); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = stdin.Close(); _ = cmd.Wait() })
	l := &loop{w: stdin, rd: bufio.NewReaderSize(stdout, 1<<20)}
	l.roundTrip(b, []byte(`{"id":0,"method":"capabilities"}`))
	return l, time.Since(start)
}

func enc(b testing.TB, v any) []byte {
	x, err := json.Marshal(v)
	if err != nil {
		b.Fatal(err)
	}
	return x
}

func messages(b testing.TB) map[string][]byte {
	big := strings.Repeat("=== RUN   TestCase\n--- PASS: TestCase (0.00s)\n", 2500) // ~100 KB
	on := map[string]string{"noise_normalizer": "on", "hook_large_output": "on"}
	body := enc(b, map[string]any{"model": "claude-sonnet-4-5", "max_tokens": 64, "messages": []any{
		map[string]any{"role": "user", "content": strings.Repeat("hello world\n", 16000)}}}) // ~190 KB
	return map[string][]byte{
		"tool_input_small": enc(b, Message{Method: "tool_input", Session: Session{Key: "k", States: on, TroveRun: true},
			Call: &optimize.ToolCall{Name: "Bash", Input: json.RawMessage(`{"command":"ls"}`)}}),
		"tool_output_1KB": enc(b, Message{Method: "tool_output", Session: Session{Key: "k", States: on}, Call: &optimize.ToolCall{Name: "Bash"},
			Output: &optimize.ToolOutput{Parts: []optimize.Part{{Text: strings.Repeat("ok line\n", 128)}}}}),
		"tool_output_100KB": enc(b, Message{Method: "tool_output", Session: Session{Key: "k", States: on}, Call: &optimize.ToolCall{Name: "Bash"},
			Output: &optimize.ToolOutput{Parts: []optimize.Part{{Text: big}}}}),
		"request_190KB": enc(b, Message{Method: "request", Session: Session{Key: "k"},
			Request: &RequestIn{Provider: "anthropic", Path: "/v1/messages", Body: body}}),
	}
}

func BenchmarkInProcess(b *testing.B) {
	for name, msg := range messages(b) {
		b.Run(name, func(b *testing.B) {
			l := inProcess(b)
			b.SetBytes(int64(len(msg)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				l.roundTrip(b, msg)
			}
		})
	}
}

func BenchmarkSubprocess(b *testing.B) {
	for name, msg := range messages(b) {
		b.Run(name, func(b *testing.B) {
			l, _ := subprocess(b)
			b.SetBytes(int64(len(msg)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				l.roundTrip(b, msg)
			}
		})
	}
}

func BenchmarkColdStart(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, d := subprocess(b)
		b.ReportMetric(float64(d.Microseconds())/1000, "ms/start")
	}
}
