package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/plugin"
)

const repoRoot = "../.."

func TestPluginDerivedFilesMatchTheGenerator(t *testing.T) {
	files, err := plugin.Render(claudeSpecs(plugin.HookCommand, true, true))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		path := filepath.Join(repoRoot, filepath.FromSlash(f.Path))
		if *update {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, f.Content, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s is missing; run `go test ./internal/cli -run TestPluginDerivedFiles -update`", f.Path)
		}
		if !bytes.Equal(bytes.ReplaceAll(got, []byte("\r\n"), []byte("\n")), f.Content) {
			t.Errorf("%s differs from what the generator renders; run `go test ./internal/cli -run TestPluginDerivedFiles -update` and review the diff", f.Path)
		}
	}
}

func TestPluginHooksMatchTheSettingsInstaller(t *testing.T) {
	b, err := plugin.HooksJSON(claudeSpecs(plugin.HookCommand, true, true))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct{ Type, Command string }
		} `json:"hooks"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for _, s := range claudeSpecs(plugin.HookCommand, true, true) {
		want[s.Event+"|"+s.Matcher+"|"+s.Command] = true
	}
	got := map[string]bool{}
	for ev, groups := range doc.Hooks {
		for _, g := range groups {
			for _, h := range g.Hooks {
				if h.Type != "command" || !strings.HasPrefix(h.Command, `sh "${CLAUDE_PLUGIN_ROOT}/bin/trove" `) {
					t.Errorf("%s: %q is not a quoted wrapper command", ev, h.Command)
				}
				got[ev+"|"+g.Matcher+"|"+h.Command] = true
			}
		}
	}
	for k := range want {
		if !got[k] {
			t.Errorf("plugin hooks lack %s", k)
		}
	}
	start := "SessionStart||" + plugin.StartCommand()
	if !got[start] {
		t.Errorf("SessionStart lacks %s", plugin.StartCommand())
	}
	delete(got, start)
	if len(got) != len(want) {
		t.Errorf("plugin registers %d handlers besides the first-run command, the settings installer %d", len(got), len(want))
	}
}

// Rule 13: no provider or product brand in any name Trove creates. Descriptions
// and file contents may say "works with Claude Code" in plain text.
func TestPluginNamesCarryNoBrand(t *testing.T) {
	brands := []string{"claude", "anthropic", "codex", "openai"}
	bad := func(what, s string) {
		low := strings.ToLower(s)
		for _, b := range brands {
			if strings.Contains(low, b) {
				t.Errorf("%s %q carries the brand %q", what, s, b)
			}
		}
	}
	for _, root := range []string{filepath.Join(repoRoot, "plugin"), filepath.Join(repoRoot, ".claude-plugin")} {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			// ".claude-plugin" is the directory name Claude Code requires for the manifest.
			if name := d.Name(); name != ".claude-plugin" {
				bad("file or folder", name)
			}
			if d.IsDir() || !strings.HasSuffix(p, ".md") {
				return nil
			}
			b, _ := os.ReadFile(p) //nolint:gosec // repository file
			for _, line := range strings.Split(string(b), "\n") {
				if v, ok := strings.CutPrefix(line, "name:"); ok {
					bad("frontmatter name", strings.TrimSpace(v))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"plugin/trove/.claude-plugin/plugin.json", ".claude-plugin/marketplace.json"} {
		b, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(f)))
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Name    string `json:"name"`
			Plugins []struct{ Name string }
		}
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatal(err)
		}
		bad(f+" name", doc.Name)
		for _, p := range doc.Plugins {
			bad(f+" plugin name", p.Name)
		}
		if doc.Name != "trove" {
			t.Errorf("%s name = %q, want trove", f, doc.Name)
		}
	}
}

func TestPluginCommandsDoNotRunOnTheirOwn(t *testing.T) {
	// A model-invocable command puts its description in every session's context
	// (rule 15). The plugin's commands are for the user to type.
	matches, _ := filepath.Glob(filepath.Join(repoRoot, "plugin", "trove", "commands", "*.md"))
	if len(matches) == 0 {
		t.Fatal("no commands found")
	}
	for _, m := range matches {
		b, _ := os.ReadFile(m) //nolint:gosec // repository file
		if !strings.Contains(string(b), "\ndisable-model-invocation: true\n") {
			t.Errorf("%s must set disable-model-invocation: true", m)
		}
	}
}

func TestPluginStartFirstRunReportsOnceAndEnablesNothing(t *testing.T) {
	h := newHarness(t, nil)
	dir := h.app.Getenv("TROVE_DATA_DIR")
	home, _ := h.app.HomeDir()
	if code := h.run("plugin", "start"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	var out hookOutput
	if err := json.Unmarshal(h.out.Bytes(), &out); err != nil {
		t.Fatalf("stdout must be exactly one JSON object: %v\n%s", err, h.out)
	}
	for _, want := range []string{"Nothing is turned on", "measured or estimated", "/trove:setup"} {
		if !strings.Contains(out.SystemMessage, want) {
			t.Errorf("first-run message lacks %q:\n%s", want, out.SystemMessage)
		}
	}
	for _, bad := range []string{"trove setup", "trove gateway"} {
		if strings.Contains(out.SystemMessage, bad) {
			t.Errorf("a plugin user (possibly on a subscription) is told to use the gateway (%q):\n%s", bad, out.SystemMessage)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, pluginStateFile)); err != nil {
		t.Errorf("state not recorded: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.toml")); err == nil {
		t.Error("the first run wrote a config file; it must not enable or configure anything")
	}
	if _, err := os.Stat(filepath.Join(home, ".claude")); err == nil {
		t.Error("the first run touched the agent's folder")
	}

	h.out.Reset()
	if code := h.run("plugin", "start"); code != 0 || h.out.Len() != 0 {
		t.Errorf("second run: exit %d, stdout %q; want silence", code, h.out)
	}
}

func TestPluginStartFailsOpen(t *testing.T) {
	// A data folder that cannot be used (a file is in the way) must not break the session.
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, map[string]string{"TROVE_DATA_DIR": filepath.Join(blocked, "sub")})
	if code := h.run("plugin", "start"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if h.out.Len() != 0 && !json.Valid(h.out.Bytes()) {
		t.Errorf("stdout is not JSON: %s", h.out)
	}
}

func TestHooksInstallRefusesWhenThePluginIsEnabled(t *testing.T) {
	h, settings := setupHarness(t, "")
	body := "{\n  \"enabledPlugins\": {\n    \"trove@trove\": true\n  }\n}\n"
	if err := os.WriteFile(settings, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := h.run("hooks", "install", "--agent", "claude", "--yes"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if got, _ := os.ReadFile(settings); string(got) != body {
		t.Errorf("settings changed although the plugin already registers the hooks:\n%s", got)
	}
	if !strings.Contains(h.out.String(), "plugin is enabled") {
		t.Errorf("no explanation:\n%s", h.out)
	}
}

func TestPluginStatusLineNeedsConsentAndNeverTouchesTheGateway(t *testing.T) {
	h, settings := setupHarness(t, "")
	if code := h.run("plugin", "status-line"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if got, _ := os.ReadFile(settings); string(got) != claudeSettings {
		t.Fatalf("changed without consent:\n%s", got)
	}
	if !strings.Contains(h.out.String(), "trove plugin status-line --yes") || strings.Contains(h.out.String(), "trove setup") {
		t.Errorf("the retry hint must name the plugin command:\n%s", h.out)
	}
	h.out.Reset()
	if code := h.run("plugin", "status-line", "--yes"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	got, _ := os.ReadFile(settings)
	if !strings.Contains(string(got), "statusLine") || strings.Contains(string(got), "ANTHROPIC_BASE_URL") {
		t.Errorf("want only a status line:\n%s", got)
	}
	h2 := newHarness(t, map[string]string{"TROVE_DATA_DIR": h.app.Getenv("TROVE_DATA_DIR")}, "claude")
	if code := h2.run("uninstall", "--yes"); code != 0 {
		t.Fatalf("uninstall exit %d: %s%s", code, h2.out, h2.err)
	}
	if got, _ := os.ReadFile(settings); string(got) != claudeSettings {
		t.Errorf("uninstall did not restore the user's settings byte for byte:\n%s", got)
	}
}

// --- the wrapper script -----------------------------------------------------

func shPath(t *testing.T) string {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this machine")
	}
	return sh
}

// wrapperEnv copies the wrapper next to a VERSION file in a temp plugin folder.
func wrapperEnv(t *testing.T, version string) (wrapper, data string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join(repoRoot, "plugin", "trove", "bin", "trove"))
	if err != nil {
		t.Fatal(err)
	}
	src = bytes.ReplaceAll(src, []byte("\r\n"), []byte("\n"))
	wrapper = filepath.Join(root, "bin", "trove")
	if err := os.WriteFile(wrapper, src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "VERSION"), []byte(version+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return wrapper, t.TempDir()
}

func runWrapper(t *testing.T, wrapper string, env []string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(shPath(t), append([]string{filepath.ToSlash(wrapper)}, args...)...) //nolint:gosec // test
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}, env...)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), code
}

func TestWrapperHookNeverFailsAndNeverDownloads(t *testing.T) {
	wrapper, data := wrapperEnv(t, "9.9.9")
	// No trove anywhere, version pinned: a hook must exit 0 quietly and fetch nothing.
	out, code := runWrapper(t, wrapper, []string{"CLAUDE_PLUGIN_DATA=" + filepath.ToSlash(data), "TROVE_BASE_URL=file:///nonexistent"}, "hook", "session_reset")
	if code != 0 || out != "" {
		t.Errorf("hook: exit %d, output %q; want 0 and silence", code, out)
	}
	if entries, _ := os.ReadDir(data); len(entries) != 0 {
		t.Errorf("a hook created files in the data folder: %v", entries)
	}
}

func TestWrapperDownloadVerifiesTheChecksum(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl")
	}
	// The wrapper's TROVE_OS and TROVE_ARCH overrides (as in install.sh) make the
	// archive name the same on every machine running the test.
	const osName, arch = "linux", "amd64"
	build := func(t *testing.T, checksum string) (base string) {
		t.Helper()
		rel := t.TempDir()
		var tgz bytes.Buffer
		gz := gzip.NewWriter(&tgz)
		tw := tar.NewWriter(gz)
		body := []byte("#!/bin/sh\necho fake-trove \"$@\"\n")
		_ = tw.WriteHeader(&tar.Header{Name: "trove", Mode: 0o755, Size: int64(len(body))})
		_, _ = tw.Write(body)
		_ = tw.Close()
		_ = gz.Close()
		name := "trove_9.9.9_" + osName + "_" + arch + ".tar.gz"
		dir := filepath.Join(rel, "v9.9.9")
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(dir, name), tgz.Bytes(), 0o644)
		sum := sha256.Sum256(tgz.Bytes())
		if checksum == "" {
			checksum = hex.EncodeToString(sum[:])
		}
		_ = os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(checksum+"  "+name+"\n"), 0o644)
		// file:///C:/... on Windows, file:///tmp/... elsewhere.
		return "file:///" + strings.TrimPrefix(filepath.ToSlash(rel), "/")
	}
	wrapper, data := wrapperEnv(t, "9.9.9")
	env := func(base string) []string {
		return []string{"CLAUDE_PLUGIN_DATA=" + filepath.ToSlash(data), "TROVE_BASE_URL=" + base, "TROVE_OS=" + osName, "TROVE_ARCH=" + arch}
	}

	out, code := runWrapper(t, wrapper, env(build(t, strings.Repeat("0", 64))), "plugin", "start")
	if code != 0 || !strings.Contains(out, "checksum mismatch") {
		t.Errorf("bad checksum: exit %d, output %q; want a refusal that fails open", code, out)
	}
	if _, err := os.Stat(filepath.Join(data, "trove-9.9.9", "trove")); err == nil {
		t.Fatal("installed a download whose checksum did not match")
	}

	out, code = runWrapper(t, wrapper, env(build(t, "")), "version")
	if code != 0 || !strings.Contains(out, "fake-trove version") {
		t.Errorf("good checksum: exit %d, output %q", code, out)
	}
	// The second call uses the copy it kept, with no network.
	out, code = runWrapper(t, wrapper, env("file:///nonexistent"), "version")
	if code != 0 || !strings.Contains(out, "fake-trove version") {
		t.Errorf("cached copy: exit %d, output %q", code, out)
	}
}
