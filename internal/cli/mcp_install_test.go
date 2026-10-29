package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/mcp/install"
)

// mcpHarness fakes a machine with both agents installed and records every
// external command Trove would run.
func mcpHarness(t *testing.T, binaries ...string) (*harness, *[]string, string) {
	t.Helper()
	if len(binaries) == 0 {
		binaries = []string{"claude", "codex"}
	}
	h := newHarness(t, nil, binaries...)
	home, _ := h.app.HomeDir()
	for _, d := range []string{".claude", ".codex"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var calls []string
	h.app.Executable = func() (string, error) { return filepath.Join(home, "bin", "trove"), nil }
	h.app.Run = func(name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if len(args) > 1 && args[1] == "get" {
			return errors.New("No MCP server found with name: trove")
		}
		return nil
	}
	return h, &calls, home
}

func codexCfg(home string) string { return filepath.Join(home, ".codex", "config.toml") }

func TestMCPInstallDryRunChangesNothing(t *testing.T) {
	h, calls, home := mcpHarness(t)
	if code := h.run("mcp", "install", "--dry-run"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	out := h.out.String()
	for _, want := range []string{"Claude Code", "Codex CLI", "claude mcp add", "[mcp_servers.trove]", "Dry run"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(codexCfg(home)); !os.IsNotExist(err) {
		t.Error("dry run wrote a file")
	}
	for _, c := range *calls {
		if strings.Contains(c, " add ") {
			t.Errorf("dry run ran %q", c)
		}
	}
}

func TestMCPInstallNeedsConsent(t *testing.T) {
	h, calls, home := mcpHarness(t)
	// No terminal and no --yes: the question cannot be answered, so nothing changes.
	if code := h.run("mcp", "install"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if code := h.run("mcp", "install", "--json"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if _, err := os.Stat(codexCfg(home)); !os.IsNotExist(err) {
		t.Error("codex config written without consent")
	}
	for _, c := range *calls {
		if !strings.HasPrefix(c, "claude mcp get ") { // the read-only probe is fine
			t.Errorf("ran %q without consent", c)
		}
	}
}

func TestMCPInstallAndUninstall(t *testing.T) {
	h, calls, home := mcpHarness(t)
	dataDir := filepath.Join(home, ".trove")
	original := "model = \"gpt-5\"\n"
	if err := os.WriteFile(codexCfg(home), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if install.Registered(dataDir) {
		t.Fatal("nothing is registered yet")
	}
	if code := h.run("mcp", "install", "--yes"); code != 0 {
		t.Fatalf("exit %d: %s %s", code, h.out, h.err)
	}
	b, _ := os.ReadFile(codexCfg(home))
	if !strings.HasPrefix(string(b), original) || !strings.Contains(string(b), "[mcp_servers.trove]") || !strings.Contains(string(b), `args = ["mcp"]`) {
		t.Errorf("codex config:\n%s", b)
	}
	wantAdd := "claude mcp add --transport stdio --scope user trove -- " + filepath.Join(home, "bin", "trove") + " mcp"
	found := false
	for _, c := range *calls {
		found = found || c == wantAdd
	}
	if !found {
		t.Errorf("want %q in %v", wantAdd, *calls)
	}
	if !install.Registered(dataDir) {
		t.Error("install should be recorded")
	}

	// Installing again changes nothing and does not run `claude mcp add` twice.
	before := len(*calls)
	h.out.Reset()
	if code := h.run("mcp", "install", "--yes"); code != 0 || !strings.Contains(h.out.String(), "Already registered") {
		t.Fatalf("second install: %d %s", code, h.out)
	}
	for _, c := range (*calls)[before:] {
		if strings.Contains(c, " add ") {
			t.Errorf("second install ran %q", c)
		}
	}

	h.out.Reset()
	if code := h.run("mcp", "status", "--json"); code != 0 {
		t.Fatal(h.err)
	}
	var st mcpStatusReport
	if err := json.Unmarshal(h.out.Bytes(), &st); err != nil || !st.Registered || len(st.Where) != 2 {
		t.Fatalf("status: %+v %v\n%s", st, err, h.out)
	}

	if code := h.run("mcp", "uninstall"); code != 0 {
		t.Fatalf("uninstall exit %d: %s %s", code, h.out, h.err)
	}
	if b, _ := os.ReadFile(codexCfg(home)); string(b) != original {
		t.Errorf("codex config not restored:\n%s", b)
	}
	if (*calls)[len(*calls)-1] != "claude mcp remove trove --scope user" {
		t.Errorf("claude registration not removed: %v", *calls)
	}
	if install.Registered(dataDir) {
		t.Error("nothing should be registered after uninstall")
	}
}

func TestMCPInstallClaudeMissingFromPath(t *testing.T) {
	h, calls, home := mcpHarness(t, "codex") // ~/.claude exists but `claude` is not on PATH
	h.out.Reset()
	if code := h.run("mcp", "install", "--yes"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if !strings.Contains(h.out.String(), "`claude` command was not found") {
		t.Errorf("want an explanation:\n%s", h.out)
	}
	if b, _ := os.ReadFile(codexCfg(home)); !strings.Contains(string(b), "mcp_servers.trove") {
		t.Error("codex should still be registered")
	}
	for _, c := range *calls {
		if strings.Contains(c, "claude") {
			t.Errorf("ran %q", c)
		}
	}
}

func TestMCPInstallDoesNotTouchClaudeJSON(t *testing.T) {
	h, _, home := mcpHarness(t)
	cj := filepath.Join(home, ".claude.json")
	if err := os.WriteFile(cj, []byte(`{"oauthAccount":"secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := h.run("mcp", "install", "--yes"); code != 0 {
		t.Fatal(h.err)
	}
	if b, _ := os.ReadFile(cj); string(b) != `{"oauthAccount":"secret"}` {
		t.Error("~/.claude.json must never be edited by Trove")
	}
}

func TestMCPInstallRefusesExistingClaudeServer(t *testing.T) {
	h, calls, _ := mcpHarness(t)
	h.app.Run = func(name string, args ...string) error {
		*calls = append(*calls, name+" "+strings.Join(args, " "))
		return nil // `claude mcp get trove` succeeds: someone else's server
	}
	h.out.Reset()
	h.run("mcp", "install", "--yes", "--agent", "claude")
	if !strings.Contains(h.out.String(), "already has an MCP server named") {
		t.Errorf("%s", h.out)
	}
	for _, c := range *calls {
		if strings.Contains(c, " add ") {
			t.Errorf("must not overwrite: %q", c)
		}
	}
}

func TestMCPInstallProjectScope(t *testing.T) {
	h, calls, home := mcpHarness(t)
	proj := filepath.Join(home, "proj")
	_ = os.MkdirAll(proj, 0o755)
	if code := h.run("mcp", "install", "--scope", "project", "--project", proj, "--yes"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	b, _ := os.ReadFile(filepath.Join(proj, ".mcp.json"))
	if !strings.Contains(string(b), `"trove"`) {
		t.Errorf(".mcp.json:\n%s", b)
	}
	if _, err := os.Stat(codexCfg(home)); !os.IsNotExist(err) {
		t.Error("project scope must not edit Codex's user config")
	}
	if !strings.Contains(h.out.String(), "only reads project config for trusted projects") {
		t.Errorf("should explain why Codex was skipped:\n%s", h.out)
	}
	for _, c := range *calls {
		if strings.Contains(c, " add ") {
			t.Errorf("project scope should not run claude mcp add: %q", c)
		}
	}
	if code := h.run("mcp", "uninstall"); code != 0 {
		t.Fatal(h.err)
	}
	if _, err := os.Stat(filepath.Join(proj, ".mcp.json")); !os.IsNotExist(err) {
		t.Error("the .mcp.json Trove created should be removed")
	}
}

func TestMCPInstallBadFlags(t *testing.T) {
	h, _, _ := mcpHarness(t)
	for _, args := range [][]string{{"--scope", "global"}, {"--agent", "vim"}} {
		if code := h.run(append([]string{"mcp", "install"}, args...)...); code == 0 {
			t.Errorf("%v should fail", args)
		}
	}
}

func TestMCPUninstallWhenNothingInstalled(t *testing.T) {
	h, _, _ := mcpHarness(t)
	if code := h.run("mcp", "uninstall"); code != 0 || !strings.Contains(h.out.String(), "Nothing to remove") {
		t.Fatalf("%d %s %s", code, h.out, h.err)
	}
}
