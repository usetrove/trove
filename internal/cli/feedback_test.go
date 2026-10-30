package cli

import (
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestFeedbackShowsBodyAndOpensNothingWithoutConsent(t *testing.T) {
	h := newHarness(t, nil, "claude")
	dir := h.app.Getenv("TROVE_DATA_DIR")
	secret := "/Users/someone/secret-project"
	seedRequests(t, dir, []any{time.Now().UnixMilli(), "anthropic", "claude-a", 200, 1.0})
	opened := 0
	h.app.Run = func(string, ...string) error { opened++; return nil }

	// No terminal input: confirm answers no.
	if code := h.run("feedback", "--note", "slow start"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	out := h.out.String()
	for _, want := range []string{"slow start", "agents detected: Claude Code", "recorded requests: 1", "https://github.com/", "Nothing was opened"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if opened != 0 {
		t.Error("opened a browser without consent")
	}
	if strings.Contains(out, secret) || strings.Contains(out, dir) || strings.Contains(out, "sk-") {
		t.Error("output contains a path or key")
	}

	// Saying yes opens exactly the link shown.
	h2 := newHarness(t, nil)
	h2.app.In = strings.NewReader("y\n")
	var gotArgs []string
	h2.app.Run = func(name string, args ...string) error { gotArgs = append([]string{name}, args...); return nil }
	if code := h2.run("feedback"); code != 0 {
		t.Fatalf("exit %d: %s", code, h2.err)
	}
	if len(gotArgs) == 0 || !strings.HasPrefix(gotArgs[len(gotArgs)-1], "https://github.com/") {
		t.Errorf("did not open the link: %v", gotArgs)
	}
	_ = runtime.GOOS
}

func TestFeedbackJSONAndNoOpenNeverOpen(t *testing.T) {
	h := newHarness(t, nil)
	h.app.In = strings.NewReader("y\n")
	h.app.Run = func(string, ...string) error { t.Error("opened a browser"); return nil }
	if code := h.run("feedback", "--json"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	var o map[string]any
	if err := json.Unmarshal(h.out.Bytes(), &o); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"diagnostics", "issue_body", "url"} {
		if _, ok := o[k]; !ok {
			t.Errorf("json lacks %q", k)
		}
	}
	if _, err := os.Stat(h.app.Getenv("TROVE_DATA_DIR")); err == nil {
		t.Error("feedback created the data directory")
	}

	h2 := newHarness(t, nil)
	h2.app.In = strings.NewReader("y\n")
	h2.app.Run = func(string, ...string) error { t.Error("opened a browser"); return nil }
	if code := h2.run("feedback", "--no-open"); code != 0 {
		t.Fatalf("exit %d", code)
	}
}
