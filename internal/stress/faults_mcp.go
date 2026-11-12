package stress

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/usetrove/trove/internal/gateway"
	"github.com/usetrove/trove/internal/hooks"
)

// stressTroveBinary is the trove executable the MCP case starts as a child process. The stress command sets it
// to its own executable; tests build one and set it explicitly.
var stressTroveBinary string

// deadMCPFault starts the real MCP server as a child process over stdio, kills it while a call is in flight, and
// checks that: the call returns instead of hanging, every later call fails with an error, and the hook path and
// the gateway keep working. An MCP server that is down must never stop the agent.
func (e *env) deadMCPFault() []Violation {
	bin := stressTroveBinary
	if bin == "" {
		exe, err := os.Executable()
		if err != nil {
			return []Violation{{"setup", "could not find the trove executable to start the MCP server: " + err.Error()}}
		}
		bin = exe
	}
	root := filepath.Join(e.dir, "mcp-project")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return []Violation{{"setup", err.Error()}}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("project\n"), 0o600); err != nil {
		return []Violation{{"setup", err.Error()}}
	}

	cmd := exec.Command(bin, "mcp", "--root", root)
	cmd.Env = append(os.Environ(), "TROVE_DATA_DIR="+filepath.Join(e.dir, "mcp-data"))
	transport := &sdk.CommandTransport{Command: cmd}
	client := sdk.NewClient(&sdk.Implementation{Name: "trove-stress", Version: "0"}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return []Violation{{"mcp_start", "could not start the MCP server: " + err.Error()}}
	}
	defer func() { _ = session.Close() }()

	var vs []Violation
	// A normal call first: the server must work before it is killed.
	if _, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "trove_fetch_full",
		Arguments: map[string]any{"output_id": "0123456789abcdef"}}); err != nil {
		return []Violation{{"mcp_start", "a normal call failed before the kill: " + err.Error()}}
	}

	// A call in flight when the process dies.
	inFlight := make(chan error, 1)
	go func() {
		_, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "trove_fetch_full",
			Arguments: map[string]any{"output_id": "0123456789abcdef"}})
		inFlight <- err
	}()
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	select {
	case <-inFlight:
		// Returned, whether it succeeded just before the kill or failed after it. Both are fine: the point is that
		// it did not hang.
	case <-time.After(10 * time.Second):
		vs = append(vs, Violation{InvFailOpenOnError, "a call in flight when the MCP server died did not return"})
	}

	// After the kill, calls must fail with an error, not succeed and not hang.
	callCtx, callCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer callCancel()
	_, err = session.CallTool(callCtx, &sdk.CallToolParams{Name: "trove_fetch_full",
		Arguments: map[string]any{"output_id": "0123456789abcdef"}})
	switch {
	case err == nil:
		vs = append(vs, Violation{InvFailOpenOnError, "a call to a dead MCP server reported success"})
	case errors.Is(callCtx.Err(), context.DeadlineExceeded):
		vs = append(vs, Violation{InvFailOpenOnError, "a call to a dead MCP server hung until the deadline"})
	}

	// The rest of Trove must not depend on the MCP server.
	var out bytes.Buffer
	code := hooks.Main(hooks.Env{Args: []string{hooks.EventPreRead, "--data-dir", filepath.Join(e.dir, "mcp-hooks")},
		Stdin: strings.NewReader("{}"), Stdout: &out, Getenv: func(string) string { return "" }})
	if code != 0 {
		vs = append(vs, Violation{InvFailOpenOnError, fmt.Sprintf("the hook path exited %d while the MCP server was dead", code)})
	}
	srv, err := newTestGateway(fakeUpstream{}, &countingRecorder{})
	if err != nil {
		return append(vs, Violation{"gateway_start", err.Error()})
	}
	defer srv.Close()
	client2 := &http.Client{Timeout: 5 * time.Second}
	h, err := client2.Get(srv.URL + gateway.HealthPath)
	if err != nil || h.StatusCode != http.StatusOK {
		vs = append(vs, Violation{InvFailOpenOnError, "the gateway did not answer its health check while the MCP server was dead"})
	}
	if h != nil {
		_ = h.Body.Close()
	}
	return vs
}
