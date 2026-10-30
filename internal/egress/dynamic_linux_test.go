//go:build linux

package egress

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// This is the network-monitoring harness: it runs the real, built binary under
// strace, exercises the commands and the gateway, and fails if the process ever
// connects or sends to an address that is not loopback. The gateway's upstream
// is a local fake, so "forwarding the user's API request" is also loopback here;
// that the real upstream is the only remote one is covered by the allowlist test
// in static_test.go.
//
// CI installs strace and sets TROVE_REQUIRE_STRACE=1 so a missing tool fails the
// job instead of silently skipping it.

func needStrace(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("strace")
	if err != nil {
		if os.Getenv("TROVE_REQUIRE_STRACE") == "1" {
			t.Fatal("strace is required in CI but was not found")
		}
		t.Skip("strace is not installed")
	}
	return p
}

func buildBinary(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "trove")
	cmd := exec.Command("go", "build", "-trimpath", "-o", exe, "github.com/usetrove/trove/cmd/trove")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return exe
}

type sandbox struct {
	exe, home, data, strace string
	env                     []string
}

func newSandbox(t *testing.T, extraEnv ...string) *sandbox {
	t.Helper()
	s := &sandbox{strace: needStrace(t)} // before the build, so a machine without strace skips fast
	s.exe = buildBinary(t)
	s.home = t.TempDir()
	s.data = filepath.Join(s.home, ".trove")
	// A deliberately small environment: no proxies, no tokens, nothing inherited
	// that could make a connection look intended.
	s.env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + s.home, "TROVE_DATA_DIR=" + s.data}, extraEnv...)
	// Some agent history to read, so the log-reading code paths run too.
	hist := filepath.Join(s.home, ".claude", "projects", "p")
	if err := os.MkdirAll(hist, 0o755); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(repoRoot(t), "internal", "logs", "claudecode", "testdata", "v2_basic.jsonl")); err == nil {
		_ = os.WriteFile(filepath.Join(hist, "s.jsonl"), b, 0o644)
	}
	return s
}

// traced starts the binary under strace, writing the trace to logPath.
func (s *sandbox) traced(logPath string, args ...string) *exec.Cmd {
	a := []string{"-f", "-qq", "-s", "256", "-o", logPath, "-e", "trace=connect,sendto,sendmsg,sendmmsg", s.exe}
	cmd := exec.Command(s.strace, append(a, args...)...)
	cmd.Env = s.env
	cmd.Dir = s.home
	return cmd
}

func assertLoopbackOnly(t *testing.T, what, logPath string) {
	t.Helper()
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("%s: no strace log: %v", what, err)
	}
	remote, err := remoteAddrs(string(b))
	if err != nil {
		t.Errorf("%s: %v", what, err)
	}
	if len(remote) > 0 {
		t.Errorf("%s connected or sent to non-loopback addresses: %v", what, remote)
	}
}

func TestCommandsMakeNoOutboundConnections(t *testing.T) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("strace output format only checked on amd64 and arm64")
	}
	s := newSandbox(t)
	for i, args := range [][]string{
		{"version"}, {"doctor"}, {"report"}, {"report", "--json"}, {"pricing", "show"},
		{"hooks", "status"}, {"mcp", "status"}, {"mcp", "schema-size"}, {"fix"}, {"undo"},
		{"uninstall", "--dry-run"}, {"setup", "--dry-run"},
	} {
		logPath := filepath.Join(t.TempDir(), fmt.Sprintf("strace-%d.log", i))
		cmd := s.traced(logPath, args...)
		cmd.Stdin = strings.NewReader("")
		done := make(chan struct{})
		go func() { _ = cmd.Run(); close(done) }() // exit status is irrelevant here; only the network is
		select {
		case <-done:
		case <-time.After(60 * time.Second):
			_ = cmd.Process.Kill()
			t.Errorf("trove %s did not finish in 60s", strings.Join(args, " "))
		}
		assertLoopbackOnly(t, "trove "+strings.Join(args, " "), logPath)
	}
}

func TestGatewayOnlyTalksToItsConfiguredUpstream(t *testing.T) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("strace output format only checked on amd64 and arm64")
	}
	var upstreamHits int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":3,"output_tokens":2}}`))
	}))
	defer up.Close()

	s := newSandbox(t, "TROVE_UPSTREAMS_ANTHROPIC="+up.URL)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	logPath := filepath.Join(t.TempDir(), "gateway.strace")
	gw := s.traced(logPath, "gateway", "start", "--listen", addr)
	if err := gw.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = gw.Wait(); close(exited) }()
	defer func() {
		stop := exec.Command(s.exe, "gateway", "stop")
		stop.Env = s.env
		_ = stop.Run()
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			_ = gw.Process.Kill()
		}
	}()

	base := "http://" + addr
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp, err := http.Get(base + "/trove/health")
		if err == nil {
			_ = resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("gateway never came up: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}

	for i := 0; i < 3; i++ {
		req, _ := http.NewRequest("POST", base+"/anthropic/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","max_tokens":5,"messages":[{"role":"user","content":"hello"}]}`))
		req.Header.Set("x-api-key", "sk-ant-api03-EGRESSTESTKEY0123456789")
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("content-type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("request %d: status %d", i, resp.StatusCode)
		}
	}
	if upstreamHits != 3 {
		t.Fatalf("upstream saw %d requests, want 3", upstreamHits)
	}
	time.Sleep(500 * time.Millisecond) // let the recorder and measurement loops run once

	stop := exec.Command(s.exe, "gateway", "stop")
	stop.Env = s.env
	_ = stop.Run()
	select {
	case <-exited:
	case <-time.After(15 * time.Second):
		_ = gw.Process.Kill()
		<-exited
	}
	assertLoopbackOnly(t, "the gateway", logPath)

	// The trace must contain the loopback connections we know happened, otherwise
	// strace saw nothing and the assertion above proved nothing.
	b, _ := os.ReadFile(logPath)
	if !strings.Contains(string(b), "127.0.0.1") {
		t.Errorf("the strace log has no loopback connection at all; the harness is not tracing. First lines:\n%s", firstLines(string(b), 8))
	}
}

func firstLines(s string, n int) string {
	l := strings.SplitN(s, "\n", n+1)
	if len(l) > n {
		l = l[:n]
	}
	return strings.Join(l, "\n")
}
