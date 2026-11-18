package hooks

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/recovery"
)

const rwDedup = config.OptimizerHookOutputDedup

var dedupRe = regexp.MustCompile(`\[Trove: identical to an output earlier in this session \(prompt #\d+, [^)]*\); full output sha256:([0-9a-f]{16})\]`)

func dedupRig(t *testing.T, state string, troveRun bool) *rig {
	t.Helper()
	noClock(t)
	r := newRig(t, nil)
	r.snapshot(Snapshot{States: map[string]string{rwDedup: state}, TroveRun: troveRun})
	r.allow(Capability{Tool: "Bash", OutputRewrite: CapYes})
	r.start()
	return r
}

func listing() string { return strings.Repeat("drwxr-xr-x  2 u u 4096 Nov 17 10:00 somedir\n", 30) }

func TestARepeatedOutputIsCollapsedAndItsFetchBackIsCounted(t *testing.T) {
	r := dedupRig(t, "on", true)
	full := listing()
	if out, _ := r.post("claude_post_bash.json", full, false); out != "" {
		t.Fatalf("the first output must pass unchanged: %q", out)
	}
	out, code := r.post("claude_post_bash.json", full, false)
	if code != 0 || out == "" {
		t.Fatalf("code %d output %q; log: %s", code, out, r.hookLog())
	}
	var resp map[string]any
	if err := json.Unmarshal(parseOut(t, out).H.Output, &resp); err != nil {
		t.Fatal(err)
	}
	m := dedupRe.FindStringSubmatch(resp["stdout"].(string))
	if m == nil {
		t.Fatalf("no dedup marker in %v", resp)
	}
	rec, err := recovery.Open(filepath.Join(r.dir, "recovery"), recovery.Options{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if got, _, err := rec.Get(m[1], 1, 1<<20); err != nil || strings.TrimRight(got, "\n") != strings.TrimRight(full, "\n") {
		t.Fatalf("the full output must be fetchable: %v", err)
	}
	if k := strings.Join(r.kinds(), ","); k != rwDedup+":rewritten" {
		t.Fatalf("events = %s", k)
	}

	// The guardrail's numerator: the agent fetches the collapsed output back.
	r.fetch(m[1])
	r.fetch(m[1])
	n := 0
	for _, e := range r.events() {
		if e.Kind == KindFetchedBack {
			n++
			if e.Hook != rwDedup {
				t.Fatalf("attributed to %s", e.Hook)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d fetched_back events; want 1", n)
	}
}

func TestOutputDedupStaysShadowWithoutTheMCPServerOrInShadow(t *testing.T) {
	for _, c := range []struct {
		name     string
		state    string
		troveRun bool
	}{{"shadow", "shadow", true}, {"no MCP server", "on", false}} {
		t.Run(c.name, func(t *testing.T) {
			r := dedupRig(t, c.state, c.troveRun)
			full := listing()
			for i := 0; i < 2; i++ {
				if out, _ := r.post("claude_post_bash.json", full, false); out != "" {
					t.Fatalf("nothing may be rewritten: %q", out)
				}
			}
			if k := strings.Join(r.kinds(), ","); k != rwDedup+":would_rewrite" {
				t.Fatalf("events = %s", k)
			}
		})
	}
}

func TestACompactionForgetsWhatWasSeen(t *testing.T) {
	r := dedupRig(t, "on", true)
	full := listing()
	r.post("claude_post_bash.json", full, false)
	r.run(EventReset, r.input("claude_pre_compact.json"))
	if out, _ := r.post("claude_post_bash.json", full, false); out != "" {
		t.Fatalf("after compaction the earlier copy may be gone, so the output must pass: %q", out)
	}
}
