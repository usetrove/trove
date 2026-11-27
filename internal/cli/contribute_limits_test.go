package cli

import (
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/usetrove/trove/internal/contribute"
)

func TestContributePreviewIsExactlyTheUpload(t *testing.T) {
	h, _, hits, last := contributeHarness(t)

	// Before consent: only the JSON, with no install id, and nothing sent.
	if code := h.run("contribute", "--preview", "--no-history"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	var p map[string]any
	if err := json.Unmarshal(h.out.Bytes(), &p); err != nil {
		t.Fatalf("--preview printed more than the payload: %v\n%s", err, h.out)
	}
	if _, ok := p["install_id"]; ok || p["schema_version"] != float64(contribute.SchemaVersion) {
		t.Errorf("preview before consent = %s", h.out)
	}

	h.reset()
	h.run("contribute", "enable", "--yes", "--no-history")
	h.reset()
	h.run("report", "--no-history")
	if atomic.LoadInt32(hits) != 1 {
		t.Fatalf("hits = %d, want 1", atomic.LoadInt32(hits))
	}
	h.reset()
	h.run("contribute", "--preview", "--no-history", "--json") // --json changes nothing: the bytes are the bytes
	if h.out.String() != *last {
		t.Errorf("preview is not byte for byte the upload:\n%s\nvs\n%s", h.out, *last)
	}
	if atomic.LoadInt32(hits) != 1 {
		t.Error("--preview sent something")
	}
}

func TestContributeLimitsConsent(t *testing.T) {
	h, dir, hits, _ := contributeHarness(t)

	// Needs sharing on first, and a plan from the list.
	if code := h.run("contribute", "limits", "enable", "--plan", "max", "--yes", "--no-history"); code == 0 {
		t.Error("limits enabled while sharing is off")
	}
	h.reset()
	h.run("contribute", "enable", "--yes", "--no-history")
	for _, plan := range []string{"", "Max", "my company"} {
		h.reset()
		if code := h.run("contribute", "limits", "enable", "--plan", plan, "--yes", "--no-history"); code == 0 {
			t.Errorf("plan %q accepted", plan)
		}
		if !strings.Contains(h.err.String(), "pro, max, ultra, team, enterprise, unspecified") {
			t.Errorf("error does not list the plans: %s", h.err)
		}
	}

	// Without a yes it shows the consent text and changes nothing.
	h.reset()
	h.run("contribute", "limits", "enable", "--plan", "max", "--no-history")
	if !strings.Contains(h.out.String(), "Share your plan-limit estimate too?") || !strings.Contains(h.out.String(), "Nothing changed") {
		t.Errorf("consent text missing:\n%s", h.out)
	}
	if st, _ := contribute.LoadState(dir); st.Limits {
		t.Fatal("limits turned on without a yes")
	}

	h.reset()
	if code := h.run("contribute", "limits", "enable", "--plan", "max", "--yes", "--no-history"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if st, _ := contribute.LoadState(dir); st.LimitsPlan() != "max" {
		t.Errorf("state after enable = %+v", st)
	}
	h.reset()
	if code := h.run("contribute", "limits", "disable"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if st, _ := contribute.LoadState(dir); st.Limits || st.Plan != "" || !st.Enabled {
		t.Errorf("state after limits disable = %+v (sharing itself should stay on)", st)
	}
	if n := atomic.LoadInt32(hits); n != 0 {
		t.Errorf("limits commands contacted the server %d times", n)
	}
}
