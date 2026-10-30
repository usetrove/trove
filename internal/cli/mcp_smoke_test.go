package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMCPStdioSubprocess starts the real binary and speaks MCP to it over
// stdin/stdout, as an agent does.
func TestMCPStdioSubprocess(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "trove")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	build := exec.Command("go", "build", "-o", exe, "github.com/usetrove/trove/cmd/trove")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	proj := filepath.Join(dir, "proj")
	_ = os.MkdirAll(proj, 0o755)
	_ = os.WriteFile(filepath.Join(proj, "a.txt"), []byte("hello\n"), 0o644)

	cmd := exec.Command(exe, "mcp")
	cmd.Dir = proj
	cmd.Env = append(os.Environ(), "TROVE_DATA_DIR="+filepath.Join(dir, "data"))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, &sdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()
	res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: "trove_read", Arguments: map[string]any{"path": "a.txt"}})
	if err != nil || res.IsError {
		t.Fatalf("%v %+v", err, res)
	}
	if got := res.Content[0].(*sdk.TextContent).Text; got != "hello\n" {
		t.Fatalf("got %q", got)
	}
}
