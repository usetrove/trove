package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/mcp/root"
	"github.com/usetrove/trove/internal/recovery"
)

// TestHelperProcess is not a test: the run tests execute this test binary as the
// "project command", so they need no tools installed and work on every OS.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("TROVE_HELPER") != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	switch args[0] {
	case "pass":
		fmt.Println("ok  \texample/pkg\t0.01s")
	case "fail":
		fmt.Print(bigFailingGoTest())
		os.Exit(1)
	case "sleep":
		time.Sleep(30 * time.Second)
	case "env":
		_, has := os.LookupEnv("ANTHROPIC_API_KEY")
		fmt.Printf("anthropic key present: %v\n", has)
	case "args":
		fmt.Println(strings.Join(args[1:], "|"))
	}
	os.Exit(0)
}

func bigFailingGoTest() string {
	var b strings.Builder
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&b, "=== RUN   TestCase%d\n--- PASS: TestCase%d (0.00s)\n", i, i)
	}
	b.WriteString("=== RUN   TestBad\n    bad_test.go:12: got 1, want 2\n--- FAIL: TestBad (0.00s)\n")
	b.WriteString("panic: boom\n\ngoroutine 7 [running]:\nexample/pkg.Do(...)\n\t/src/pkg/do.go:44 +0x1d\n")
	b.WriteString("FAIL\texample/pkg\t0.123s\nFAIL\n")
	return b.String()
}

type memBlob struct {
	data map[string][]byte
	fail bool
}

func (m *memBlob) Put(d []byte) (string, error) {
	if m.fail {
		return "", errors.New("disk full")
	}
	if m.data == nil {
		m.data = map[string][]byte{}
	}
	id := recovery.ID(d)
	m.data[id] = d
	return id, nil
}

func runProject(t *testing.T, cmds map[string][]string, timeouts map[string]int) *root.Root {
	t.Helper()
	t.Setenv("TROVE_HELPER", "1")
	dir := t.TempDir()
	var b strings.Builder
	exe, _ := os.Executable()
	for name, extra := range cmds {
		argv := append([]string{exe, "-test.run=^TestHelperProcess$", "--"}, extra...)
		qs := make([]string, len(argv))
		for i, a := range argv {
			qs[i] = fmt.Sprintf("%q", a)
		}
		fmt.Fprintf(&b, "[commands.%s]\nrun = [%s]\n", name, strings.Join(qs, ", "))
		if s := timeouts[name]; s > 0 {
			fmt.Fprintf(&b, "timeout_sec = %d\n", s)
		}
	}
	write(t, dir, ".trove/commands.toml", b.String())
	r, err := root.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRunPass(t *testing.T) {
	r := runProject(t, map[string][]string{"test": {"pass"}}, nil)
	blob := &memBlob{}
	got, err := Run(context.Background(), r, blob, allowAll{}, RunArgs{Name: "test"})
	if err != nil || !strings.HasPrefix(got, "test: PASS") || !strings.Contains(got, "output_id: ") {
		t.Fatalf("%v\n%s", err, got)
	}
}

func TestRunFailCondensesAndIsRecoverable(t *testing.T) {
	r := runProject(t, map[string][]string{"test": {"fail"}}, nil)
	blob := &memBlob{}
	got, err := Run(context.Background(), r, blob, allowAll{}, RunArgs{Name: "test"})
	if err != nil {
		t.Fatal(err)
	}
	raw := bigFailingGoTest()
	for _, want := range []string{"test: FAIL (exit 1", "failing: TestBad", "panic: boom", "do.go:44", "bad_test.go:12: got 1, want 2", "lines trimmed; full output sha256:"} {
		if !strings.Contains(got, want) {
			t.Errorf("result missing %q:\n%s", want, got)
		}
	}
	if len(got)*4 > len(raw) {
		t.Errorf("result %d bytes vs raw %d: want at least a 4x reduction", len(got), len(raw))
	}
	// Every trimmed output is retrievable in full, byte for byte.
	id := recovery.ID([]byte(raw))
	if !strings.Contains(got, "output_id: "+id) {
		t.Fatalf("output id %s not reported:\n%s", id, got)
	}
	if string(blob.data[id]) != raw {
		t.Error("stored output differs from the command's raw output")
	}
}

func TestRunStoreFailureDoesNotTrim(t *testing.T) {
	r := runProject(t, map[string][]string{"test": {"fail"}}, nil)
	got, err := Run(context.Background(), r, &memBlob{fail: true}, allowAll{}, RunArgs{Name: "test"})
	if err != nil || strings.Contains(got, "lines trimmed") || !strings.Contains(got, "could not be stored") {
		t.Fatalf("must not trim without a stored copy: %v\n%s", err, got)
	}
}

func TestRunNotOnAllowlist(t *testing.T) {
	r := runProject(t, map[string][]string{"test": {"pass"}, "lint": {"pass"}}, nil)
	for _, name := range []string{"deploy", "", "test; echo pwned", "test && ls", "../test", "$(id)", "TEST", "test ", "rm -rf /"} {
		got, err := Run(context.Background(), r, &memBlob{}, allowAll{}, RunArgs{Name: name})
		if err == nil || got != "" {
			t.Errorf("%q must be refused, got %q", name, got)
		}
	}
	_, err := Run(context.Background(), r, &memBlob{}, allowAll{}, RunArgs{Name: "deploy"})
	if !strings.Contains(err.Error(), "Allowed: lint, test") {
		t.Errorf("error should list allowed names: %v", err)
	}
}

func TestRunNoAllowlistFile(t *testing.T) {
	r, _ := root.New(t.TempDir())
	_, err := Run(context.Background(), r, &memBlob{}, allowAll{}, RunArgs{Name: "test"})
	if err == nil || !strings.Contains(err.Error(), "commands.toml") {
		t.Fatalf("want guidance to create the file, got %v", err)
	}
}

func TestRunArgumentsAreNotShellInterpreted(t *testing.T) {
	r := runProject(t, map[string][]string{"echo": {"args", "a b", "; echo pwned", "$(id)", "&&"}}, nil)
	blob := &memBlob{}
	got, err := Run(context.Background(), r, blob, allowAll{}, RunArgs{Name: "echo"})
	if err != nil || !strings.Contains(got, "a b|; echo pwned|$(id)|&&") {
		t.Fatalf("%v\n%s", err, got)
	}
}

func TestRunTimeoutKills(t *testing.T) {
	r := runProject(t, map[string][]string{"slow": {"sleep"}}, map[string]int{"slow": 1})
	start := time.Now()
	got, err := Run(context.Background(), r, &memBlob{}, allowAll{}, RunArgs{Name: "slow"})
	if err != nil || !strings.Contains(got, "TIMEOUT after 1s") {
		t.Fatalf("%v\n%s", err, got)
	}
	if time.Since(start) > 15*time.Second {
		t.Error("timeout did not stop the process promptly")
	}
}

func TestRunDropsCredentialEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-should-not-leak")
	r := runProject(t, map[string][]string{"env": {"env"}}, nil)
	blob := &memBlob{}
	got, err := Run(context.Background(), r, blob, allowAll{}, RunArgs{Name: "env"})
	if err != nil || strings.Contains(got, "sk-ant") || !strings.Contains(got, "anthropic key present: false") {
		t.Fatalf("%v\n%s", err, got)
	}
}

func TestRunMissingProgram(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ".trove/commands.toml", "[commands.test]\nrun = [\"definitely-not-a-real-program-xyz\"]\n")
	r, _ := root.New(dir)
	_, err := Run(context.Background(), r, &memBlob{}, allowAll{}, RunArgs{Name: "test"})
	if err == nil || !strings.Contains(err.Error(), "could not start") {
		t.Fatalf("got %v", err)
	}
}

func TestLoadCommandsValidation(t *testing.T) {
	tests := []struct{ name, toml, want string }{
		{"bad toml", "[commands\n", "not valid"},
		{"empty run", "[commands.test]\nrun = []\n", "needs run"},
		{"bad name", "[commands.\"a b\"]\nrun = [\"x\"]\n", "not allowed"},
		{"bad timeout", "[commands.test]\nrun = [\"x\"]\ntimeout_sec = 999999\n", "timeout_sec"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, ".trove/commands.toml", tc.toml)
			r, _ := root.New(dir)
			if _, err := LoadCommands(r); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

func TestAllowlistOutsideProjectIgnored(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "commands.toml")
	_ = os.WriteFile(outside, []byte("[commands.test]\nrun = [\"echo\"]\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, ".trove"), 0o755)
	if err := os.Symlink(outside, filepath.Join(dir, ".trove", "commands.toml")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	r, _ := root.New(dir)
	if _, err := LoadCommands(r); err == nil {
		t.Fatal("an allowlist that is a symlink out of the project must not be used")
	}
}

func TestFailingNames(t *testing.T) {
	out := "--- FAIL: TestA (0s)\n--- FAIL: TestA (0s)\nFAILED tests/test_x.py::test_y - assert\nFAIL\texample/pkg\t0.1s\nFAIL\n"
	got := strings.Join(failingNames([]byte(out)), ",")
	if got != "TestA,tests/test_x.py::test_y,example/pkg" {
		t.Errorf("got %q", got)
	}
}
