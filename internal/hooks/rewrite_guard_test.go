package hooks

import (
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/config"
)

func TestARerunOfAQuietedCommandIsCountedOnceAndNotQuietedAgain(t *testing.T) {
	r := newRig(t, map[string]string{rwQuiet: "on"})
	r.allow(Capability{Tool: "Bash", InputRewrite: CapYes})
	r.start()

	if out, _ := r.bash("pip install -r requirements.txt"); !strings.Contains(out, "updatedInput") {
		t.Fatalf("first run should be quieted: %q", out)
	}
	// The agent asks for the same command again: it did not get what it needed.
	// It must not be quieted a second time, or the agent would never get its output.
	for i := 0; i < 3; i++ {
		if out, _ := r.bash("pip install -r requirements.txt"); out != "" {
			t.Fatalf("run %d must go through unchanged: %q", i+2, out)
		}
	}
	got := strings.Join(r.kinds(), ",")
	if got != rwQuiet+":rewritten,"+rwQuiet+":reissued" {
		t.Fatalf("events = %s; want one rewrite and one reissue", got)
	}
	// A different command is judged on its own.
	if out, _ := r.bash("pip install other"); !strings.Contains(out, "updatedInput") {
		t.Fatalf("a new command should be quieted: %q", out)
	}
}

func (r *rig) fetch(id string) {
	r.t.Helper()
	if out, code := r.run(EventPostFetch, r.input("claude_post_fetch.json", "{{TOOL}}", fetchTool, "{{ID}}", id)); out != "" || code != 0 {
		r.t.Fatalf("fetch hook: out=%q code=%d", out, code)
	}
}

func TestFetchBackIsCountedOnlyForOutputTheHookTrimmed(t *testing.T) {
	r := trimRig(t, true)
	full, _ := goTestOutput(400)
	out, _ := r.post("claude_post_bash.json", full, false)
	short := parseOut(t, out)
	m := markerRe.FindStringSubmatch(string(short.H.Output))
	if m == nil {
		t.Fatalf("no marker in %s", short.H.Output)
	}
	id := m[2]

	r.fetch("0123456789abcdef") // an output id Trove never trimmed (for example from trove_run)
	if strings.Contains(strings.Join(r.kinds(), ","), "fetched_back") {
		t.Fatalf("a fetch of someone else's output must not count against the trimmer: %v", r.kinds())
	}
	r.fetch(id)
	r.fetch(id) // fetching it again (paging) is the same fetch-back
	n := 0
	for _, e := range r.events() {
		if e.Kind == KindFetchedBack {
			n++
			if e.Hook != config.OptimizerOutputTrimmer {
				t.Fatalf("attributed to %s", e.Hook)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d fetched_back events; want 1 per trimmed output", n)
	}
}

func TestTheFetchHookIgnoresOtherToolsAndBadInput(t *testing.T) {
	r := trimRig(t, true)
	r.run(EventPostFetch, r.input("claude_post_fetch.json", "{{TOOL}}", "mcp__other__tool", "{{ID}}", "0123456789abcdef"))
	r.run(EventPostFetch, `{"session_id":"x","tool_name":"`+fetchTool+`","tool_input":"nope"}`)
	r.run(EventPostFetch, "garbage")
	for _, k := range r.kinds() {
		if strings.Contains(k, "fetched_back") {
			t.Fatalf("%v", r.kinds())
		}
	}
}
