package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/store"
)

type harness struct {
	app      *App
	out, err *bytes.Buffer
}

func newHarness(t *testing.T, env map[string]string, binaries ...string) *harness {
	t.Helper()
	h := &harness{out: &bytes.Buffer{}, err: &bytes.Buffer{}}
	home := t.TempDir()
	// Never let a test resolve the real ~/.trove: point the data directory into
	// the temporary home unless the test chose one.
	// A fake API key is present by default because `trove setup` refuses to point Claude Code at the gateway
	// without evidence of one (see setup_apikey_test.go); tests that need it absent pass "" for it.
	// The Anthropic default is a fake in the accepted Console format, so the key checks pass by default.
	all := map[string]string{"TROVE_DATA_DIR": filepath.Join(home, ".trove"), "ANTHROPIC_API_KEY": "sk-ant-api03-FAKEHARNESSKEYFORTESTS0123456789abcdefgh", "OPENAI_API_KEY": "test-value-not-a-key"}
	for k, v := range env {
		all[k] = v
	}
	h.app = &App{
		Out: h.out, Err: h.err,
		Getenv: func(k string) string { return all[k] },
		LookPath: func(name string) (string, error) {
			for _, b := range binaries {
				if b == name {
					return "/usr/bin/" + name, nil
				}
			}
			return "", errors.New("not found")
		},
		HomeDir: func() (string, error) { return home, nil },
	}
	return h
}

func (h *harness) run(args ...string) int { return Execute(h.app, args) }

// `trove --version` is the documented spelling. The old `trove version` is a
// hidden alias for one release, so both must print the same thing.
func TestVersion(t *testing.T) {
	for _, spelling := range [][]string{{"--version"}, {"version"}} {
		h := newHarness(t, nil)
		if code := h.run(spelling...); code != 0 {
			t.Fatalf("%v: exit %d: %s", spelling, code, h.err)
		}
		if first, _, _ := strings.Cut(h.out.String(), "\n"); first != "trove dev" {
			t.Errorf("%v: first line %q; the plugin launcher compares it to \"trove <version>\"", spelling, first)
		}
		for _, want := range []string{"commit: none", "built: unknown", "pricing data: " + pricing.DataVersion} {
			if !strings.Contains(h.out.String(), want) {
				t.Errorf("%v: missing %q in %q", spelling, want, h.out)
			}
		}
		h = newHarness(t, nil)
		h.run(append(spelling, "--json")...)
		var v map[string]string
		if err := json.Unmarshal(h.out.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"version", "commit", "build_date", "pricing_data_version"} {
			if _, ok := v[k]; !ok {
				t.Errorf("%v: json lacks %q", spelling, k)
			}
		}
	}
}

func TestVersionIsOnlyAFlagOnTheRoot(t *testing.T) {
	h := newHarness(t, nil)
	if code := h.run("report", "--version"); code != 1 || !strings.HasPrefix(h.err.String(), "Error: ") {
		t.Errorf("report --version: exit %d, stderr %q; want an unknown-flag error", code, h.err)
	}
	h = newHarness(t, nil)
	h.run("--help")
	if strings.Contains(h.out.String(), "Same as `trove --version`") {
		t.Errorf("the hidden `version` command shows in --help:\n%s", h.out)
	}
	if !strings.Contains(h.out.String(), "--version") {
		t.Errorf("--help does not list --version:\n%s", h.out)
	}
}

// `trove config` was removed until it is built for real. It must be an unknown
// command, not a stub that pretends to work.
func TestConfigCommandIsNotRegistered(t *testing.T) {
	for _, c := range [][]string{{"config"}, {"config", "get"}, {"config", "set", "gateway.port", "9000"}} {
		h := newHarness(t, nil)
		if code := h.run(c...); code != 1 || !strings.HasPrefix(h.err.String(), "Error: ") {
			t.Errorf("%v: exit %d, stderr %q; want an unknown-command error", c, code, h.err)
		}
	}
}

func TestUnknownCommandAndFlagFail(t *testing.T) {
	for _, args := range [][]string{{"bogus"}, {"version", "--nope"}, {"config", "get"}} {
		h := newHarness(t, nil)
		if code := h.run(args...); code != 1 || !strings.HasPrefix(h.err.String(), "Error: ") {
			t.Errorf("%v: exit %d, stderr %q", args, code, h.err)
		}
	}
	h := newHarness(t, nil)
	h.run("bogus", "--json")
	var v map[string]string
	if err := json.Unmarshal(h.err.Bytes(), &v); err != nil || v["error"] == "" {
		t.Errorf("json error output: %q", h.err)
	}
}

func decodeDoctor(t *testing.T, h *harness) doctorReport {
	t.Helper()
	var r doctorReport
	if err := json.Unmarshal(h.out.Bytes(), &r); err != nil {
		t.Fatalf("bad json %q: %v", h.out, err)
	}
	return r
}

func find(r doctorReport, name string) check {
	for _, c := range r.Checks {
		if c.Name == name {
			return c
		}
	}
	return check{}
}

func TestDoctorFreshDirWarnsButPasses(t *testing.T) {
	h := newHarness(t, nil)
	dir := filepath.Join(t.TempDir(), "nope")
	if code := h.run("doctor", "--json", "--data-dir", dir); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	r := decodeDoctor(t, h)
	if !r.OK || find(r, "data directory").Status != statusInfo || find(r, "database").Status != statusInfo {
		t.Errorf("report %+v", r)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Error("doctor must not create the data directory")
	}
}

func TestDoctorHealthyStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "d")
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	h := newHarness(t, nil)
	if code := h.run("doctor", "--json", "--data-dir", dir); code != 0 {
		t.Fatalf("exit %d: %s %s", code, h.out, h.err)
	}
	r := decodeDoctor(t, h)
	if c := find(r, "database"); c.Status != statusOK || !strings.Contains(c.Detail, "current") {
		t.Errorf("database check %+v", c)
	}
	if find(r, "data directory").Status != statusOK || find(r, "config").Status != statusOK {
		t.Errorf("report %+v", r)
	}
}

func TestDoctorBadConfigFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("retention_days = 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, nil)
	if code := h.run("doctor", "--data-dir", dir); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if !strings.Contains(h.out.String(), "retention_days is 0") || !strings.Contains(h.out.String(), "FAIL") {
		t.Errorf("output %q", h.out)
	}
}

func TestDoctorNeverEchoesSecrets(t *testing.T) {
	const key = "sk-ant-api03-FAKEFAKEFAKE1234567890abcdef"
	h := newHarness(t, map[string]string{"TROVE_UPSTREAMS_OPENAI": "ftp://" + key})
	if code := h.run("doctor", "--data-dir", t.TempDir()); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if all := h.out.String() + h.err.String(); strings.Contains(all, "FAKEFAKEFAKE") {
		t.Errorf("leaked key: %q", all)
	}
}

func TestDoctorDetectsAgentsWithoutChangingThem(t *testing.T) {
	h := newHarness(t, nil, "claude")
	home, _ := h.app.HomeDir()
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	h.run("doctor", "--json", "--data-dir", t.TempDir())
	r := decodeDoctor(t, h)
	if c := find(r, "agent: Claude Code"); !strings.Contains(c.Detail, "`claude` on PATH") || !strings.Contains(c.Detail, "settings folder") {
		t.Errorf("claude: %+v", c)
	}
	if c := find(r, "agent: Codex CLI"); c.Detail != "not detected" {
		t.Errorf("codex: %+v", c)
	}
	if entries, _ := os.ReadDir(claudeDir); len(entries) != 0 {
		t.Error("doctor modified the agent folder")
	}
}

func TestDoctorFlagsOpenPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on Windows")
	}
	dir := filepath.Join(t.TempDir(), "d")
	db, _ := store.Open(dir)
	_ = db.Close()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, nil)
	if code := h.run("doctor", "--data-dir", dir); code != 1 || !strings.Contains(h.out.String(), "chmod 0700") {
		t.Errorf("exit %d, out %q", code, h.out)
	}
}

func TestNoColorRespected(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		flag bool
	}{{"env", map[string]string{"NO_COLOR": "1"}, false}, {"flag", nil, true}} {
		h := newHarness(t, tc.env)
		h.app.NoColor = tc.flag
		if h.app.Color() {
			t.Errorf("%s: color should be off", tc.name)
		}
	}
	h := newHarness(t, nil) // bytes.Buffer is not a terminal
	h.run("doctor", "--data-dir", t.TempDir())
	if strings.Contains(h.out.String(), "\x1b[") {
		t.Error("ANSI codes written to a non-terminal")
	}
}

func TestResolveDataDirPrecedence(t *testing.T) {
	flagDir, envDir := t.TempDir(), t.TempDir()
	h := newHarness(t, map[string]string{"TROVE_DATA_DIR": envDir})
	if d, _ := h.app.ResolveDataDir(); d != envDir {
		t.Errorf("env: %q", d)
	}
	h.app.DataDir = flagDir
	if d, _ := h.app.ResolveDataDir(); d != flagDir {
		t.Errorf("flag: %q", d)
	}
}
