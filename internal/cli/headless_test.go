package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func headlessGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	got = strings.ReplaceAll(got, "\r\n", "\n")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if strings.ReplaceAll(string(want), "\r\n", "\n") != got {
		t.Errorf("%s differs from the golden file; run with -update and review the diff:\n%s", name, got)
	}
}

func TestHeadlessSnippetsMatchTheGoldenFiles(t *testing.T) {
	headlessGolden(t, "headless_env.golden", HeadlessEnvSnippet("http://127.0.0.1:8787/anthropic"))
	headlessGolden(t, "headless_ci.golden", HeadlessCIExample(8787))
}

// The snippets carry what keeps the plan login out of the gateway and the job's key out of the text.
func TestHeadlessSnippetsAreSafe(t *testing.T) {
	env, ci := HeadlessEnvSnippet("http://127.0.0.1:8787/anthropic"), HeadlessCIExample(8787)
	for name, s := range map[string]string{"env": env, "ci": ci} {
		if !strings.Contains(s, "claude --bare -p") {
			t.Errorf("%s: jobs must run with --bare, which never reads the login or keychain", name)
		}
		for _, bad := range []string{"sk-ant", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_AUTH_TOKEN", "claude-code-action", "export ANTHROPIC"} {
			if strings.Contains(s, bad) {
				t.Errorf("%s contains %q", name, bad)
			}
		}
	}
	if !strings.Contains(env, `ANTHROPIC_BASE_URL="http://127.0.0.1:8787/anthropic" \`) || !strings.Contains(env, "${HEADLESS_API_KEY:?") {
		t.Errorf("env snippet: base URL and key variable must be set for the job's command only:\n%s", env)
	}
	for _, want := range []string{"ANTHROPIC_BASE_URL: http://127.0.0.1:8787/anthropic", "ANTHROPIC_API_KEY: ${{ secrets.HEADLESS_API_KEY }}",
		"curl -fsS http://127.0.0.1:8787/trove/health", "not the Claude Code GitHub Action"} {
		if !strings.Contains(ci, want) {
			t.Errorf("CI example lacks %q", want)
		}
	}
}

// The function must be valid shell; the CI run blocks too.
func TestHeadlessSnippetsParseAsShell(t *testing.T) {
	sh, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	check := func(name, script string) {
		t.Helper()
		cmd := exec.Command(sh, "-n")
		cmd.Stdin = strings.NewReader(script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s is not valid shell: %v\n%s\n%s", name, err, out, script)
		}
	}
	check("env snippet", HeadlessEnvSnippet("http://127.0.0.1:8787/anthropic"))
	ci := HeadlessCIExample(8787)
	var block []string
	in := false
	for _, l := range strings.Split(ci, "\n") {
		switch {
		case strings.HasSuffix(l, "run: |"):
			in, block = true, nil
		case in && strings.HasPrefix(l, "          "):
			block = append(block, strings.TrimPrefix(l, "          "))
		case in:
			check("CI run block", strings.Join(block, "\n"))
			in = false
		}
	}
}

func TestHeadlessCommand(t *testing.T) {
	h := newHarness(t, nil)
	if code := h.run("headless", "env", "--port", "9100"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	out := h.out.String()
	if !strings.Contains(out, "http://127.0.0.1:9100/anthropic") || !strings.Contains(out, "monthly API credits") {
		t.Fatalf("output:\n%s", out)
	}
	if strings.Contains(out, "FAKEHARNESSKEY") {
		t.Fatal("the command printed the API key from the environment")
	}

	h = newHarness(t, nil)
	if code := h.run("headless", "ci", "--json"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	var v headlessOutput
	if err := json.Unmarshal(h.out.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Tradeoff) != 2 || v.CI == "" || v.Env != "" || v.KeyVar != HeadlessKeyVar || len(v.Sources) == 0 ||
		v.BaseURL != "http://127.0.0.1:8787/anthropic" {
		t.Fatalf("json: %+v", v)
	}

	h = newHarness(t, nil)
	if code := h.run("headless", "--port", "0x"); code == 0 {
		t.Fatal("a bad port must be refused")
	}
}
