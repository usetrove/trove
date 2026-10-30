package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// snapshot reads every file under the agent folders so the test can prove they
// come back byte for byte.
func snapshot(t *testing.T, home string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, d := range []string{".claude", ".codex"} {
		_ = filepath.WalkDir(filepath.Join(home, d), func(p string, e os.DirEntry, err error) error {
			if err != nil || e.IsDir() {
				return nil
			}
			b, _ := os.ReadFile(p)
			out[p] = string(b)
			return nil
		})
	}
	return out
}

func installEverything(t *testing.T) (h *harness, home string, calls *[]string, before map[string]string) {
	t.Helper()
	h, calls, home = mcpHarness(t)
	claude, codex := agentHome(t, h)
	// Something of the user's own in both, so a restore has to keep it.
	if err := os.WriteFile(claude, []byte("{\n  \"model\": \"opus\",\n  \"theme\": \"dark\"\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(codex, []byte("model = \"gpt-5\"\n[mcp_servers.other]\ncommand = \"npx\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before = snapshot(t, home)
	for _, args := range [][]string{
		{"setup", "--yes", "--status-line"},
		{"hooks", "install", "--yes"},
		{"mcp", "install", "--yes"},
	} {
		if code := h.run(args...); code != 0 {
			t.Fatalf("%v: exit %d: %s", args, code, h.err)
		}
	}
	if after := snapshot(t, home); after[claude] == before[claude] || after[codex] == before[codex] {
		t.Fatal("install changed nothing, so the test proves nothing")
	}
	return h, home, calls, before
}

func TestUninstallRestoresEveryAgentFileAndPurgesData(t *testing.T) {
	h, home, calls, before := installEverything(t)
	dir, _ := h.app.ResolveDataDir()
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("no data dir to purge")
	}
	*calls = nil

	if code := h.run("uninstall", "--yes", "--purge-data"); code != 0 {
		t.Fatalf("exit %d\nout: %s\nerr: %s", code, h.out, h.err)
	}
	after := snapshot(t, home)
	for p, want := range before {
		if after[p] != want {
			t.Errorf("%s not restored.\nwant: %q\ngot:  %q", p, want, after[p])
		}
	}
	for p := range after {
		if _, ok := before[p]; !ok {
			t.Errorf("uninstall left behind %s", p)
		}
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		entries, _ := os.ReadDir(dir)
		t.Errorf("data dir still exists: %v", entries)
	}
	removed := false
	for _, c := range *calls {
		if strings.Contains(c, "mcp remove") {
			removed = true
		}
	}
	if !removed {
		t.Errorf("did not ask Claude Code to remove the MCP server; ran %v", *calls)
	}
}

func TestUninstallKeepsDataByDefault(t *testing.T) {
	h, home, _, before := installEverything(t)
	dir, _ := h.app.ResolveDataDir()
	if code := h.run("uninstall", "--yes"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	for p, want := range snapshot(t, home) {
		if before[p] != want {
			t.Errorf("%s not restored", p)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "config.toml")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Error("data dir was deleted without --purge-data")
	}
}

func TestUninstallWithoutYesOrTerminalChangesNothing(t *testing.T) {
	h, home, _, _ := installEverything(t)
	installed := snapshot(t, home)
	if code := h.run("uninstall", "--purge-data"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	for p, want := range snapshot(t, home) {
		if installed[p] != want {
			t.Errorf("%s changed without consent", p)
		}
	}
	if !strings.Contains(h.out.String(), "Nothing was changed") {
		t.Errorf("did not say nothing changed:\n%s", h.out)
	}
}

// The data folder name is shared with another program. Anything Trove did not
// create must survive, and so must a database that is not Trove's.
func TestPurgeLeavesForeignFilesAndForeignDatabase(t *testing.T) {
	h := newHarness(t, nil)
	dir, _ := h.app.ResolveDataDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"trove.db":       "SQLite format 3\x00 somebody else's data",
		"v2.db":          "other",
		"supervisor.pid": "123",
		"config.toml":    "[gateway]\nport = 8787\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if code := h.run("uninstall", "--yes", "--purge-data"); code != 0 {
		t.Fatalf("exit %d\nout: %s\nerr: %s", code, h.out, h.err)
	}
	for _, name := range []string{"trove.db", "v2.db", "supervisor.pid"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s was deleted but is not Trove's: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "config.toml")); !os.IsNotExist(err) {
		t.Error("Trove's own config.toml should have been deleted")
	}
	if !strings.Contains(h.out.String(), "v2.db") {
		t.Errorf("report should name what it left alone:\n%s", h.out)
	}
}

// If any step cannot be completed (here: the agent's own command to remove the
// MCP server fails), the records and backups are still needed, so uninstall
// must report the problem and must not delete data.
func TestPurgeIsRefusedWhenAStepFails(t *testing.T) {
	h, _, _, _ := installEverything(t)
	dir, _ := h.app.ResolveDataDir()
	h.app.Run = func(name string, args ...string) error {
		if len(args) > 1 && args[1] == "remove" {
			return errors.New("claude: command failed")
		}
		return nil
	}
	h.out.Reset()
	if code := h.run("uninstall", "--yes", "--purge-data"); code == 0 {
		t.Fatal("exit 0 although a step failed")
	}
	if !strings.Contains(h.out.String(), "NOT deleted") {
		t.Errorf("did not say data was kept:\n%s", h.out)
	}
	for _, name := range []string{"backups", "mcp_installs.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s was deleted although a step failed: %v", name, err)
		}
	}
}

func TestUninstallJSONAndNothingToDo(t *testing.T) {
	h := newHarness(t, nil)
	if code := h.run("uninstall", "--yes", "--json"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if !strings.Contains(h.out.String(), `"plan"`) {
		t.Errorf("not JSON: %s", h.out)
	}
}
