package cli

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

var hostileStrings = []string{
	"/usr/local/bin/trove",
	"/opt/my tools/trove",
	`/tmp/a"b`,
	"/tmp/a'b",
	"/tmp/$(touch pwned)",
	"/tmp/`touch pwned`",
	"/tmp/a;touch pwned",
	"/tmp/a&&touch pwned",
	"/tmp/a|b",
	"/tmp/a\nb",
	`/tmp/a\b`,
	"/tmp/~user",
	"/tmp/$HOME/*",
	"",
}

func TestShellQuoteSafeStringsAreUnchanged(t *testing.T) {
	for _, s := range []string{"/usr/local/bin/trove", "/home/me/.trove", "a-b_c.d:e,f=g+h@i%j"} {
		if got := shellQuote(s); got != s {
			t.Errorf("shellQuote(%q) = %q, want it unchanged", s, got)
		}
	}
}

// The shell itself is the judge: whatever the input, it must come back as one
// argument with exactly the original bytes and run nothing else.
func TestShellQuoteSurvivesARealShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this machine")
	}
	dir := t.TempDir()
	for _, s := range hostileStrings {
		cmd := exec.Command(sh, "-c", `printf '%s' `+shellQuote(s))
		cmd.Dir = dir
		out, err := cmd.Output()
		if err != nil {
			t.Errorf("%q: %v", s, err)
			continue
		}
		if string(out) != s {
			t.Errorf("shell turned %q into %q", s, out)
		}
	}
	if entries, _ := exec.Command(sh, "-c", "ls -A").Output(); len(entries) != 0 {
		t.Log("(sanity: scratch dir listing is not used)")
	}
	cmd := exec.Command(sh, "-c", "ls -A")
	cmd.Dir = dir
	if out, _ := cmd.Output(); strings.Contains(string(out), "pwned") {
		t.Fatal("a quoted string ran a command")
	}
}

func TestHookCommandsQuoteHostilePaths(t *testing.T) {
	h := newHarness(t, nil)
	h.app.Executable = func() (string, error) { return `/opt/a b/$(id)/trove`, nil }
	_, build, err := hookCommands(h.app, `/data/it's`, "")
	if err != nil {
		t.Fatal(err)
	}
	got := build("pre_read")
	if !strings.HasPrefix(got, `'/opt/a b/$(id)/trove' hook pre_read`) {
		t.Errorf("binary path not quoted: %s", got)
	}
}

func TestStatusLineCommandQuotesDataDir(t *testing.T) {
	h := newHarness(t, map[string]string{"TROVE_DATA_DIR": "/d"})
	h.app.Executable = func() (string, error) { return "/bin/trove", nil }
	got, err := statusLineCommand(h.app, "/data/`x`")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "--data-dir '/data/`x`'") {
		t.Errorf("data dir not quoted: %s", got)
	}
}

func TestNoBareTroveWhenPathUnknown(t *testing.T) {
	for name, exe := range map[string]func() (string, error){
		"error":    func() (string, error) { return "", errors.New("boom") },
		"empty":    func() (string, error) { return "", nil },
		"relative": func() (string, error) { return "trove", nil },
	} {
		h := newHarness(t, nil)
		h.app.Executable = exe
		if _, _, err := hookCommands(h.app, "/d", ""); err == nil || !strings.Contains(err.Error(), "PATH") {
			t.Errorf("%s: hooks should refuse, got %v", name, err)
		}
		if _, err := statusLineCommand(h.app, "/d"); err == nil {
			t.Errorf("%s: status line should refuse", name)
		}
	}
}
