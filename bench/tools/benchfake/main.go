// Command benchfake is a test double for trovebench's no-spend end-to-end test.
//
//	benchfake mock  -addr-file F          a deterministic stand-in for the Anthropic Messages API
//	benchfake agent -turns N -model M     a fake "coding agent" that talks to $ANTHROPIC_BASE_URL
//
// It never contacts the real API. The mock only binds to loopback, which is the
// one place Trove lets an upstream use plain http.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: benchfake mock|agent [flags]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "mock":
		os.Exit(runMock(os.Args[2:]))
	case "agent":
		os.Exit(runAgent(os.Args[2:]))
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		os.Exit(2)
	}
}

func runMock(args []string) int {
	fs := flag.NewFlagSet("mock", flag.ExitOnError)
	addrFile := fs.String("addr-file", "", "file to write the listen address to")
	_ = fs.Parse(args)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mock: cannot listen:", err)
		return 1
	}
	if *addrFile != "" {
		if err := os.WriteFile(*addrFile, []byte(ln.Addr().String()), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, "mock: cannot write addr file:", err)
			return 1
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &req)
		if req.Model == "" {
			req.Model = "claude-haiku-5-5"
		}
		// Deterministic usage: a pure function of the request body size.
		in := len(body) / 4
		resp := map[string]any{
			"id": "msg_mock", "type": "message", "role": "assistant", "model": req.Model,
			"content":     []map[string]any{{"type": "text", "text": "ok"}},
			"stop_reason": "end_turn",
			"usage": map[string]any{
				"input_tokens": in, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0,
				"output_tokens": 20, "service_tier": "standard",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.Serve(ln); err != nil {
		fmt.Fprintln(os.Stderr, "mock:", err)
		return 1
	}
	return 0
}

type usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func runAgent(args []string) int {
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	turns := fs.Int("turns", 3, "number of requests to send")
	model := fs.String("model", "claude-haiku-5-5", "model name to put in requests")
	solution := fs.String("solution", "", "optional shell script that 'solves' the task")
	_ = fs.Parse(args)

	base := strings.TrimRight(os.Getenv("ANTHROPIC_BASE_URL"), "/")
	key := os.Getenv("ANTHROPIC_API_KEY")
	if base == "" || key == "" {
		fmt.Fprintln(os.Stderr, "agent: ANTHROPIC_BASE_URL and ANTHROPIC_API_KEY must be set")
		return 1
	}
	// The same tool output repeated every turn, so duplicate/noise optimizers have something to act on.
	toolOut := strings.Repeat("build log line with trailing spaces   \n", 200)
	var in, out int
	msgs := []any{map[string]any{"role": "user", "content": "fix the failing test"}}
	for i := 0; i < *turns; i++ {
		body, _ := json.Marshal(map[string]any{
			"model": *model, "max_tokens": 1024, "system": "You are a coding agent.", "messages": msgs,
		})
		req, _ := http.NewRequest(http.MethodPost, base+"/v1/messages", bytes.NewReader(body))
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("content-type", "application/json")
		resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
		if err != nil {
			fmt.Fprintln(os.Stderr, "agent: request failed:", err)
			return 1
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			fmt.Fprintf(os.Stderr, "agent: status %d: %s\n", resp.StatusCode, string(raw))
			return 1
		}
		var parsed struct {
			Usage usage `json:"usage"`
		}
		_ = json.Unmarshal(raw, &parsed)
		in += parsed.Usage.InputTokens
		out += parsed.Usage.OutputTokens
		id := fmt.Sprintf("toolu_%d", i)
		msgs = append(msgs,
			map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": id, "name": "Bash", "input": map[string]any{"command": "make test"}}}},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": toolOut}}},
		)
	}
	if *solution != "" {
		if err := exec.Command("bash", *solution).Run(); err != nil {
			fmt.Fprintln(os.Stderr, "agent: solution script failed:", err)
		}
	}
	res := map[string]any{
		"type": "result", "subtype": "success", "is_error": false, "num_turns": *turns, "session_id": "fake",
		"total_cost_usd": 0,
		"usage":          map[string]any{"input_tokens": in, "output_tokens": out, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0},
		"modelUsage": map[string]any{*model: map[string]any{
			"inputTokens": in, "outputTokens": out, "cacheReadInputTokens": 0, "cacheCreationInputTokens": 0, "costUSD": 0,
		}},
	}
	_ = json.NewEncoder(os.Stdout).Encode(res)
	return 0
}
