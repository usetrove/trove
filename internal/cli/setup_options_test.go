package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// The options note carries the compliance guidance; its claims are pinned here so
// a rewrite cannot silently drop one (sources and dates: docs/compliance.md).
func TestSetupExplainsCreditsSubscriptionsAndHealthcare(t *testing.T) {
	h := newHarness(t, map[string]string{"TROVE_DATA_DIR": t.TempDir()}, "claude")
	agentHome(t, h)
	if code := h.run("setup", "--json"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	var rep setupReport
	if err := json.Unmarshal(h.out.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(rep.Options, "\n")
	for _, want := range []string{
		"Free, Pro, Max, Ultra, Team or Enterprise", "Trove plugin", // subscription logins are refused
		"Console organization", "Agent SDK", "does not mention `claude -p`", // credits, without overclaiming
		"do not cover interactive Claude Code", "X-Trove-Kind",
		"SigV4", "HIPAA", "plugin only",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("options lack %q", want)
		}
	}
	if strings.Contains(all, "claude -p is covered") {
		t.Error("setup must not claim `claude -p` is covered by credits")
	}
}
